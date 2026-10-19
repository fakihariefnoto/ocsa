package actions

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// TestLockHolderIgnoresAStaleFile: a lock file outlives the process whenever
// ocsa is killed. Reporting that as "another ocsa is working" sends the reader
// looking for something that is not there, and the lock itself is already
// released by the kernel.
func TestLockHolderIgnoresAStaleFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, LockFileName)

	// A pid that cannot be running: the process was reaped long ago.
	const deadPID = 999999
	if err := os.WriteFile(path, []byte(strconv.Itoa(deadPID)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := LockHolder(dir); got != "" {
		t.Errorf("stale lock reported as held by pid %q", got)
	}

	// And a lock file that names this very process is genuinely held.
	if err := os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := LockHolder(dir); got == "" {
		t.Error("a live holder was not reported")
	}

	// Missing, empty and malformed files are all "not held", not errors.
	for _, content := range []string{"", "not-a-pid\n", "  \n"} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := LockHolder(dir); got != "" {
			t.Errorf("content %q reported as held by %q", content, got)
		}
	}
	if got := LockHolder(t.TempDir()); got != "" {
		t.Errorf("absent lock file reported as held by %q", got)
	}
}

// TestStaleLockDoesNotBlockAcquisition: the real property that matters. flock
// is released on process exit, so a leftover file must not refuse a new run.
func TestStaleLockDoesNotBlockAcquisition(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, LockFileName)
	if err := os.WriteFile(path, []byte("999999\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	lock, err := AcquireLock(dir)
	if err != nil {
		t.Fatalf("a stale lock file blocked acquisition: %v", err)
	}
	defer lock.Release()

	// Acquiring rewrites the holder, so the next reader sees the truth.
	if got := LockHolder(dir); got != strconv.Itoa(os.Getpid()) {
		t.Errorf("lock names pid %q, want this process %d", got, os.Getpid())
	}
}
