package report

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/fakih/ocsa/internal/db"
	"github.com/fakih/ocsa/internal/paths"
	"github.com/fakih/ocsa/internal/storage"
)

// Status answers one question: is it safe to rewrite this database right now,
// and if not, what is in the way. It is read-only and cheap enough to run
// before any action, and it is what a reader checks when a claim refuses.
func Status(w io.Writer, p paths.Paths, s *storage.Scan, a db.Activity, lockHolder string) {
	comp := s.Composition()

	fmt.Fprintf(w, "%s\n", p.Data)
	fmt.Fprintf(w, "%-16s %s\n", "database", paths.ByteSize(comp.Total)+" on disk, "+
		paths.ByteSize(comp.Live)+" in use")
	if comp.Frozen > 0 {
		fmt.Fprintf(w, "%-16s %s\n", "", paths.ByteSize(comp.Frozen)+
			" frozen (a vacuum cannot free it)")
	}
	// Reclaimable is the freelist, not "everything that is not live": the
	// indexes and key/value state are in use and a vacuum will not free them.
	fmt.Fprintf(w, "%-16s %s\n", "reclaimable", paths.ByteSize(comp.Free))

	fmt.Fprintln(w, "\nWHO HAS IT OPEN")
	switch {
	case !a.Open:
		fmt.Fprintln(w, "  nobody")
	case len(a.Holders) == 0:
		fmt.Fprintln(w, "  an unidentified process")
	default:
		for _, h := range a.Holders {
			fmt.Fprintf(w, "  %s\n", h.String())
		}
	}

	fmt.Fprintln(w, "\nRIGHT NOW")
	switch {
	case a.WriteLockHeld:
		fmt.Fprintln(w, "  writing: a claim would wait for a gap, then proceed")
		fmt.Fprintln(w, "  if the writes never stop it exits 3 and changes nothing")
	case a.Open:
		fmt.Fprintln(w, "  idle: open but not writing, so a rewrite will not disturb it")
	default:
		fmt.Fprintln(w, "  free: nothing holds it, a claim can start immediately")
	}

	// The one thing that blocks a claim outright is another ocsa already at work.
	if lockHolder != "" {
		fmt.Fprintf(w, "\nHELD BY ANOTHER OCSA\n  pid %s\n", lockHolder)
		fmt.Fprintln(w, "  wait for it to finish, or delete the lock file if it is stale")
	}

	if a.WALBytes > 0 {
		fmt.Fprintf(w, "\nwrite-ahead log  %s\n", paths.ByteSize(a.WALBytes))
	}
	if a.Open && a.WriteLockHeld {
		fmt.Fprintf(w, "\nOpenCode is running; ocsa never stops it. To stop it yourself:\n")
		fmt.Fprintf(w, "  opencode service stop\n")
	}
}

// StatusJSON is the machine-readable form, for scripts that gate on safety.
type StatusJSON struct {
	DataDir     string            `json:"data_dir"`
	Database    string            `json:"database"`
	Bytes       int64             `json:"bytes"`
	LiveBytes   int64             `json:"live_bytes"`
	FrozenBytes int64             `json:"frozen_bytes"`
	Reclaimable int64             `json:"reclaimable_bytes"` // free pages only
	Open        bool              `json:"open"`
	Writing     bool              `json:"writing"`
	SafeToRun   bool              `json:"safe_to_run"`
	Holders     []string          `json:"holders,omitempty"`
	WALBytes    int64             `json:"wal_bytes"`
	LockHolder  string            `json:"lock_holder,omitempty"`
	Checked     time.Time         `json:"checked"`
	Extra       map[string]string `json:"-"`
}

// BuildStatus assembles the JSON view.
func BuildStatus(p paths.Paths, s *storage.Scan, a db.Activity, lockHolder string) StatusJSON {
	comp := s.Composition()
	st := StatusJSON{
		DataDir:     p.Data,
		Database:    p.DB,
		Bytes:       comp.Total,
		LiveBytes:   comp.Live,
		FrozenBytes: comp.Frozen,
		Reclaimable: comp.Free,
		Open:        a.Open,
		Writing:     a.WriteLockHeld,
		SafeToRun:   !a.WriteLockHeld && lockHolder == "",
		WALBytes:    a.WALBytes,
		LockHolder:  lockHolder,
		Checked:     time.Now(),
	}
	for _, h := range a.Holders {
		st.Holders = append(st.Holders, h.String())
	}
	return st
}

// Summary is a one-line verdict, for scripts that only want the answer.
func (s StatusJSON) Summary() string {
	switch {
	case s.LockHolder != "":
		return "busy: another ocsa holds the lock (pid " + s.LockHolder + ")"
	case s.Writing:
		return "writing: a claim would wait for a gap"
	case s.Open:
		return "idle: safe to run now"
	default:
		return "free: safe to run now"
	}
}

// String keeps the JSON type printable.
func (s StatusJSON) String() string { return strings.TrimSpace(s.Summary()) }
