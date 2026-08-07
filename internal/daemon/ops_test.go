package daemon

import (
	"errors"
	"strings"
	"testing"

	"github.com/jourdanhaines/banshee/internal/config"
	"github.com/jourdanhaines/banshee/internal/ipc"
)

// fakeUI records the calls handleOp makes, standing in for internal/ui so the
// daemon's protocol logic is testable without a display.
type fakeUI struct {
	visible bool
	calls   []string
	query   string
}

func (u *fakeUI) Show(query string) {
	u.visible = true
	u.query = query
	u.calls = append(u.calls, "show")
}

func (u *fakeUI) Hide() {
	u.visible = false
	u.calls = append(u.calls, "hide")
}

func (u *fakeUI) Visible() bool { return u.visible }

func (u *fakeUI) Reload() { u.calls = append(u.calls, "reload") }

func TestHandleOp(t *testing.T) {
	reloadErr := errors.New("index rebuild failed")

	tests := []struct {
		name        string
		startHidden bool
		nilUI       bool
		onReload    func() error
		req         ipc.Request
		wantOK      bool
		wantErrSub  string
		wantCalls   []string
		wantVisible bool
		wantQuery   string
		wantQuit    bool
	}{
		{
			name:        "toggle from hidden shows with query",
			startHidden: true,
			req:         ipc.Request{Op: ipc.OpToggle, Query: "blacksh"},
			wantOK:      true,
			wantCalls:   []string{"show"},
			wantVisible: true,
			wantQuery:   "blacksh",
		},
		{
			name:      "toggle from visible hides",
			req:       ipc.Request{Op: ipc.OpToggle},
			wantOK:    true,
			wantCalls: []string{"hide"},
		},
		{
			name:        "show is idempotent",
			req:         ipc.Request{Op: ipc.OpShow, Query: "q"},
			wantOK:      true,
			wantCalls:   []string{"show"},
			wantVisible: true,
			wantQuery:   "q",
		},
		{
			name:      "hide",
			req:       ipc.Request{Op: ipc.OpHide},
			wantOK:    true,
			wantCalls: []string{"hide"},
		},
		{
			name:        "reload runs hook then ui",
			startHidden: true,
			onReload:    func() error { return nil },
			req:         ipc.Request{Op: ipc.OpReload},
			wantOK:      true,
			wantCalls:   []string{"reload"},
		},
		{
			name:        "reload reports hook failure but still refreshes ui",
			startHidden: true,
			onReload:    func() error { return reloadErr },
			req:         ipc.Request{Op: ipc.OpReload},
			wantErrSub:  reloadErr.Error(),
			wantCalls:   []string{"reload"},
		},
		{
			name:        "reload without hook",
			startHidden: true,
			req:         ipc.Request{Op: ipc.OpReload},
			wantOK:      true,
			wantCalls:   []string{"reload"},
		},
		{
			name:        "ping reports visibility",
			req:         ipc.Request{Op: ipc.OpPing},
			wantOK:      true,
			wantVisible: true,
		},
		{
			name:        "ping is not ready before the ui exists",
			nilUI:       true,
			req:         ipc.Request{Op: ipc.OpPing},
			wantErrSub:  "not ready",
			startHidden: true,
		},
		{
			name:        "quit",
			startHidden: true,
			req:         ipc.Request{Op: ipc.OpQuit},
			wantOK:      true,
			wantQuit:    true,
		},
		{
			name:        "quit works before the ui exists",
			nilUI:       true,
			req:         ipc.Request{Op: ipc.OpQuit},
			wantOK:      true,
			wantQuit:    true,
			startHidden: true,
		},
		{
			name:        "unknown op",
			startHidden: true,
			req:         ipc.Request{Op: "dance"},
			wantErrSub:  `unknown op "dance"`,
		},
		{
			name:        "toggle before the ui exists",
			nilUI:       true,
			req:         ipc.Request{Op: ipc.OpToggle},
			wantErrSub:  "not ready",
			startHidden: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ui := &fakeUI{visible: !tt.startHidden}
			var arg UI = ui
			if tt.nilUI {
				arg = nil
			}
			quit := false

			resp := handleOp(arg, tt.req, tt.onReload, func() { quit = true })

			if resp.OK != tt.wantOK {
				t.Errorf("ok = %v (error %q), want %v", resp.OK, resp.Error, tt.wantOK)
			}
			if tt.wantErrSub != "" && !strings.Contains(resp.Error, tt.wantErrSub) {
				t.Errorf("error = %q, want it to contain %q", resp.Error, tt.wantErrSub)
			}
			if tt.wantErrSub == "" && resp.Error != "" {
				t.Errorf("unexpected error %q", resp.Error)
			}
			if got := strings.Join(ui.calls, ","); got != strings.Join(tt.wantCalls, ",") {
				t.Errorf("ui calls = [%s], want [%s]", got, strings.Join(tt.wantCalls, ","))
			}
			if !tt.nilUI && ui.visible != tt.wantVisible {
				t.Errorf("visible = %v, want %v", ui.visible, tt.wantVisible)
			}
			if ui.query != tt.wantQuery {
				t.Errorf("query = %q, want %q", ui.query, tt.wantQuery)
			}
			if quit != tt.wantQuit {
				t.Errorf("quit called = %v, want %v", quit, tt.wantQuit)
			}
			if tt.req.Op == ipc.OpPing {
				if resp.Version != config.Version {
					t.Errorf("ping version = %q, want %q", resp.Version, config.Version)
				}
				if resp.Visible != tt.wantVisible {
					t.Errorf("ping visible = %v, want %v", resp.Visible, tt.wantVisible)
				}
			}
		})
	}
}

// TestHandleOpNilQuit guards the optional-callback contract.
func TestHandleOpNilQuit(t *testing.T) {
	resp := handleOp(&fakeUI{}, ipc.Request{Op: ipc.OpQuit}, nil, nil)
	if !resp.OK {
		t.Fatalf("quit with nil callback: %+v", resp)
	}
}
