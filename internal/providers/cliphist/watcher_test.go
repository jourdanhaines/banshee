package cliphist

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestClassify(t *testing.T) {
	tests := []struct {
		name      string
		types     []string
		wantKind  Kind
		wantFetch string
		wantMIME  string
		wantHint  bool
		wantOK    bool
	}{
		{
			name:      "plain text",
			types:     []string{"text/plain;charset=utf-8", "text/plain", "UTF8_STRING"},
			wantKind:  KindText,
			wantFetch: "text",
			wantMIME:  "text/plain",
			wantOK:    true,
		},
		{
			name:      "uri-list beats the file manager's thumbnail and text offers",
			types:     []string{"text/uri-list", "image/png", "text/plain"},
			wantKind:  KindFiles,
			wantFetch: "text/uri-list",
			wantMIME:  "text/uri-list",
			wantOK:    true,
		},
		{
			name:      "png beats the browser's text fallback",
			types:     []string{"image/png", "text/html", "text/plain"},
			wantKind:  KindImage,
			wantFetch: "image/png",
			wantMIME:  "image/png",
			wantOK:    true,
		},
		{
			name:      "jpeg accepted when no png",
			types:     []string{"image/jpeg"},
			wantKind:  KindImage,
			wantFetch: "image/jpeg",
			wantMIME:  "image/jpeg",
			wantOK:    true,
		},
		{
			name:   "webp-only image skipped",
			types:  []string{"image/webp"},
			wantOK: false,
		},
		{
			name:      "password manager hint flagged",
			types:     []string{"text/plain", "x-kde-passwordManagerHint"},
			wantKind:  KindText,
			wantFetch: "text",
			wantMIME:  "text/plain",
			wantHint:  true,
			wantOK:    true,
		},
		{
			name:   "empty offer",
			types:  nil,
			wantOK: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kind, fetch, mime, hint, ok := classify(tt.types)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if !ok {
				return
			}
			if kind != tt.wantKind || fetch != tt.wantFetch || mime != tt.wantMIME || hint != tt.wantHint {
				t.Errorf("classify() = (%v, %q, %q, %v), want (%v, %q, %q, %v)",
					kind, fetch, mime, hint, tt.wantKind, tt.wantFetch, tt.wantMIME, tt.wantHint)
			}
		})
	}
}

// watcherEnv builds a watcher whose child is an in-memory pipe and whose
// capture commands answer from canned outputs.
type watcherEnv struct {
	mu       sync.Mutex
	types    string            // --list-types response
	payloads map[string]string // fetch type → payload
	writer   io.WriteCloser
}

func (e *watcherEnv) options() WatcherOptions {
	return WatcherOptions{
		LookPath: func(string) (string, error) { return "/usr/bin/wl-paste", nil },
		Getenv: func(key string) string {
			if key == "WAYLAND_DISPLAY" {
				return "wayland-1"
			}
			return ""
		},
		StartCmd: func(argv []string) (io.ReadCloser, func() error, func(), error) {
			r, w := io.Pipe()
			e.mu.Lock()
			e.writer = w
			e.mu.Unlock()
			return r, func() error { return nil }, func() { w.Close() }, nil
		},
		Run: func(_ context.Context, argv []string) ([]byte, error) {
			e.mu.Lock()
			defer e.mu.Unlock()
			if reflect.DeepEqual(argv, []string{"wl-paste", "--list-types"}) {
				if e.types == "" {
					return nil, errors.New("clipboard empty")
				}
				return []byte(e.types), nil
			}
			if len(argv) == 4 && argv[2] == "--type" {
				if p, ok := e.payloads[argv[3]]; ok {
					return []byte(p), nil
				}
			}
			return nil, fmt.Errorf("unexpected argv %v", argv)
		},
	}
}

// signal emits one change-signal line to the watcher's scanner.
func (e *watcherEnv) signal(t *testing.T) {
	t.Helper()
	e.mu.Lock()
	w := e.writer
	e.mu.Unlock()
	if w == nil {
		t.Fatal("watcher child not started")
	}
	if _, err := io.WriteString(w, "\n"); err != nil {
		t.Fatalf("signal: %v", err)
	}
}

