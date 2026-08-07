package daemon

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
)

// ErrAlreadyRunning reports that another banshee daemon holds the
// single-instance lock. Callers surface it as "daemon already running" and exit
// without touching the socket.
var ErrAlreadyRunning = errors.New("daemon already running")

// Lock is the advisory file lock that makes the daemon single-instance. It is
// held for the whole lifetime of the process: the kernel drops it when the file
// descriptor closes, so a crashed daemon never leaves a lock behind.
type Lock struct {
	f    *os.File
	path string
}

// AcquireLock takes an exclusive, non-blocking flock on path, creating the file
// and its parent directory if needed. It returns ErrAlreadyRunning when another
// process (or another open file descriptor in this one) already holds the lock.
//
// The winner's pid is written into the file for humans reading it; that write
// is best effort and never fails the acquisition.
func AcquireLock(path string) (*Lock, error) {
	if path == "" {
		return nil, errors.New("daemon: AcquireLock requires a path")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("daemon: create lock dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("daemon: open lock %s: %w", path, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, ErrAlreadyRunning
		}
		return nil, fmt.Errorf("daemon: flock %s: %w", path, err)
	}
	if err := f.Truncate(0); err == nil {
		_, _ = f.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0)
		_ = f.Sync()
	}
	return &Lock{f: f, path: path}, nil
}

// Path returns the lock file path.
func (l *Lock) Path() string { return l.path }

// Release unlocks and closes the lock file. The file itself is left in place —
// unlinking it would race another daemon that has already opened it. Release is
// safe to call more than once.
func (l *Lock) Release() error {
	if l == nil || l.f == nil {
		return nil
	}
	f := l.f
	l.f = nil
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return f.Close()
}
