package launch

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/jourdanhaines/banshee/internal/providers"
)

// clipOptions returns Options with a fake PATH and environment for
// ResolveClipboard tests.
func clipOptions(wayland bool, onPath ...string) Options {
	path := map[string]bool{}
	for _, p := range onPath {
		path[p] = true
	}
	return Options{
		LookPath: func(file string) (string, error) {
			if path[file] {
				return "/usr/bin/" + file, nil
			}
			return "", exec.ErrNotFound
		},
		Getenv: func(key string) string {
			if key == "WAYLAND_DISPLAY" && wayland {
				return "wayland-1"
			}
			return ""
		},
	}
}

func TestResolveClipboard(t *testing.T) {
	tests := []struct {
		name     string
		wayland  bool
		onPath   []string
		wantArgv []string
		wantErr  bool
	}{
		{
			name:     "wayland prefers wl-copy",
			wayland:  true,
			onPath:   []string{"wl-copy", "xclip", "xsel"},
			wantArgv: []string{"wl-copy"},
		},
		{
			name:     "wl-copy skipped outside wayland",
			wayland:  false,
			onPath:   []string{"wl-copy", "xclip"},
			wantArgv: []string{"xclip", "-selection", "clipboard"},
		},
		{
			name:     "xsel is the last fallback",
			wayland:  true,
			onPath:   []string{"xsel"},
			wantArgv: []string{"xsel", "--clipboard", "--input"},
		},
		{
			name:    "nothing on PATH errors",
			wayland: true,
			onPath:  nil,
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			argv, err := ResolveClipboard(clipOptions(tt.wayland, tt.onPath...))
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ResolveClipboard() = %v, want error", argv)
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolveClipboard() error: %v", err)
			}
			if !reflect.DeepEqual(argv, tt.wantArgv) {
				t.Errorf("ResolveClipboard() = %v, want %v", argv, tt.wantArgv)
			}
		})
	}
}

func TestClipboardHandler(t *testing.T) {
	newDispatcher := func(opts Options) *Dispatcher {
		d := NewDispatcher()
		RegisterBuiltins(d, opts)
		return d
	}

	t.Run("copies text via stdin", func(t *testing.T) {
		var gotArgv []string
		var gotText string
		opts := clipOptions(true, "wl-copy")
		opts.RunStdin = func(argv []string, stdin io.Reader) error {
			b, err := io.ReadAll(stdin)
			if err != nil {
				return err
			}
			gotArgv, gotText = argv, string(b)
			return nil
		}
		d := newDispatcher(opts)
		if err := d.Dispatch(providers.Action{Kind: providers.ActClipboardCopy, Text: "2+2 = 4"}); err != nil {
			t.Fatalf("Dispatch() error: %v", err)
		}
		if !reflect.DeepEqual(gotArgv, []string{"wl-copy"}) {
			t.Errorf("argv = %v, want [wl-copy]", gotArgv)
		}
		if gotText != "2+2 = 4" {
			t.Errorf("stdin = %q, want %q", gotText, "2+2 = 4")
		}
	})

	t.Run("empty text errors", func(t *testing.T) {
		d := newDispatcher(clipOptions(true, "wl-copy"))
		err := d.Dispatch(providers.Action{Kind: providers.ActClipboardCopy})
		if err == nil || !strings.Contains(err.Error(), "empty text") {
			t.Fatalf("Dispatch() error = %v, want empty text error", err)
		}
	})

	t.Run("tool failure propagates", func(t *testing.T) {
		opts := clipOptions(true, "wl-copy")
		opts.RunStdin = func([]string, io.Reader) error { return errors.New("boom") }
		d := newDispatcher(opts)
		err := d.Dispatch(providers.Action{Kind: providers.ActClipboardCopy, Text: "x"})
		if err == nil || !strings.Contains(err.Error(), "boom") {
			t.Fatalf("Dispatch() error = %v, want wrapped boom", err)
		}
	})

	t.Run("no tool found errors", func(t *testing.T) {
		d := newDispatcher(clipOptions(false))
		err := d.Dispatch(providers.Action{Kind: providers.ActClipboardCopy, Text: "x"})
		if err == nil || !strings.Contains(err.Error(), "no clipboard tool") {
			t.Fatalf("Dispatch() error = %v, want no clipboard tool error", err)
		}
	})
}

