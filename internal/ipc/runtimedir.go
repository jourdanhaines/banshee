package ipc

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// VerifyRuntimeDir refuses to use a runtime directory another user could write
// to.
//
// RuntimeDir falls back to os.TempDir() when $XDG_RUNTIME_DIR is unset, which
// makes the socket and the lock live in a world-writable parent. os.MkdirAll
// happily succeeds on a /tmp/banshee that somebody else created first, and
// nothing downstream would notice: Listen unlinks whatever socket it finds
// there, and a client's readiness ping cannot tell a hijacked socket from the
// real daemon. So the directory is checked instead of trusted — it must be a
// real directory (not a symlink to one), owned by this uid, and not writable
// by group or world.
//
// A directory this process could plausibly have created too loosely (a umask
// of 0) is tightened in place rather than rejected; only a directory that is
// not ours, or that stays group/world-writable, is an error.
func VerifyRuntimeDir(dir string) error {
	fi, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("ipc: stat runtime dir %s: %w", dir, err)
	}
	if !fi.IsDir() {
		return fmt.Errorf("ipc: runtime dir %s is not a directory", dir)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return nil // unknown platform: nothing to check against
	}
	if int(st.Uid) != os.Getuid() {
		return fmt.Errorf("ipc: runtime dir %s is owned by uid %d, not %d — refusing to use it",
			dir, st.Uid, os.Getuid())
	}
	if fi.Mode().Perm()&0o022 != 0 {
		if err := os.Chmod(dir, 0o700); err != nil {
			return fmt.Errorf("ipc: runtime dir %s is group/world writable: %w", dir, err)
		}
		fi, err = os.Lstat(dir)
		if err != nil || fi.Mode().Perm()&0o022 != 0 {
			return fmt.Errorf("ipc: runtime dir %s is group/world writable", dir)
		}
	}
	return nil
}

// verifyParent runs VerifyRuntimeDir on the directory holding path.
func verifyParent(path string) error { return VerifyRuntimeDir(filepath.Dir(path)) }
