package actions

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"github.com/fakih/ocsa/internal/db"
	"github.com/fakih/ocsa/internal/paths"
)

// CleanOptions configures the guard-rail cleanups around the database.
type CleanOptions struct {
	Yes          bool
	Log          bool
	KeepLog      int64
	ToolOutput   bool
	ToolOutAge   time.Duration
	Snapshots    bool
	SnapshotAge  time.Duration
	SnapshotKeep int
	Orphans      bool
}

// CleanLog truncates the opencode log. Safe while OpenCode is running: it
// appends, it does not seek.
func CleanLog(path string) (int64, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	size := fi.Size()
	if err := os.Truncate(path, 0); err != nil {
		return 0, err
	}
	return size, nil
}

// TruncateLog is CleanLog without the size, for use inside other actions.
func TruncateLog(path string) error {
	_, err := CleanLog(path)
	return err
}

// CleanLogTo keeps the last keepBytes of the log, if it is a plain file.
func CleanLogTo(path string, keepBytes int64) (int64, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	size := fi.Size()
	if keepBytes <= 0 || size <= keepBytes {
		return 0, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	tail := data[size-keepBytes:]
	if err := os.WriteFile(path, tail, fi.Mode().Perm()); err != nil {
		return 0, err
	}
	return size - keepBytes, nil
}

// PruneToolOutput deletes spilled tool results older than age.
func PruneToolOutput(dir string, age time.Duration) (int64, int, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, 0, nil
		}
		return 0, 0, err
	}
	cutoff := time.Now().Add(-age)
	var freed int64
	var n int
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if age > 0 && info.ModTime().After(cutoff) {
			continue
		}
		freed += info.Size()
		if err := os.Remove(filepath.Join(dir, e.Name())); err == nil {
			n++
		}
	}
	return freed, n, nil
}

// PruneSnapshots deletes snapshot directories, keeping the newest keep and
// skipping anything younger than age. Deleting a snapshot breaks revert for the
// session it belongs to, so this is opt-in from the CLI.
func PruneSnapshots(dir string, keep int, age time.Duration) (int64, int, error) {
	type item struct {
		path string
		size int64
		mod  time.Time
	}
	var items []item
	ents, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, 0, nil
		}
		return 0, 0, err
	}
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		p := filepath.Join(dir, e.Name())
		info, err := e.Info()
		if err != nil {
			continue
		}
		items = append(items, item{p, dirSize(p), info.ModTime()})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].mod.After(items[j].mod) })

	cutoff := time.Time{}
	if age > 0 {
		cutoff = time.Now().Add(-age)
	}
	var freed int64
	var n int
	for i, it := range items {
		if i < keep {
			continue
		}
		if !cutoff.IsZero() && it.mod.After(cutoff) {
			continue
		}
		if err := os.RemoveAll(it.path); err == nil {
			freed += it.size
			n++
		}
	}
	return freed, n, nil
}

// orphanSweeps mirror storage.OrphanQueries as count/delete pairs.
var orphanSweeps = []struct {
	Label, Table, Where string
}{
	{"message", "message", "session_id NOT IN (SELECT id FROM session)"},
	{"part", "part", "session_id NOT IN (SELECT id FROM session)"},
	{"part_message", "part", "message_id NOT IN (SELECT id FROM message)"},
	{"session_message", "session_message", "session_id NOT IN (SELECT id FROM session_v2)"},
}

