package daemon

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/jourdanhaines/banshee/internal/ipc"
)

// TestOpsOverSocket wires the real control socket to the real op dispatcher
// with a fake UI in place of GTK, covering the path a `banshee toggle` takes
// end to end minus the main-loop hop.
func TestOpsOverSocket(t *testing.T) {
	dir, err := os.MkdirTemp("", "bansheedaemon")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	ui := &fakeUI{}
	var mu sync.Mutex
	quits := 0

	sock := filepath.Join(dir, "b.sock")
	srv, err := ipc.Listen(sock, func(req ipc.Request) ipc.Response {
		// Stands in for the glib.IdleAdd hop: serialize onto one "main loop".
		mu.Lock()
		defer mu.Unlock()
		return handleOp(ui, req, nil, func() { quits++ })
	})
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer srv.Close()

	steps := []struct {
		op          string
		query       string
		wantVisible bool
	}{
		{op: ipc.OpPing, wantVisible: false},
		{op: ipc.OpToggle, query: "blacksheep", wantVisible: true},
		{op: ipc.OpPing, wantVisible: true},
		{op: ipc.OpToggle, wantVisible: false},
		{op: ipc.OpShow, query: "x", wantVisible: true},
		{op: ipc.OpHide, wantVisible: false},
		{op: ipc.OpReload, wantVisible: false},
		{op: ipc.OpQuit, wantVisible: false},
	}

	for _, step := range steps {
		resp, err := ipc.SendTo(sock, ipc.Request{Op: step.op, Query: step.query})
		if err != nil {
			t.Fatalf("%s: %v", step.op, err)
		}
		if !resp.OK {
			t.Fatalf("%s: ok=false (%s)", step.op, resp.Error)
		}
		if step.op == ipc.OpPing && resp.Visible != step.wantVisible {
			t.Errorf("ping after %s: visible = %v, want %v", step.op, resp.Visible, step.wantVisible)
		}
		mu.Lock()
		got := ui.Visible()
		mu.Unlock()
		if got != step.wantVisible {
			t.Errorf("after %s: visible = %v, want %v", step.op, got, step.wantVisible)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if quits != 1 {
		t.Errorf("quit callback ran %d times, want 1", quits)
	}
	if ui.query != "x" {
		t.Errorf("last query = %q, want %q", ui.query, "x")
	}
}
