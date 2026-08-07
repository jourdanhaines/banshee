//go:build gtksmoke

// Manual end-to-end check of the daemon lifecycle against a real GTK main loop.
// It needs a Wayland/X display, so it is excluded from the normal test run:
//
//	go test -tags gtksmoke -run Smoke ./internal/daemon
package daemon

import (
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/diamondburned/gotk4/pkg/gtk/v4"

	"github.com/jourdanhaines/banshee/internal/ipc"
)

func TestDaemonSmoke(t *testing.T) {
	dir, err := os.MkdirTemp("", "bansheesmoke")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	defer os.RemoveAll(dir)

	sock := filepath.Join(dir, "b.sock")
	lockPath := filepath.Join(dir, "b.lock")
	ui := &fakeUI{}

	done := make(chan error, 1)
	go func() {
		// GTK wants its main loop pinned to one OS thread.
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		done <- Run(Options{
			NewUI:      func(*gtk.Application) UI { return ui },
			AppID:      "dev.jourdan.banshee.smoke",
			SocketPath: sock,
			LockPath:   lockPath,
			Logger:     log.New(io.Discard, "", 0),
		})
	}()

	// The UI only exists once GTK activates, which is exactly what ping reports.
	if err := EnsureDaemon(EnsureOptions{
		Ping: func() (ipc.Response, error) { return ipc.SendTo(sock, ipc.Request{Op: ipc.OpPing}) },
		// The goroutine above is the daemon; "spawning" is a no-op here, so the
		// first probe (which may beat Listen) simply falls through to polling.
		Spawn:    func() error { return nil },
		Timeout:  5 * time.Second,
		Interval: 25 * time.Millisecond,
	}); err != nil {
		t.Fatalf("daemon never became ready: %v", err)
	}

	// A second daemon must lose the flock race.
	if _, err := AcquireLock(lockPath); !errors.Is(err, ErrAlreadyRunning) {
		t.Errorf("second AcquireLock = %v, want ErrAlreadyRunning", err)
	}

	for _, step := range []struct {
		op          string
		wantVisible bool
	}{
		{op: ipc.OpToggle, wantVisible: true},
		{op: ipc.OpToggle, wantVisible: false},
		{op: ipc.OpShow, wantVisible: true},
		{op: ipc.OpHide, wantVisible: false},
		{op: ipc.OpReload, wantVisible: false},
	} {
		if _, err := ipc.SendTo(sock, ipc.Request{Op: step.op}); err != nil {
			t.Fatalf("%s: %v", step.op, err)
		}
		resp, err := ipc.SendTo(sock, ipc.Request{Op: ipc.OpPing})
		if err != nil {
			t.Fatalf("ping after %s: %v", step.op, err)
		}
		if resp.Visible != step.wantVisible {
			t.Errorf("after %s: visible = %v, want %v", step.op, resp.Visible, step.wantVisible)
		}
	}

	if _, err := ipc.SendTo(sock, ipc.Request{Op: ipc.OpQuit}); err != nil {
		t.Fatalf("quit: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("daemon did not exit after quit")
	}
	if _, err := os.Stat(sock); err == nil {
		t.Errorf("socket %s survived shutdown", sock)
	}
	if lock, err := AcquireLock(lockPath); err != nil {
		t.Errorf("lock not released after shutdown: %v", err)
	} else {
		lock.Release()
	}
}