// SweepOrphans deletes rows whose parent is gone. The audit found none on a
// real install, so this mostly exists to prove the check stays true.
func SweepOrphans(p paths.Paths, yes bool) (res Result, err error) {
	res = Result{Action: "sweep orphans", Removed: map[string]int64{}}
	start := time.Now()
	defer func() { res.Elapsed = time.Since(start) }()

	conn, err := db.Open(p.DB, true)
	if err != nil {
		return res, err
	}
	defer conn.Close()

	total := int64(0)
	for _, sw := range orphanSweeps {
		if ok, err := conn.HasTable(sw.Table); err != nil || !ok {
			continue
		}
		var n int64
		if yes {
			n, err = conn.Exec("DELETE FROM " + sw.Table + " WHERE " + sw.Where)
			if err != nil {
				return res, err
			}
		} else {
			if err := conn.SQL.QueryRow(
				"SELECT COUNT(*) FROM " + sw.Table + " WHERE " + sw.Where).Scan(&n); err != nil {
				continue
			}
		}
		if n > 0 {
			res.Removed[sw.Label] = n
			total += n
		}
	}
	res.DryRun = !yes
	if total == 0 {
		res.Notes = append(res.Notes, "no orphan rows found")
		res.Verified = true
		return res, ErrNothingToDo
	}
	if !yes {
		res.Notes = append(res.Notes, "would delete "+strconv.FormatInt(total, 10)+" rows")
		return res, nil
	}
	res.Steps = append(res.Steps, "deleted "+strconv.FormatInt(total, 10)+" orphan rows")
	verdict, err := conn.Integrity()
	if err != nil {
		return res, err
	}
	res.Verified = verdict == "ok"
	return res, nil
}

// Clean executes the requested non-database cleanups.
func Clean(p paths.Paths, o CleanOptions) (Result, error) {
	res := Result{Action: "clean", Removed: map[string]int64{}}
	start := time.Now()
	res.DryRun = !o.Yes

	if o.Log {
		size := statSize(p.LogFile)
		if size > 0 {
			if o.Yes {
				var err error
				var freed int64
				if o.KeepLog > 0 {
					freed, err = CleanLogTo(p.LogFile, o.KeepLog)
				} else {
					freed, err = CleanLog(p.LogFile)
				}
				if err != nil {
					res.Notes = append(res.Notes, "log: "+err.Error())
				} else {
					res.Removed["log"] = freed
				}
			} else {
				res.Removed["log"] = size
			}
		}
	}

	if o.ToolOutput {
		if o.Yes {
			freed, n, err := PruneToolOutput(p.ToolOutput, o.ToolOutAge)
			if err != nil {
				res.Notes = append(res.Notes, "tool-output: "+err.Error())
			} else {
				res.Removed["tool-output"] = freed
				res.Steps = append(res.Steps, "removed "+strconv.Itoa(n)+" tool outputs")
			}
		} else {
			dirSum := dirSize(p.ToolOutput)
			res.Removed["tool-output"] = dirSum
		}
	}

	if o.Snapshots {
		keep := o.SnapshotKeep
		if keep == 0 {
			keep = 5
		}
		if o.Yes {
			freed, n, err := PruneSnapshots(p.Snapshot, keep, o.SnapshotAge)
			if err != nil {
				res.Notes = append(res.Notes, "snapshots: "+err.Error())
			} else {
				res.Removed["snapshots"] = freed
				res.Steps = append(res.Steps, "removed "+strconv.Itoa(n)+" snapshots")
			}
		} else {
			_, n, err := PruneSnapshots(p.Snapshot, keep, o.SnapshotAge)
			if err == nil {
				res.Steps = append(res.Steps, "would remove "+strconv.Itoa(n)+" snapshots")
			}
		}
	}

	for _, v := range res.Removed {
		res.Reclaimed += v
	}
	// Nothing in this action has a natural before/after, so express it as the
	// bytes removed from the directory.
	res.Before = res.Reclaimed
	res.After = 0
	res.Elapsed = time.Since(start)
	if res.Reclaimed == 0 {
		res.Notes = append(res.Notes, "nothing to clean")
		return res, ErrNothingToDo
	}
	res.Verified = true
	return res, nil
}

func dirSize(p string) int64 {
	var total int64
	_ = filepath.WalkDir(p, func(_ string, d os.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr
		}
		if d.IsDir() {
			return nil
		}
		if fi, err := d.Info(); err == nil {
			total += fi.Size()
		}
		return nil
	})
	return total
}

// ToolOutputPrunable reports how much of the spilled tool output is older than
// age, and how many files that is, without deleting anything. The plan needs
// the number before the user agrees to it.
func ToolOutputPrunable(dir string, age time.Duration) (int64, int) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return 0, 0
	}
	cutoff := time.Now().Add(-age)
	var bytes int64
	var n int
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if age > 0 && info.ModTime().After(cutoff) {
			continue
		}
		bytes += info.Size()
		n++
	}
	return bytes, n
}