func TestCopyToClipboardSensitive(t *testing.T) {
	tests := []struct {
		name     string
		wayland  bool
		onPath   []string
		wantArgv []string
	}{
		{
			name:     "wl-copy gains --sensitive",
			wayland:  true,
			onPath:   []string{"wl-copy"},
			wantArgv: []string{"wl-copy", "--sensitive"},
		},
		{
			name:     "xclip degrades to plain copy",
			wayland:  false,
			onPath:   []string{"xclip"},
			wantArgv: []string{"xclip", "-selection", "clipboard"},
		},
		{
			name:     "xsel degrades to plain copy",
			wayland:  false,
			onPath:   []string{"xsel"},
			wantArgv: []string{"xsel", "--clipboard", "--input"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotArgv []string
			var gotText string
			opts := clipOptions(tt.wayland, tt.onPath...)
			opts.RunStdin = func(argv []string, stdin io.Reader) error {
				b, err := io.ReadAll(stdin)
				if err != nil {
					return err
				}
				gotArgv, gotText = argv, string(b)
				return nil
			}
			if err := CopyToClipboardSensitive(opts, "s3cret"); err != nil {
				t.Fatalf("CopyToClipboardSensitive() error: %v", err)
			}
			if !reflect.DeepEqual(gotArgv, tt.wantArgv) {
				t.Errorf("argv = %v, want %v", gotArgv, tt.wantArgv)
			}
			if gotText != "s3cret" {
				t.Errorf("stdin = %q, want %q", gotText, "s3cret")
			}
		})
	}
}

func TestCopyToClipboardMIME(t *testing.T) {
	tests := []struct {
		name      string
		wayland   bool
		onPath    []string
		sensitive bool
		wantArgv  []string
		wantErr   string
	}{
		{
			name:     "wl-copy declares the type",
			wayland:  true,
			onPath:   []string{"wl-copy"},
			wantArgv: []string{"wl-copy", "--type", "image/png"},
		},
		{
			name:      "wl-copy sensitive appends the hint",
			wayland:   true,
			onPath:    []string{"wl-copy"},
			sensitive: true,
			wantArgv:  []string{"wl-copy", "--type", "image/png", "--sensitive"},
		},
		{
			name:     "xclip declares the type",
			wayland:  false,
			onPath:   []string{"xclip"},
			wantArgv: []string{"xclip", "-selection", "clipboard", "-t", "image/png"},
		},
		{
			name:    "xsel cannot offer a MIME type",
			wayland: false,
			onPath:  []string{"xsel"},
			wantErr: "cannot offer MIME type",
		},
		{
			name:    "no tool found errors",
			wayland: false,
			wantErr: "no clipboard tool",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotArgv []string
			var gotData string
			opts := clipOptions(tt.wayland, tt.onPath...)
			opts.RunStdin = func(argv []string, stdin io.Reader) error {
				b, err := io.ReadAll(stdin)
				if err != nil {
					return err
				}
				gotArgv, gotData = argv, string(b)
				return nil
			}
			err := CopyToClipboardMIME(opts, "image/png", strings.NewReader("\x89PNG"), tt.sensitive)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("CopyToClipboardMIME() error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("CopyToClipboardMIME() error: %v", err)
			}
			if !reflect.DeepEqual(gotArgv, tt.wantArgv) {
				t.Errorf("argv = %v, want %v", gotArgv, tt.wantArgv)
			}
			if gotData != "\x89PNG" {
				t.Errorf("stdin = %q, want png bytes", gotData)
			}
		})
	}
}

// TestCopyToClipboardReal exercises the real runStdinCmd path with a fake
// tool script that writes its stdin to a file.
func TestCopyToClipboardReal(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "out")
	script := filepath.Join(dir, "xsel")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexec /bin/cat > "+out+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)

	opts := Options{Getenv: func(string) string { return "" }}
	if err := CopyToClipboard(opts, "hello 42"); err != nil {
		t.Fatalf("CopyToClipboard() error: %v", err)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello 42" {
		t.Errorf("copied %q, want %q", got, "hello 42")
	}
}
