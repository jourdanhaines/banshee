package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestAcquireLockContention(t *testing.T) {
	path := filepath.Join(t.TempDir(), "banshee.lock")

	first, err := AcquireLock(path)
	if err != nil {
		t.Fatalf("first AcquireLock: %v", err)
	}

	if _, err := AcquireLock(path); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("second AcquireLock error = %v, want ErrAlreadyRunning", err)
	}

	if err := first.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}

	third, err := AcquireLock(path)
	if err != nil {
		t.Fatalf("AcquireLock after Release: %v", err)
	}
	if err := third.Release(); err != nil {
		t.Fatalf("Release third: %v", err)
	}
	if err := third.Release(); err != nil {
		t.Fatalf("second Release should be a no-op, got %v", err)
	}
}

func TestAcquireLockCreatesDirAndRecordsPID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "run", "banshee.lock")

	lock, err := AcquireLock(path)
	if err != nil {
		t.Fatalf("AcquireLock: %v", err)
	}
	defer lock.Release()

	if lock.Path() != path {
		t.Errorf("Path() = %q, want %q", lock.Path(), path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read lock file: %v", err)
	}
	if got, want := strings.TrimSpace(string(data)), strconv.Itoa(os.Getpid()); got != want {
		t.Errorf("lock file contains %q, want pid %q", got, want)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("lock file mode = %v, want 0600", perm)
	}
}

func TestAcquireLockErrors(t *testing.T) {
	tests := []struct {
		name string
		path string
	}{
		{name: "empty path", path: ""},
		{name: "unwritable parent", path: filepath.Join(t.TempDir(), "file", "banshee.lock")},
	}
	// Make the "file" component a regular file so MkdirAll fails.
	if err := os.WriteFile(filepath.Join(filepath.Dir(filepath.Dir(tests[1].path)), "file"), nil, 0o600); err != nil {
		t.Fatalf("setup: %v", err)
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lock, err := AcquireLock(tt.path)
			if err == nil {
				lock.Release()
				t.Fatalf("AcquireLock(%q) succeeded, want error", tt.path)
			}
			if errors.Is(err, ErrAlreadyRunning) {
				t.Fatalf("AcquireLock(%q) = ErrAlreadyRunning, want a plain failure", tt.path)
			}
		})
	}
}

func TestNilLockRelease(t *testing.T) {
	var l *Lock
	if err := l.Release(); err != nil {
		t.Fatalf("nil Lock Release: %v", err)
	}
}
