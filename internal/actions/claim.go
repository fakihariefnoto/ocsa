package actions

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/fakih/ocsa/internal/db"
	"github.com/fakih/ocsa/internal/paths"
	"github.com/fakih/ocsa/internal/storage"
)

// StepStatus says whether a step will run.
type StepStatus string

const (
	StepPlanned StepStatus = "planned"
	StepSkipped StepStatus = "skipped"
)

// Step is one thing the claim will do, or deliberately will not.
type Step struct {
	ID        string     `json:"id"`
	Label     string     `json:"label"`
	Detail    string     `json:"detail"`
	Projected int64      `json:"projected_bytes"`
	Status    StepStatus `json:"status"`
	Reason    string     `json:"reason,omitempty"` // why it is skipped
	OptIn     bool       `json:"opt_in"`
}

// Plan is the whole proposal: what will be freed, what will be left alone, and
// what is happening to the database while it runs. The user confirms this
// verbatim, and the same plan is what executes.
type Plan struct {
	Steps     []Step         `json:"steps"`
	Activity  ActivityReport `json:"activity"`
	Projected int64          `json:"projected_bytes"`
	Frozen    int64          `json:"frozen_bytes"`
	Snapshots int64          `json:"snapshots_bytes"`
}

// Planned returns the steps that will actually run.
func (p Plan) Planned() []Step {
	var out []Step
	for _, s := range p.Steps {
		if s.Status == StepPlanned {
			out = append(out, s)
		}
	}
	return out
}

// ActivityReport is the concurrency picture at plan time.
type ActivityReport struct {
	Running   bool     `json:"running"`
	Writers   bool     `json:"write_lock_held"`
	Summary   string   `json:"summary"`
	Holders   []string `json:"holders,omitempty"`
	WALBytes  int64    `json:"wal_bytes"`
	SafeIfRun bool     `json:"safe_to_run_now"`
}

// ClaimOptions configures the plan and the run.
type ClaimOptions struct {
	Yes              bool
	Force            bool // run even if a writer is active right now
	Wait             time.Duration
	QuietFor         time.Duration
	IncludeSnapshots bool
	SnapshotKeep     int
	ToolOutputAge    time.Duration
	KeepLogBytes     int64
}

// DefaultWait and DefaultQuietFor are how long to wait for a gap between agent
// turns before taking the write lock. OpenCode commits between turns, so a
// window of a second or two nearly always appears quickly.
const (
	DefaultWait     = 30 * time.Second
	DefaultQuietFor = 2 * time.Second
)

func (o ClaimOptions) wait() time.Duration {
	if o.Wait > 0 {
		return o.Wait
	}
	return DefaultWait
}

func (o ClaimOptions) quietFor() time.Duration {
	if o.QuietFor > 0 {
		return o.QuietFor
	}
	return DefaultQuietFor
}

func (o ClaimOptions) toolOutputAge() time.Duration {
	if o.ToolOutputAge > 0 {
		return o.ToolOutputAge
	}
	return 7 * 24 * time.Hour
}

func (o ClaimOptions) keepLog() int64 {
	if o.KeepLogBytes > 0 {
		return o.KeepLogBytes
	}
	return 1 << 20
}

func (o ClaimOptions) snapshotKeep() int {
	if o.SnapshotKeep > 0 {
		return o.SnapshotKeep
	}
	return storage.SnapshotKeepRecent()
}