// eventually polls cond until it holds or the deadline passes.
func eventually(t *testing.T, within time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestWatcherCaptures(t *testing.T) {
	env := &watcherEnv{
		types:    "text/plain\nUTF8_STRING",
		payloads: map[string]string{"text": "hello clipboard"},
	}
	store := NewStore()
	w := NewWatcher(store, env.options())
	if err := w.Start(); err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	defer w.Shutdown()

	eventually(t, time.Second, "child start", func() bool {
		env.mu.Lock()
		defer env.mu.Unlock()
		return env.writer != nil
	})
	env.signal(t)
	eventually(t, time.Second, "text capture", func() bool { return len(store.List()) == 1 })

	got := store.List()[0]
	if got.Kind != KindText || got.Text != "hello clipboard" || got.MIME != "text/plain" || got.Sensitive {
		t.Errorf("entry = %+v", got)
	}

	// A hinted copy lands masked.
	env.mu.Lock()
	env.types = "text/plain\nx-kde-passwordManagerHint"
	env.payloads["text"] = "hunter2hunter2"
	env.mu.Unlock()
	env.signal(t)
	eventually(t, time.Second, "hinted capture", func() bool { return len(store.List()) == 2 })
	hinted := store.List()[0]
	if !hinted.Sensitive || hinted.MaskReason != MaskReasonHint {
		t.Errorf("hinted entry = %+v", hinted)
	}

	// Empty clipboard (list-types errors) records nothing.
	env.mu.Lock()
	env.types = ""
	env.mu.Unlock()
	env.signal(t)
	time.Sleep(20 * time.Millisecond)
	if len(store.List()) != 2 {
		t.Errorf("empty clipboard recorded an entry: %d", len(store.List()))
	}
}

func TestWatcherGating(t *testing.T) {
	t.Run("no wayland", func(t *testing.T) {
		w := NewWatcher(NewStore(), WatcherOptions{
			Getenv:   func(string) string { return "" },
			LookPath: func(string) (string, error) { return "/usr/bin/wl-paste", nil },
		})
		if err := w.Start(); err == nil || !strings.Contains(err.Error(), "Wayland") {
			t.Errorf("Start() = %v, want Wayland error", err)
		}
	})
	t.Run("no wl-paste", func(t *testing.T) {
		w := NewWatcher(NewStore(), WatcherOptions{
			Getenv:   func(string) string { return "wayland-1" },
			LookPath: func(string) (string, error) { return "", exec.ErrNotFound },
		})
		if err := w.Start(); err == nil || !strings.Contains(err.Error(), "wl-paste") {
			t.Errorf("Start() = %v, want wl-paste error", err)
		}
	})
}

func TestWatcherCrashLimit(t *testing.T) {
	var mu sync.Mutex
	starts := 0
	var logs []string

	opts := WatcherOptions{
		LookPath: func(string) (string, error) { return "/usr/bin/wl-paste", nil },
		Getenv: func(key string) string {
			if key == "WAYLAND_DISPLAY" {
				return "wayland-1"
			}
			return ""
		},
		// Child "crashes" immediately: empty stdout, wait returns an error.
		StartCmd: func([]string) (io.ReadCloser, func() error, func(), error) {
			mu.Lock()
			starts++
			mu.Unlock()
			return io.NopCloser(strings.NewReader("")), func() error { return errors.New("crash") }, func() {}, nil
		},
		Log: func(format string, args ...any) {
			mu.Lock()
			logs = append(logs, fmt.Sprintf(format, args...))
			mu.Unlock()
		},
		RestartBackoff: time.Millisecond,
		MaxBackoff:     2 * time.Millisecond,
		CrashWindow:    time.Minute,
		CrashLimit:     3,
	}
	w := NewWatcher(NewStore(), opts)
	if err := w.Start(); err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	defer w.Shutdown()

	eventually(t, time.Second, "crash-limit disable", func() bool {
		mu.Lock()
		defer mu.Unlock()
		for _, l := range logs {
			if strings.Contains(l, "disabled until reload") {
				return true
			}
		}
		return false
	})
	mu.Lock()
	if starts != 3 {
		t.Errorf("starts = %d, want 3", starts)
	}
	mu.Unlock()
}

// TestWatcherReal drives the real startRealCmd path with a stub wl-paste on a
// fake PATH, plugin-lifecycle style: the stub's --watch branch emits two
// signal lines then blocks; --list-types and --type answer canned data.
func TestWatcherReal(t *testing.T) {
	dir := t.TempDir()
	stub := filepath.Join(dir, "wl-paste")
	script := `#!/bin/sh
case "$1" in
--watch) printf '\n\n'; while true; do sleep 1; done ;;
--list-types) printf 'text/plain\nUTF8_STRING\n' ;;
--no-newline) printf 'from the real stub' ;;
esac
`
	if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	store := NewStore()
	w := NewWatcher(store, WatcherOptions{
		Getenv: func(key string) string {
			if key == "WAYLAND_DISPLAY" {
				return "wayland-1"
			}
			return ""
		},
	})
	if err := w.Start(); err != nil {
		t.Fatalf("Start() error: %v", err)
	}

	eventually(t, 2*time.Second, "both signals deduped into one entry", func() bool {
		l := store.List()
		return len(l) == 1 && l[0].Copies == 2
	})
	if got := store.List()[0]; got.Text != "from the real stub" {
		t.Errorf("entry = %+v", got)
	}

	// Shutdown must kill the blocked child promptly.
	done := make(chan struct{})
	go func() { w.Shutdown(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Shutdown() hung on a blocked child")
	}
}
