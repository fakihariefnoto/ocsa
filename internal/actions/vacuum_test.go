package actions

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/fakih/ocsa/internal/db"
	"github.com/fakih/ocsa/internal/paths"
)

// fixture builds a database with a table big enough that deleting a run of
// rows empties whole pages, which is what puts pages on the freelist.
func fixture(t *testing.T, rows, size int) paths.Paths {
	t.Helper()
	p := paths.Paths{}.WithDataDir(t.TempDir())
	conn, err := db.Create(p.DB)
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer conn.Close()

	q := `WITH RECURSIVE c(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM c WHERE n < ` +
		itoa(int64(rows)) + `)
		INSERT INTO t SELECT randomblob(` + itoa(int64(size)) + `) FROM c`
	if _, err := conn.SQL.Exec("CREATE TABLE t(x)"); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := conn.SQL.Exec(q); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if _, err := conn.SQL.Exec("DELETE FROM t WHERE rowid <= " + itoa(int64(rows/2))); err != nil {
		t.Fatalf("delete: %v", err)
	}
	return p
}

func TestVacuumReclaimsAndIsIdempotent(t *testing.T) {
	p := fixture(t, 3000, 8000)

	before, err := os.Stat(p.DB)
	if err != nil {
		t.Fatal(err)
	}

	res, err := Vacuum(p, VacuumOptions{Yes: true, Force: true})
	if err != nil {
		t.Fatalf("vacuum: %v", err)
	}
	if !res.Verified {
		t.Error("vacuum reported success but did not verify")
	}
	if res.Reclaimed <= 0 {
		t.Errorf("expected a positive reclaim, got %d", res.Reclaimed)
	}
	after, err := os.Stat(p.DB)
	if err != nil {
		t.Fatal(err)
	}
	if after.Size() >= before.Size() {
		t.Errorf("file did not shrink: %d -> %d", before.Size(), after.Size())
	}
	if res.After != after.Size() {
		t.Errorf("reported size %d does not match disk %d", res.After, after.Size())
	}

	// Running it again with the threshold in force must be a no-op, not a
	// second rewrite.
	if _, err := Vacuum(p, VacuumOptions{Yes: true}); !errors.Is(err, ErrNothingToDo) {
		t.Errorf("second vacuum: want ErrNothingToDo, got %v", err)
	}
	// And so must an explicitly forced second pass: there is nothing to gain.
	res2, err := Vacuum(p, VacuumOptions{Yes: true, Force: true})
	if err != nil {
		t.Fatalf("forced second vacuum: %v", err)
	}
	if res2.Reclaimed != 0 {
		t.Errorf("forced second vacuum reclaimed %d bytes, want 0", res2.Reclaimed)
	}
}

func TestVacuumIsDryRunWithoutYes(t *testing.T) {
	p := fixture(t, 3000, 8000)
	before, _ := os.Stat(p.DB)

	res, err := Vacuum(p, VacuumOptions{Force: true})
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if !res.DryRun {
		t.Error("expected a dry run")
	}
	after, _ := os.Stat(p.DB)
	if before.Size() != after.Size() {
		t.Errorf("dry run changed the file: %d -> %d", before.Size(), after.Size())
	}
}

// TestVacuumVerifiesRowCounts is the regression that matters most: a vacuum
// that silently dropped rows must fail, not report success.
func TestVacuumVerifiesRowCounts(t *testing.T) {
	p := fixture(t, 500, 4000)
	conn, err := db.Create(p.DB)
	if err != nil {
		t.Fatal(err)
	}
	counts, err := conn.RowCounts()
	if err != nil {
		t.Fatal(err)
	}
	if !sameCounts(counts, counts) {
		t.Error("sameCounts is not reflexive")
	}
	counts["t"] = 999
	if sameCounts(counts, map[string]int64{"t": 1}) {
		t.Error("sameCounts accepted different values")
	}
	conn.Close()
}

func TestAutoVacuumSwitchesMode(t *testing.T) {
	p := fixture(t, 2000, 4000)

	res, err := AutoVacuum(p, "incremental", VacuumOptions{Yes: true})
	if err != nil {
		t.Fatalf("auto vacuum: %v", err)
	}
	if !res.Verified {
		t.Error("mode change did not verify")
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
	if st.AutoVacuum != AutoVacuumModes["incremental"] {
		t.Errorf("auto_vacuum is %d, want 2", st.AutoVacuum)
	}

	// Already in that mode: nothing to do.
	if _, err := AutoVacuum(p, "incremental", VacuumOptions{Yes: true}); !errors.Is(err, ErrNothingToDo) {
		t.Errorf("re-setting the same mode: want ErrNothingToDo, got %v", err)
	}
	if _, err := AutoVacuum(p, "bogus", VacuumOptions{Yes: true}); err == nil {
		t.Error("unknown mode should be an error")
	}
}

func TestCleanToleratesMissingDirectories(t *testing.T) {
	p := paths.Paths{}.WithDataDir(t.TempDir())

	res, err := Clean(p, CleanOptions{Yes: true, Log: true, ToolOutput: true, Snapshots: true})
	if !errors.Is(err, ErrNothingToDo) {
		t.Fatalf("want ErrNothingToDo on an empty install, got %v (%+v)", err, res)
	}
	for _, note := range res.Notes {
		if strings.Contains(note, "no such file") {
			t.Errorf("missing directory surfaced as an error note: %q", note)
		}
	}
}

func TestCleanTruncatesLogAndKeepsTail(t *testing.T) {
	p := paths.Paths{}.WithDataDir(t.TempDir())
	if err := os.MkdirAll(p.Log, 0o755); err != nil {
		t.Fatal(err)
	}
	payload := strings.Repeat("a", 1000) + "TAIL"
	if err := os.WriteFile(p.LogFile, []byte(payload), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := Clean(p, CleanOptions{Yes: true, Log: true, KeepLog: 64})
	if err != nil {
		t.Fatalf("clean: %v", err)
	}
	if res.Reclaimed == 0 {
		t.Error("expected bytes reclaimed")
	}
	got, err := os.ReadFile(p.LogFile)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 64 || string(got) != payload[len(payload)-64:] {
		t.Errorf("log tail wrong: got %d bytes %q", len(got), string(got))
	}
}

func TestPruneSnapshotsKeepsNewest(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 5; i++ {
		sub := dir + "/snap" + itoa(int64(i))
		if err := os.MkdirAll(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(sub+"/obj", make([]byte, 1024), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	freed, n, err := PruneSnapshots(dir, 2, 0)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if n != 3 || freed != 3*1024 {
		t.Errorf("pruned %d dirs / %d bytes, want 3 / 3072", n, freed)
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 2 {
		t.Errorf("%d snapshots left, want 2", len(ents))
	}
}
