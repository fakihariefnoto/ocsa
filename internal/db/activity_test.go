package db

import (
	"os"
	"testing"
	"time"
)

func activityFixture(t *testing.T) *DB {
	t.Helper()
	path := t.TempDir() + "/opencode.db"
	conn, err := Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.SQL.Exec("CREATE TABLE t(x)"); err != nil {
		t.Fatal(err)
	}
	return conn
}

// TestWriteLockHeldIsTheRealSignal: the process table cannot tell you whether
// an agent turn is mid-write, but SQLite can. This is what the claim uses to
// decide between waiting and barging in.
func TestWriteLockHeldIsTheRealSignal(t *testing.T) {
	idle := activityFixture(t)
	defer idle.Close()

	held, err := WriteLockHeld(idle.Path)
	if err != nil {
		t.Fatalf("probe on an idle database: %v", err)
	}
	if held {
		t.Error("reported a write lock on an idle database")
	}

	release, err := HoldWriteLock(idle.Path)
	if err != nil {
		t.Fatal(err)
	}

	held, err = WriteLockHeld(idle.Path)
	if err != nil {
		t.Fatalf("probe while a writer holds the lock: %v", err)
	}
	if !held {
		t.Error("did not notice a held write lock")
	}

	release()
	held, err = WriteLockHeld(idle.Path)
	if err != nil {
		t.Fatal(err)
	}
	if held {
		t.Error("still reported a write lock after the writer rolled back")
	}
}

// TestProbeMustNotBlockOnADatabaseWithRealBloat: the probe is meant to answer
// quickly so the UI can report what is happening; it must not queue behind a
// five-minute busy timeout.
func TestProbeIsFast(t *testing.T) {
	conn := activityFixture(t)
	defer conn.Close()
	release, err := HoldWriteLock(conn.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	start := time.Now()
	if _, err := Probe(conn.Path); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("probe took %s while a writer held the lock; it should give up quickly", elapsed)
	}
}

func TestProbeReportsAnOpenFile(t *testing.T) {
	conn := activityFixture(t)
	defer conn.Close()

	a, err := Probe(conn.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !a.Open {
		t.Error("did not notice the database file exists")
	}
	// lsof may be unavailable or restricted; the summary must still be sane.
	if a.Summary() == "" {
		t.Error("empty summary")
	}

	if _, err := os.Stat(conn.Path + "-missing"); err == nil {
		t.Skip("unexpected")
	}
}

func TestWaitForQuietGivesUp(t *testing.T) {
	conn := activityFixture(t)
	defer conn.Close()
	release, err := HoldWriteLock(conn.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	quiet, _, err := WaitForQuiet(conn.Path, 400*time.Millisecond, 200*time.Millisecond, nil)
	if err != nil {
		t.Fatal(err)
	}
	if quiet {
		t.Error("claimed the database went quiet while a writer held the lock")
	}
}