// BuildPlan decides what a claim would do to this installation and why. It
// touches nothing, so it is what the confirmation screen renders and the CLI
// prints before asking.
func BuildPlan(s *storage.Scan, o ClaimOptions) Plan {
	p := Plan{Frozen: s.Reclaim.LegacyBytes, Snapshots: s.Reclaim.SnapshotTotal}
	// Steps are appended as "will run" and Normalize fills that in, so a step
	// is only skipped when something says so explicitly.

	// The database: one rewrite does the vacuum and the auto-vacuum change, so
	// they are a single step rather than two passes over the same file. There
	// is no point rewriting a file with nothing to drop, so setting the mode
	// waits for a rewrite worth doing, and is reported as skipped meanwhile
	// rather than quietly not happening.
	switch {
	case s.Reclaim.DBFree > 0 && s.Stats.AutoVacuum == 0:
		p.Steps = append(p.Steps, Step{
			ID: "vacuum+autovacuum", Label: "Rewrite the database and turn on auto_vacuum",
			Detail:    "drops free pages now, and lets SQLite return future ones incrementally",
			Projected: s.Reclaim.DBFree,
		})
	case s.Reclaim.DBFree > 0:
		p.Steps = append(p.Steps, Step{
			ID: "vacuum", Label: "Rewrite the database",
			Detail: fmt.Sprintf("drops %s of free pages held inside the file",
				paths.ByteSize(s.Reclaim.DBFree)),
			Projected: s.Reclaim.DBFree,
		})
	default:
		p.Steps = append(p.Steps, Step{
			ID: "vacuum", Label: "Rewrite the database", Status: StepSkipped,
			Reason: "no free pages to drop",
		})
		if s.Stats.AutoVacuum == 0 {
			p.Steps = append(p.Steps, Step{
				ID: "autovacuum", Label: "Turn on auto_vacuum", Status: StepSkipped,
				Reason: "nothing to rewrite yet; use the menu action to set it alone",
			})
		}
	}

	p.Steps = append(p.Steps, Step{
		ID: "checkpoint", Label: "Drain the write-ahead log",
		Detail:    "the rewrite inflates the log; without this half the space stays held",
		Projected: s.WALBytes,
	})

	if s.Reclaim.LogReclaimable > 0 {
		p.Steps = append(p.Steps, Step{
			ID: "log", Label: "Trim the log",
			Detail:    fmt.Sprintf("keeps the last %s", paths.ByteSize(o.keepLog())),
			Projected: s.Reclaim.LogReclaimable,
		})
	} else {
		p.Steps = append(p.Steps, Step{
			ID: "log", Label: "Trim the log", Status: StepSkipped,
			Reason: "already small",
		})
	}

	toolBytes, toolCount := ToolOutputPrunable(s.Paths.ToolOutput, o.toolOutputAge())
	if toolBytes > 0 {
		p.Steps = append(p.Steps, Step{
			ID: "tool-output", Label: "Prune old tool output",
			Detail: fmt.Sprintf("%d files older than %s",
				toolCount, shortDuration(o.toolOutputAge())),
			Projected: toolBytes,
		})
	} else {
		p.Steps = append(p.Steps, Step{
			ID: "tool-output", Label: "Prune old tool output", Status: StepSkipped,
			Reason: "nothing older than " + shortDuration(o.toolOutputAge()),
		})
	}

	if s.Reclaim.OrphanBytes > 0 || orphansPresent(s) {
		var n int64
		for _, or := range s.Orphans {
			n += or.Count
		}
		p.Steps = append(p.Steps, Step{
			ID: "orphans", Label: "Delete orphaned rows",
			Detail:    fmt.Sprintf("%d rows whose parent is gone", n),
			Projected: s.Reclaim.OrphanBytes,
		})
	}

	// Snapshots are the one destructive thing here: they hold revert data for
	// old sessions. Never default, always visible.
	snapStep := Step{
		ID: "snapshots", Label: "Prune old snapshots",
		Detail:    fmt.Sprintf("keeps the newest %d; breaks revert for older sessions", o.snapshotKeep()),
		Projected: s.Reclaim.SnapshotPrunable, OptIn: true,
	}
	if o.IncludeSnapshots {
		snapStep.Status = StepPlanned
	} else {
		snapStep.Status = StepSkipped
		snapStep.Reason = "off by default: it would break revert for old sessions"
	}
	p.Steps = append(p.Steps, snapStep)

	for _, st := range p.Steps {
		if st.Status == StepPlanned {
			p.Projected += st.Projected
		}
	}
	return p
}

// Normalize fills in the default status. Steps are appended as "will run"
// unless something explicitly skips them, and forgetting to set the status used
// to mean a step silently did not run at all.
func (p Plan) Normalize() Plan {
	for i := range p.Steps {
		if p.Steps[i].Status == "" {
			p.Steps[i].Status = StepPlanned
		}
	}
	p.Projected = 0
	for _, st := range p.Steps {
		if st.Status == StepPlanned {
			p.Projected += st.Projected
		}
	}
	return p
}

