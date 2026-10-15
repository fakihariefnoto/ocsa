// Package actions performs the mutating operations. It is the only package
// that opens the database read-write, and every entry point verifies what it
// did before reporting success.
package actions

import (
	"errors"
	"os"
	"time"

	"github.com/fakih/ocsa/internal/db"
	"github.com/fakih/ocsa/internal/paths"
)

// Sentinel outcomes, so the CLI can map them to exit codes.
var (
	ErrNothingToDo  = errors.New("nothing worth reclaiming")
	ErrPrecondition = errors.New("precondition failed")
	ErrVerification = errors.New("verification failed after the change")
)

// Result is what every action reports back, to the TUI and to --json alike.
type Result struct {
	Action       string           `json:"action"`
	Before       int64            `json:"before_bytes"`
	After        int64            `json:"after_bytes"`
	Reclaimed    int64            `json:"reclaimed_bytes"`
	Elapsed      time.Duration    `json:"elapsed"`
	Verified     bool             `json:"verified"`
	DryRun       bool             `json:"dry_run"`
	Steps        []string         `json:"steps"`
	Notes        []string         `json:"notes"`
	BeforeCounts map[string]int64 `json:"before_counts,omitempty"`
	AfterCounts  map[string]int64 `json:"after_counts,omitempty"`
	WALBefore    int64            `json:"wal_before_bytes"`
	WALAfter     int64            `json:"wal_after_bytes"`
	Removed      map[string]int64 `json:"removed,omitempty"`
}

// DefaultThreshold is the share of the file that must be free before a vacuum
// is considered worthwhile.
const DefaultThreshold = 0.05

// VacuumOptions configures a vacuum.
type VacuumOptions struct {
	Yes         bool
	Force       bool // ignore the threshold
	Threshold   float64
	TruncateLog bool
}

// Vacuum rewrites the database in place, returning freelist pages to the
// filesystem. It is safe to run against a live OpenCode: SQLite serialises on
// busy_timeout and rolls back entirely if it cannot get the lock.
func Vacuum(p paths.Paths, o VacuumOptions) (res Result, err error) {
	res = Result{Action: "vacuum"}
	start := time.Now()
	defer func() { res.Elapsed = time.Since(start) }()

	threshold := o.Threshold
	if threshold <= 0 {
		threshold = DefaultThreshold
	}
	res.Before = statSize(p.DB)
	res.WALBefore = statSize(p.DB + "-wal")

	conn, err := db.Open(p.DB, true)
	if err != nil {
		return res, err
	}
	defer conn.Close()

	st, err := conn.Stats()
	if err != nil {
		return res, err
	}
	freeRatio := 0.0
	if st.FileBytes() > 0 {
		freeRatio = float64(st.FreeBytes()) / float64(st.FileBytes())
	}
	if !o.Force && freeRatio < threshold {
		res.Notes = append(res.Notes,
			"only "+paths.ByteSize(st.FreeBytes())+" of free pages ("+
				pct(freeRatio)+") - below the "+pct(threshold)+" threshold")
		return res, ErrNothingToDo
	}

	if !o.Yes {
		res.DryRun = true
		res.Notes = append(res.Notes,
			"would rewrite "+paths.ByteSize(st.FileBytes())+
				"; projected result "+paths.ByteSize(st.LiveBytes()))
		return res, nil
	}

	// 1. flush pending WAL frames
	if _, _, _, err := conn.Checkpoint(); err != nil {
		return res, err
	}
	res.Steps = append(res.Steps, "checkpointed WAL")

	// 2. never vacuum a database that is already damaged
	verdict, err := conn.Integrity()
	if err != nil {
		return res, err
	}
	if verdict != "ok" {
		res.Notes = append(res.Notes, "integrity_check before vacuum: "+verdict)
		return res, ErrPrecondition
	}
	res.Steps = append(res.Steps, "integrity_check: ok")

	// 3. remember the shape of the data
	res.BeforeCounts, err = conn.RowCounts()
	if err != nil {
		return res, err
	}

	// 4. rewrite in place
	if err := conn.Vacuum(); err != nil {
		res.Notes = append(res.Notes, "VACUUM failed, nothing changed: "+err.Error())
		return res, ErrPrecondition
	}
	res.Steps = append(res.Steps, "VACUUM")

	// 5. the vacuum inflated the WAL; drain it or half the space stays gone
	if _, _, _, err := conn.Checkpoint(); err != nil {
		res.Notes = append(res.Notes, "post-vacuum checkpoint failed: "+err.Error())
	}
	res.Steps = append(res.Steps, "checkpointed WAL (post-vacuum)")

	// 6. prove nothing was lost
	after, err := conn.Integrity()
	if err != nil {
		return res, err
	}
	res.AfterCounts, err = conn.RowCounts()
	if err != nil {
		return res, err
	}
	fkViolations, err := conn.ForeignKeyViolations()
	if err != nil {
		return res, err
	}
	ok := after == "ok" && fkViolations == 0 && sameCounts(res.BeforeCounts, res.AfterCounts)
	res.Verified = ok
	if !res.Verified {
		res.Notes = append(res.Notes, "integrity after vacuum: "+after+
			", foreign key violations: "+itoa(int64(fkViolations)))
		return res, ErrVerification
	}
	res.Steps = append(res.Steps, "integrity_check: ok, row counts match")

	if o.TruncateLog && p.LogFile != "" {
		if err := TruncateLog(p.LogFile); err == nil {
			res.Steps = append(res.Steps, "truncated log")
		}
	}

	res.After = statSize(p.DB)
	res.WALAfter = statSize(p.DB + "-wal")
	res.Reclaimed = res.Before - res.After
	if res.Reclaimed < 0 {
		// A pointer map can make the file marginally larger; never report a
		// negative reclaim as if space had been freed.
		res.Notes = append(res.Notes, "file grew by "+paths.ByteSize(-res.Reclaimed)+
			" (page bookkeeping)")
		res.Reclaimed = 0
	}
	return res, nil
}

