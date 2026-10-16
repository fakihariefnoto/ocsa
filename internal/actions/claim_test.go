package actions

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fakih/ocsa/internal/db"
	"github.com/fakih/ocsa/internal/paths"
	"github.com/fakih/ocsa/internal/storage"
)

// claimFixture builds a bloated installation with a fat log, which is what a
// claim has to deal with.
func claimFixture(t *testing.T) (*storage.Scan, paths.Paths) {
	t.Helper()
	p := fixture(t, 3000, 8000) // reuses the vacuum fixture: rows deleted, pages free
	if err := os.MkdirAll(p.Log, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.LogFile, make([]byte, 3<<20), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := storage.Run(p)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	return s, p
}

// TestPlanMarksStepsPlanned is the regression for a bug that would have shipped
// silently: step status defaulted to empty, so Planned() returned nothing and a
// confirmed claim rewrote no database at all while reporting success.
func TestPlanMarksStepsPlanned(t *testing.T) {
	s, _ := claimFixture(t)
	plan := BuildPlan(s, ClaimOptions{}).Normalize()

	if len(plan.Planned()) == 0 {
		t.Fatal("no steps are planned, so a confirmed claim would do nothing")
	}
	for _, st := range plan.Steps {
		if st.Status == "" {
			t.Errorf("step %q has no status", st.ID)
		}
	}
	var vacuum *Step
	for i := range plan.Steps {
		if strings.HasPrefix(plan.Steps[i].ID, "vacuum") {
			vacuum = &plan.Steps[i]
		}
	}
	if vacuum == nil {
		t.Fatal("no vacuum step in the plan")
	}
	if vacuum.Status != StepPlanned {
		t.Errorf("vacuum is %q, want planned", vacuum.Status)
	}
	if plan.Projected <= 0 {
		t.Errorf("projected bytes = %d, want a positive estimate", plan.Projected)
	}

	// The destructive step must be opt-in, and must say why.
	var snap *Step
	for i := range plan.Steps {
		if plan.Steps[i].ID == "snapshots" {
			snap = &plan.Steps[i]
		}
	}
	if snap == nil {
		t.Fatal("the snapshots step is not even shown")
	}
	if snap.Status != StepSkipped {
		t.Error("snapshots are planned by default; they should be opt-in")
	}
	if snap.Reason == "" {
		t.Error("a skipped step must say why it is skipped")
	}

	withSnaps := BuildPlan(s, ClaimOptions{IncludeSnapshots: true}).Normalize()
	for _, st := range withSnaps.Steps {
		if st.ID == "snapshots" && st.Status != StepPlanned {
			t.Error("--include-snapshots did not plan the snapshot prune")
		}
	}
}

func TestClaimReclaimsAndTurnsOnAutoVacuum(t *testing.T) {
	s, p := claimFixture(t)

	before, err := os.Stat(p.DB)
	if err != nil {
		t.Fatal(err)
	}

	plan := BuildPlan(s, ClaimOptions{}).Normalize()
	res, err := Claim(p, plan, ClaimOptions{Yes: true})
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if !res.Verified {
		t.Error("claim reported success without verifying")
	}
	if res.Reclaimed <= 0 {
		t.Errorf("reclaimed %d bytes, want a positive number", res.Reclaimed)
	}

	after, err := os.Stat(p.DB)
	if err != nil {
		t.Fatal(err)
	}
	if after.Size() >= before.Size() {
		t.Errorf("database did not shrink: %d -> %d", before.Size(), after.Size())
	}

	// The database work must have happened: not just the log trim.
	var rewrote bool
	for _, out := range res.Outcomes {
		if strings.HasPrefix(out.Step.ID, "vacuum") && out.Err == "" {
			rewrote = true
		}
	}
	if !rewrote {
		t.Errorf("no vacuum step ran: %+v", res.Outcomes)
	}

	conn, err := db.Open(p.DB, false)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	st, err := conn.Stats()
	if err != nil {
		t.Fatal(err)
	}
	if st.AutoVacuum != db.AutoVacuumIncremental {
		t.Errorf("auto_vacuum = %d, want %d so this does not come back",
			st.AutoVacuum, db.AutoVacuumIncremental)
	}
	if st.Freelist != 0 {
		t.Errorf("freelist = %d, want 0 after a rewrite", st.Freelist)
	}

	// The log trim is outside the database, and must also have happened.
	if fi, err := os.Stat(p.LogFile); err != nil {
		t.Fatal(err)
	} else if fi.Size() > 2<<20 {
		t.Errorf("log is still %d bytes, expected it trimmed to the 1 MB tail", fi.Size())
	}
}

func TestClaimIsDryRunWithoutYes(t *testing.T) {
	s, p := claimFixture(t)
	before, _ := os.Stat(p.DB)

	plan := BuildPlan(s, ClaimOptions{}).Normalize()
	res, err := Claim(p, plan, ClaimOptions{})
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if !res.DryRun {
		t.Error("expected a dry run")
	}
	after, _ := os.Stat(p.DB)
	if before.Size() != after.Size() {
		t.Errorf("dry run changed the database: %d -> %d", before.Size(), after.Size())
	}
	if fi, _ := os.Stat(p.LogFile); fi.Size() == 0 {
		t.Error("dry run truncated the log")
	}
}

// TestClaimRefusesWhileOpenCodeIsWriting is the safety property: a claim must
// not barge into an agent turn that is mid-write.
func TestClaimRefusesWhileOpenCodeIsWriting(t *testing.T) {
	s, p := claimFixture(t)

	// Hold the write lock the way a busy agent turn would. This has to be a
	// pinned connection: a pooled one gets recycled, and database/sql may reset
	// the session, silently dropping the transaction and the lock with it.
	release, err := db.HoldWriteLock(p.DB)
	if err != nil {
		t.Fatalf("could not take the write lock for the test: %v", err)
	}
	defer release()

	held, err := db.WriteLockHeld(p.DB)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if !held {
		t.Fatal("probe did not notice the write lock")
	}

	before, _ := os.Stat(p.DB)
	plan := BuildPlan(s, ClaimOptions{}).Normalize()
	_, err = Claim(p, plan, ClaimOptions{Yes: true, Wait: 300 * time.Millisecond, QuietFor: 200 * time.Millisecond})
	if !errors.Is(err, ErrPrecondition) {
		t.Fatalf("claim while a writer is active: got %v, want ErrPrecondition", err)
	}
	after, _ := os.Stat(p.DB)
	if before.Size() != after.Size() {
		t.Errorf("a refused claim still changed the database: %d -> %d",
			before.Size(), after.Size())
	}
}

// TestWaitForQuietReturnsWhenTheWriterStops covers the other half: the claim
// should proceed on its own once the writer is done, not just give up.
func TestWaitForQuietReturnsWhenTheWriterStops(t *testing.T) {
	s, p := claimFixture(t)

	release, err := db.HoldWriteLock(p.DB)
	if err != nil {
		t.Fatal(err)
	}

	// Hold long enough to be observed: the claim does one process-table read
	// before it starts polling, which can take a few hundred milliseconds.
	released := make(chan struct{})
	go func() {
		time.Sleep(1200 * time.Millisecond)
		release()
		close(released)
	}()
	defer release()

	plan := BuildPlan(s, ClaimOptions{}).Normalize()
	res, err := Claim(p, plan, ClaimOptions{
		Yes: true, Wait: 20 * time.Second, QuietFor: 300 * time.Millisecond,
	})
	<-released
	if err != nil {
		t.Fatalf("claim after the writer stopped: %v", err)
	}
	if !res.Verified {
		t.Error("claim did not verify")
	}
	var waited bool
	for _, n := range res.Notes {
		if strings.Contains(n, "waited") {
			waited = true
		}
	}
	if !waited {
		t.Errorf("claim did not report that it waited: %v", res.Notes)
	}
}

func TestAcquireLockIsExclusive(t *testing.T) {
	dir := t.TempDir()
	first, err := AcquireLock(dir)
	if err != nil {
		t.Fatalf("first lock: %v", err)
	}
	if _, err := AcquireLock(dir); err == nil {
		t.Error("a second lock was granted; two claims could run at once")
	} else if !strings.Contains(err.Error(), "another ocsa") {
		t.Errorf("unhelpful refusal message: %v", err)
	}
	if err := first.Release(); err != nil {
		t.Fatalf("release: %v", err)
	}
	// Released, so it can be taken again.
	second, err := AcquireLock(dir)
	if err != nil {
		t.Fatalf("re-acquire after release: %v", err)
	}
	second.Release()
}

func TestToolOutputPrunableCountsWithoutDeleting(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "old")
	if err := os.WriteFile(old, make([]byte, 2048), 0o644); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "new"), make([]byte, 1024), 0o644); err != nil {
		t.Fatal(err)
	}

	bytes, n := ToolOutputPrunable(dir, 24*time.Hour)
	if bytes != 2048 || n != 1 {
		t.Errorf("got %d bytes / %d files, want 2048 / 1", bytes, n)
	}
	if _, err := os.Stat(old); err != nil {
		t.Error("ToolOutputPrunable deleted a file; it must only report")
	}
}

func TestPlanExplainsSkippedSteps(t *testing.T) {
	dir := t.TempDir()
	p := paths.Paths{}.WithDataDir(dir)
	conn, err := db.Create(p.DB)
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	s, err := storage.Run(p)
	if err != nil {
		t.Fatal(err)
	}
	plan := BuildPlan(s, ClaimOptions{}).Normalize()
	lines := plan.StepsText()
	if len(lines) != len(plan.Steps) {
		t.Fatalf("StepsText produced %d lines for %d steps", len(lines), len(plan.Steps))
	}
	// An empty install has nothing to do, and must say so rather than showing
	// a blank plan.
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "no free pages") {
		t.Errorf("empty install does not explain itself:\n%s", joined)
	}
	if plan.Projected != 0 {
		t.Errorf("projected %d bytes on an empty install", plan.Projected)
	}
}