// orphanBytesFallback keeps the orphan step visible when only counts are known.
func orphansPresent(s *storage.Scan) bool {
	for _, o := range s.Orphans {
		if o.Count > 0 {
			return true
		}
	}
	return false
}

func shortDuration(d time.Duration) string {
	switch {
	case d >= 24*time.Hour:
		return fmt.Sprintf("%d days", int(d.Hours()/24))
	case d >= time.Hour:
		return fmt.Sprintf("%d hours", int(d.Hours()))
	default:
		return fmt.Sprintf("%d minutes", int(d.Minutes()))
	}
}

// StepOutcome is what a step actually did.
type StepOutcome struct {
	Step  Step   `json:"step"`
	Bytes int64  `json:"bytes_reclaimed"`
	Note  string `json:"note,omitempty"`
	Err   string `json:"error,omitempty"`
}

// ClaimResult is the aggregate outcome, plus a line per step.
type ClaimResult struct {
	Result
	Outcomes []StepOutcome  `json:"outcomes,omitempty"`
	Activity ActivityReport `json:"activity"`
	Waited   time.Duration  `json:"waited"`
}

// Claim executes a plan. It takes an advisory lock so two ocsa processes cannot
// fight, waits for a gap in the writes when OpenCode is being used, and
// verifies the database after the rewrite before reporting success.
func Claim(p paths.Paths, plan Plan, o ClaimOptions) (res ClaimResult, err error) {
	start := time.Now()
	res = ClaimResult{Result: Result{Action: "claim storage"}}
	defer func() { res.Elapsed = time.Since(start) }()

	lock, err := AcquireLock(p.Data)
	if err != nil {
		return res, fmt.Errorf("%w: %v", ErrPrecondition, err)
	}
	defer lock.Release()

	// Know what we are walking into before touching anything.
	activity, err := db.Probe(p.DB)
	if err != nil {
		return res, err
	}
	res.Activity = report(activity)

	if activity.WriteLockHeld && !o.Force {
		waited, final, werr := db.WaitForQuiet(p.DB, o.wait(), o.quietFor(), nil)
		if werr != nil {
			return res, werr
		}
		res.Activity = report(final)
		if waited {
			res.Waited = o.wait()
			res.Notes = append(res.Notes,
				"waited for OpenCode to finish what it was writing")
		} else {
			res.Notes = append(res.Notes,
				"OpenCode is writing continuously ("+final.Summary()+"); "+
					"nothing was changed. Retry, or pass --force to take the write lock anyway")
			return res, ErrPrecondition
		}
	} else if activity.Open && !activity.WriteLockHeld {
		res.Notes = append(res.Notes,
			"OpenCode is running but idle, so the rewrite does not disturb it")
	}

	if !o.Yes {
		res.DryRun = true
		res.Notes = append(res.Notes, "dry run: nothing was changed")
		return res, nil
	}

	res.Before = statSize(p.DB)
	res.WALBefore = statSize(p.DB + "-wal")

	// The database work: one connection, one write transaction at a time.
	conn, err := db.Open(p.DB, true)
	if err != nil {
		return res, err
	}
	defer conn.Close()

	if res.BeforeCounts, err = conn.RowCounts(); err != nil {
		return res, err
	}
	if verdict, err := conn.Integrity(); err != nil {
		return res, err
	} else if verdict != "ok" {
		res.Notes = append(res.Notes, "integrity_check before the rewrite: "+verdict)
		return res, ErrPrecondition
	}

	for _, st := range plan.Planned() {
		out := StepOutcome{Step: st}
		switch st.ID {
		case "vacuum", "vacuum+autovacuum":
			if st.ID == "vacuum+autovacuum" {
				if err := conn.SetAutoVacuum(db.AutoVacuumIncremental); err != nil {
					out.Err = err.Error()
					break
				}
				out.Note = "auto_vacuum set to incremental"
			}
			if err := conn.Vacuum(); err != nil {
				out.Err = err.Error()
				break
			}
			out.Note = strings.TrimSpace(out.Note + " database rewritten in place")
		case "checkpoint":
			if _, _, _, err := conn.Checkpoint(); err != nil {
				out.Err = err.Error()
				break
			}
			out.Note = "write-ahead log drained"
		case "orphans":
			sr, serr := SweepOrphans(p, true)
			if serr != nil && !errors.Is(serr, ErrNothingToDo) {
				out.Err = serr.Error()
				break
			}
			out.Bytes = sr.Reclaimed
			out.Note = strings.Join(sr.Steps, "; ")
		case "log":
			freed, lerr := CleanLogTo(p.LogFile, o.keepLog())
			if lerr != nil {
				out.Err = lerr.Error()
				break
			}
			out.Bytes = freed
			out.Note = "kept the tail of the log"
		case "tool-output":
			freed, n, perr := PruneToolOutput(p.ToolOutput, o.toolOutputAge())
			if perr != nil {
				out.Err = perr.Error()
				break
			}
			out.Bytes = freed
			out.Note = fmt.Sprintf("removed %d files", n)
		case "snapshots":
			freed, n, serr := PruneSnapshots(p.Snapshot, o.snapshotKeep(), 0)
			if serr != nil {
				out.Err = serr.Error()
				break
			}
			out.Bytes = freed
			out.Note = fmt.Sprintf("removed %d snapshots", n)
		}
		res.Outcomes = append(res.Outcomes, out)
		if out.Err != "" {
			res.Notes = append(res.Notes, st.Label+": "+out.Err)
		}
	}

	// Verify the database, which is the part that can actually lose data.
	if res.AfterCounts, err = conn.RowCounts(); err != nil {
		return res, err
	}
	verdict, err := conn.Integrity()
	if err != nil {
		return res, err
	}
	fkViolations, err := conn.ForeignKeyViolations()
	if err != nil {
		return res, err
	}
	stats, err := conn.Stats()
	if err != nil {
		return res, err
	}
	res.Verified = verdict == "ok" && fkViolations == 0 &&
		sameCounts(res.BeforeCounts, res.AfterCounts)
	if !res.Verified {
		res.Notes = append(res.Notes,
			fmt.Sprintf("verification failed: integrity=%s, foreign key violations=%d",
				verdict, fkViolations))
		return res, ErrVerification
	}
	res.Steps = append(res.Steps,
		"verified: integrity ok, foreign keys clean, row counts identical")

	if stats.AutoVacuum != 0 {
		res.Steps = append(res.Steps, "auto_vacuum is now "+modeName(stats.AutoVacuum)+
			", so future free pages can be returned without a rewrite")
	}

	res.After = statSize(p.DB)
	res.WALAfter = statSize(p.DB + "-wal")
	res.Reclaimed = res.Before - res.After
	for _, out := range res.Outcomes {
		res.Reclaimed += out.Bytes
	}
	if res.Reclaimed < 0 {
		// Do not guess at the cause: OpenCode may have written during the run,
		// and a wrong explanation is worse than none.
		res.Notes = append(res.Notes, "the file grew by "+
			paths.ByteSize(-res.Reclaimed)+" during the run; something else is "+
			"writing to it as well")
		res.Reclaimed = 0
	}
	return res, nil
}

func report(a db.Activity) ActivityReport {
	r := ActivityReport{
		Running:  a.Open,
		Writers:  a.WriteLockHeld,
		Summary:  a.Summary(),
		WALBytes: a.WALBytes,
	}
	for _, h := range a.Holders {
		r.Holders = append(r.Holders, h.String())
	}
	r.SafeIfRun = !a.WriteLockHeld
	return r
}

// StepsText renders the plan for a terminal or a TUI, one line per step, with
// the reasoning attached. Skipped steps are shown, not hidden: the point of the
// confirmation is that nothing happens silently.
func (p Plan) StepsText() []string {
	var out []string
	for _, s := range p.Steps {
		mark := "x"
		if s.Status == StepSkipped {
			mark = "-"
		}
		line := fmt.Sprintf("[%s] %-44s %10s  %s", mark, s.Label,
			paths.ByteSize(s.Projected), s.Detail)
		if s.Status == StepSkipped && s.Reason != "" {
			line = fmt.Sprintf("[%s] %-44s %10s  %s", mark, s.Label, "-", s.Reason)
		}
		out = append(out, strings.TrimRight(line, " "))
	}
	return out
}
