package actions

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// Lock is an advisory lock over a data directory. Two ocsa processes vacuuming
// the same database would serialise on SQLite's write lock anyway, but the
// second would sit in a five-minute busy_timeout wondering why nothing was
// happening. This turns that into an immediate, explanatory refusal.
type Lock struct {
	file *os.File
	path string
}

// LockFileName is the advisory lock's name inside the data directory.
const LockFileName = "ocsa.lock"

// AcquireLock takes the lock without blocking. A returned error means another
// ocsa is holding it.
func AcquireLock(dataDir string) (*Lock, error) {
	path := filepath.Join(dataDir, LockFileName)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if err == syscall.EWOULDBLOCK {
			return nil, fmt.Errorf("another ocsa is working in %s (lock: %s)", dataDir, path)
		}
		return nil, err
	}
	// Record who holds it, for the next process that gets refused.
	f.Truncate(0)
	_, _ = f.WriteAt([]byte(fmt.Sprintf("%d\n", os.Getpid())), 0)
	return &Lock{file: f, path: path}, nil
}

// Release drops the lock. Safe to call twice.
func (l *Lock) Release() error {
	if l == nil || l.file == nil {
		return nil
	}
	err := syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	closeErr := l.file.Close()
	l.file = nil
	if err != nil {
		return err
	}
	return closeErr
}

// LockHolder reports the pid recorded in the lock file, for messages. It
// returns "" when the file is absent, unreadable, or names a process that no
// longer exists: a lock file outlives the process that wrote it whenever ocsa is
// killed, and reporting that as "another ocsa is working" would send the reader
// hunting for something that is not there. flock itself is released by the
// kernel on exit, so a stale file never actually blocks AcquireLock.
func LockHolder(dataDir string) string {
	b, err := os.ReadFile(filepath.Join(dataDir, LockFileName))
	if err != nil {
		return ""
	}
	s := strings.TrimSpace(string(b))
	if s == "" {
		return ""
	}
	pid, err := strconv.Atoi(s)
	if err != nil {
		return ""
	}
	if err := syscall.Kill(pid, 0); err != nil {
		return "" // gone, so the lock is stale
	}
	return s
}