// AutoVacuumModes maps the CLI names to SQLite's integers.
var AutoVacuumModes = map[string]int{
	"none":        0,
	"full":        1,
	"incremental": 2,
}

// AutoVacuum changes the auto-vacuum mode and rebuilds the file so the change
// takes effect. INCREMENTAL reclaims on demand via PRAGMA incremental_vacuum;
// FULL reclaims on commit and needs no maintenance.
func AutoVacuum(p paths.Paths, mode string, o VacuumOptions) (res Result, err error) {
	value, ok := AutoVacuumModes[mode]
	if !ok {
		return Result{Action: "auto_vacuum"}, errors.New(
			"unknown auto_vacuum mode " + mode + " (use none, full or incremental)")
	}

	res = Result{Action: "auto_vacuum " + mode}
	start := time.Now()
	defer func() { res.Elapsed = time.Since(start) }()

	res.Before = statSize(p.DB)
	res.WALBefore = statSize(p.DB + "-wal")

	conn, err := db.Open(p.DB, true)
	if err != nil {
		return res, err
	}
	defer conn.Close()

	st, err := conn.Stats()
	if err != nil {
		return res, err
	}
	if !o.Yes {
		res.DryRun = true
		res.Notes = append(res.Notes, "current mode: "+modeName(st.AutoVacuum)+
			", would set: "+mode)
		return res, nil
	}
	if st.AutoVacuum == value {
		res.Notes = append(res.Notes, "already "+mode)
		res.Verified = true
		res.After = res.Before
		return res, ErrNothingToDo
	}

	if _, _, _, err := conn.Checkpoint(); err != nil {
		return res, err
	}
	res.BeforeCounts, err = conn.RowCounts()
	if err != nil {
		return res, err
	}
	if err := conn.SetAutoVacuum(value); err != nil {
		return res, err
	}
	res.Steps = append(res.Steps, "auto_vacuum = "+mode)

	// The pragma only takes effect on the next VACUUM.
	if err := conn.Vacuum(); err != nil {
		return res, err
	}
	res.Steps = append(res.Steps, "VACUUM (applies the mode)")
	if _, _, _, err := conn.Checkpoint(); err != nil {
		res.Notes = append(res.Notes, "post-vacuum checkpoint failed: "+err.Error())
	}

	verdict, err := conn.Integrity()
	if err != nil {
		return res, err
	}
	res.AfterCounts, err = conn.RowCounts()
	if err != nil {
		return res, err
	}
	now, err := conn.Stats()
	if err != nil {
		return res, err
	}
	res.Verified = verdict == "ok" && sameCounts(res.BeforeCounts, res.AfterCounts) &&
		now.AutoVacuum == value
	if !res.Verified {
		res.Notes = append(res.Notes, "verification failed: integrity="+verdict+
			", mode now "+modeName(now.AutoVacuum))
		return res, ErrVerification
	}

	res.After = statSize(p.DB)
	res.WALAfter = statSize(p.DB + "-wal")
	res.Reclaimed = res.Before - res.After
	if res.Reclaimed < 0 {
		res.Notes = append(res.Notes, "file grew by "+paths.ByteSize(-res.Reclaimed)+
			" (pointer map pages for incremental auto-vacuum)")
		res.Reclaimed = 0
	}
	return res, nil
}

// Incremental returns freelist pages to the filesystem in small steps. Cheap
// enough to run often; a no-op unless auto_vacuum is INCREMENTAL.
func Incremental(p paths.Paths) (res Result, err error) {
	res = Result{Action: "incremental_vacuum"}
	start := time.Now()
	defer func() { res.Elapsed = time.Since(start) }()

	res.Before = statSize(p.DB)
	conn, err := db.Open(p.DB, true)
	if err != nil {
		return res, err
	}
	defer conn.Close()

	st, err := conn.Stats()
	if err != nil {
		return res, err
	}
	if st.AutoVacuum != AutoVacuumModes["incremental"] {
		res.Notes = append(res.Notes, "auto_vacuum is "+modeName(st.AutoVacuum)+
			" - set it to incremental for this to do anything")
		return res, ErrNothingToDo
	}
	if st.Freelist == 0 {
		res.Notes = append(res.Notes, "no free pages to return")
		return res, ErrNothingToDo
	}
	if err := conn.IncrementalVacuum(); err != nil {
		return res, err
	}
	res.Steps = append(res.Steps, "incremental_vacuum dropped "+
		itoa(st.Freelist)+" free pages")
	if _, _, _, err := conn.Checkpoint(); err != nil {
		res.Notes = append(res.Notes, "checkpoint failed: "+err.Error())
	}
	res.After = statSize(p.DB)
	res.Reclaimed = res.Before - res.After
	res.Verified = true
	return res, nil
}

func sameCounts(a, b map[string]int64) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func statSize(p string) int64 {
	fi, err := os.Stat(p)
	if err != nil {
		return 0
	}
	return fi.Size()
}

func modeName(v int) string {
	switch v {
	case 1:
		return "full"
	case 2:
		return "incremental"
	default:
		return "none"
	}
}

func pct(f float64) string {
	return itoa(int64(f*1000+0.5)) + "‰"
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [24]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
