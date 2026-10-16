package db

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Holder is a process that currently has the database file open.
type Holder struct {
	PID     int
	Command string
}

// String renders a holder for a message.
func (h Holder) String() string {
	if h.Command == "" {
		return strconv.Itoa(h.PID)
	}
	return h.Command + " (pid " + strconv.Itoa(h.PID) + ")"
}

// Activity is what is happening to the database right now.
type Activity struct {
	Open          bool // any process holds the file open
	Holders       []Holder
	WALBytes      int64
	WriteLockHeld bool // a connection is inside a write transaction
	DataVersion   int64
}

// Summary is a short human description of who is using the database.
func (a Activity) Summary() string {
	if !a.Open {
		return "nobody has it open"
	}
	names := make([]string, 0, len(a.Holders))
	for _, h := range a.Holders {
		names = append(names, h.String())
	}
	if len(names) == 0 {
		return "open by an unidentified process"
	}
	return strings.Join(names, ", ")
}

// Holders lists the processes with path open. A client with the file open is
// harmless; a client mid-write is not, which is what WriteLockHeld answers.
func Holders(path string) []Holder {
	out, err := exec.Command("lsof", "-nP", "-F", "pc", path).Output()
	if err != nil {
		return nil
	}
	var holders []Holder
	var cur *Holder
	for _, line := range strings.Split(string(out), "\n") {
		if line == "" {
			continue
		}
		switch line[0] {
		case 'p':
			if pid, err := strconv.Atoi(line[1:]); err == nil {
				holders = append(holders, Holder{PID: pid})
				cur = &holders[len(holders)-1]
			}
		case 'c':
			if cur != nil {
				cur.Command = line[1:]
			}
		}
	}
	return holders
}

// WriteLockHeld probes whether a connection currently holds the write lock. The
// probe uses a short timeout so it answers quickly against a busy database
// rather than queueing behind the writer, and it rolls back so it changes
// nothing. This is SQLite's own answer to "is someone writing right now",
// which no amount of scanning the process table can tell you.
//
// The probe runs on a pinned connection: a pooled one can be recycled between
// the BEGIN and the ROLLBACK, and database/sql may reset the session on reuse,
// which would silently drop the transaction.
func WriteLockHeld(path string) (bool, error) {
	handle, err := openTimeout(path, 250*time.Millisecond)
	if err != nil {
		return false, err
	}
	defer handle.Close()

	ctx := context.Background()
	conn, err := handle.Conn(ctx)
	if err != nil {
		return false, err
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		if isBusy(err) {
			return true, nil
		}
		return false, err
	}
	_, _ = conn.ExecContext(ctx, "ROLLBACK")
	return false, nil
}

// Probe collects the current activity.
func Probe(path string) (Activity, error) {
	a := Activity{}
	fi, err := os.Stat(path)
	a.Open = err == nil && fi != nil
	a.Holders = Holders(path)
	if walInfo, err := os.Stat(path + "-wal"); err == nil {
		a.WALBytes = walInfo.Size()
	}
	held, err := WriteLockHeld(path)
	if err != nil {
		return a, err
	}
	a.WriteLockHeld = held
	if v, err := dataVersion(path); err == nil {
		a.DataVersion = v
	}
	return a, nil
}

func dataVersion(path string) (int64, error) {
	conn, err := openTimeout(path, 250*time.Millisecond)
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	var v int64
	if err := conn.QueryRow("PRAGMA data_version").Scan(&v); err != nil {
		return 0, err
	}
	return v, nil
}

// HoldWriteLock takes the write lock on a pinned connection and returns a
// function that releases it. Tests use it to stand in for a busy agent turn.
func HoldWriteLock(path string) (release func(), err error) {
	handle, err := openTimeout(path, 250*time.Millisecond)
	if err != nil {
		return nil, err
	}
	ctx := context.Background()
	conn, err := handle.Conn(ctx)
	if err != nil {
		handle.Close()
		return nil, err
	}
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		conn.Close()
		handle.Close()
		return nil, err
	}
	return func() {
		_, _ = conn.ExecContext(ctx, "ROLLBACK")
		conn.Close()
		handle.Close()
	}, nil
}

// WaitForQuiet blocks until no connection holds the write lock for quietFor
// continuously, or until deadline passes. Returns whether it went quiet, plus
// the last observation. Callers use it to take the vacuum off the critical path
// of a running agent turn instead of either barging in or refusing outright.
//
// Only the write-lock probe runs on each tick: it is a single quick query. The
// process table (lsof) is read once, for reporting, because forking it several
// times a second would make the loop too slow to notice a short gap.
func WaitForQuiet(path string, deadline time.Duration, quietFor time.Duration, tick func(Activity)) (bool, Activity, error) {
	end := time.Now().Add(deadline)

	last, err := Probe(path)
	if err != nil {
		return false, last, err
	}
	if tick != nil {
		tick(last)
	}

	var quietSince time.Time
	for {
		held, err := WriteLockHeld(path)
		if err != nil {
			return false, last, err
		}
		last.WriteLockHeld = held
		if held {
			quietSince = time.Time{}
		} else {
			if quietSince.IsZero() {
				quietSince = time.Now()
			}
			if time.Since(quietSince) >= quietFor {
				return true, last, nil
			}
		}
		if time.Now().After(end) {
			return false, last, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func isBusy(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "database is locked") ||
		strings.Contains(s, "database table is locked") ||
		strings.Contains(s, "sqlite_busy")
}

// openTimeout opens with a chosen busy timeout. Used by the probes, which must
// answer quickly, as opposed to actions, which are happy to wait.
func openTimeout(path string, timeout time.Duration) (*sql.DB, error) {
	handle, err := open(path, "rw")
	if err != nil {
		return nil, err
	}
	if _, err := handle.SQL.Exec(fmt.Sprintf("PRAGMA busy_timeout=%d", timeout.Milliseconds())); err != nil {
		handle.Close()
		return nil, err
	}
	return handle.SQL, nil
}
