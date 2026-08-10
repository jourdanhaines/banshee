package cliphist

import (
	"errors"
	"io"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jourdanhaines/banshee/internal/launch"
	"github.com/jourdanhaines/banshee/internal/providers"
)

// recorder collects what the handlers copied, thread-safe because handleCopy
// detaches.
type recorder struct {
	mu        sync.Mutex
	texts     []string
	streams   []string
	mimes     []string
	sensitive []bool
	notices   []string
	reopens   []string
}

func (r *recorder) deps(s *Store) Deps {
	return Deps{
		Store: s,
		CopyText: func(text string, sensitive bool) error {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.texts = append(r.texts, text)
			r.sensitive = append(r.sensitive, sensitive)
			return nil
		},
		CopyStream: func(mime string, rd io.Reader, sensitive bool) error {
			b, err := io.ReadAll(rd)
			if err != nil {
				return err
			}
			r.mu.Lock()
			defer r.mu.Unlock()
			r.streams = append(r.streams, string(b))
			r.mimes = append(r.mimes, mime)
			r.sensitive = append(r.sensitive, sensitive)
			return nil
		},
		Notify: func(msg string) {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.notices = append(r.notices, msg)
		},
		Reopen: func(q string) {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.reopens = append(r.reopens, q)
		},
	}
}

func (r *recorder) snapshot() recorder {
	r.mu.Lock()
	defer r.mu.Unlock()
	return recorder{
		texts: append([]string(nil), r.texts...), streams: append([]string(nil), r.streams...),
		mimes: append([]string(nil), r.mimes...), sensitive: append([]bool(nil), r.sensitive...),
		notices: append([]string(nil), r.notices...), reopens: append([]string(nil), r.reopens...),
	}
}

func dispatcherWith(deps Deps) *launch.Dispatcher {
	d := launch.NewDispatcher()
	RegisterHandlersWith(d, deps)
	return d
}

func copyAction(id uint64) providers.Action {
	return providers.Action{Kind: ActClipCopy, Argv: []string{strconv.FormatUint(id, 10)}}
}

func TestHandleCopy(t *testing.T) {
	t.Run("text entry copies raw content with sensitivity flag", func(t *testing.T) {
		s := NewStore()
		e, _ := s.Add(KindText, "text/plain", []byte("s3cret-token-value"), true, "looks like a secret")
		rec := &recorder{}
		d := dispatcherWith(rec.deps(s))

		if err := d.Dispatch(copyAction(e.ID)); err != nil {
			t.Fatalf("Dispatch error: %v", err)
		}
		eventually(t, time.Second, "detached copy", func() bool { return len(rec.snapshot().texts) == 1 })
		got := rec.snapshot()
		if got.texts[0] != "s3cret-token-value" || !got.sensitive[0] {
			t.Errorf("copied (%q, sensitive=%v), want raw value with sensitive=true", got.texts[0], got.sensitive[0])
		}
	})

	t.Run("files entry streams the uri-list", func(t *testing.T) {
		s := NewStore()
		e, _ := s.Add(KindFiles, "text/uri-list", []byte("file:///a\nfile:///b\n"), false, "")
		rec := &recorder{}
		d := dispatcherWith(rec.deps(s))

		if err := d.Dispatch(copyAction(e.ID)); err != nil {
			t.Fatalf("Dispatch error: %v", err)
		}
		eventually(t, time.Second, "detached stream", func() bool { return len(rec.snapshot().streams) == 1 })
		got := rec.snapshot()
		if got.mimes[0] != "text/uri-list" || got.streams[0] != "file:///a\nfile:///b\n" {
			t.Errorf("streamed (%q, %q)", got.mimes[0], got.streams[0])
		}
	})

	t.Run("image entry streams the runtime file", func(t *testing.T) {
		s := NewStore(WithImageDir(t.TempDir()))
		e, _ := s.Add(KindImage, "image/png", []byte("png-payload"), false, "")
		rec := &recorder{}
		d := dispatcherWith(rec.deps(s))

		if err := d.Dispatch(copyAction(e.ID)); err != nil {
			t.Fatalf("Dispatch error: %v", err)
		}
		eventually(t, time.Second, "detached image stream", func() bool { return len(rec.snapshot().streams) == 1 })
		got := rec.snapshot()
		if got.mimes[0] != "image/png" || got.streams[0] != "png-payload" {
			t.Errorf("streamed (%q, %q)", got.mimes[0], got.streams[0])
		}
	})

	t.Run("copy failure reaches Notify without content", func(t *testing.T) {
		s := NewStore()
		e, _ := s.Add(KindText, "text/plain", []byte("private-content"), false, "")
		rec := &recorder{}
		deps := rec.deps(s)
		deps.CopyText = func(string, bool) error { return errors.New("boom") }
		d := dispatcherWith(deps)

		if err := d.Dispatch(copyAction(e.ID)); err != nil {
			t.Fatalf("Dispatch error: %v", err)
		}
		eventually(t, time.Second, "notify", func() bool { return len(rec.snapshot().notices) == 1 })
		if msg := rec.snapshot().notices[0]; strings.Contains(msg, "private-content") {
			t.Errorf("notification leaked content: %q", msg)
		}
	})

	t.Run("bad ids are synchronous errors", func(t *testing.T) {
		s := NewStore()
		d := dispatcherWith((&recorder{}).deps(s))
		for _, argv := range [][]string{nil, {"not-a-number"}, {"42"}} {
			err := d.Dispatch(providers.Action{Kind: ActClipCopy, Argv: argv})
			if err == nil {
				t.Errorf("Dispatch(%v) succeeded, want error", argv)
			}
		}
	})
}

func TestHandleDelete(t *testing.T) {
	s := NewStore()
	e, _ := s.Add(KindText, "text/plain", []byte("bye"), false, "")
	rec := &recorder{}
	d := dispatcherWith(rec.deps(s))

	if err := d.Dispatch(providers.Action{Kind: ActClipDelete, Argv: []string{strconv.FormatUint(e.ID, 10)}}); err != nil {
		t.Fatalf("Dispatch error: %v", err)
	}
	if len(s.List()) != 0 {
		t.Error("entry survives delete")
	}
	got := rec.snapshot()
	if len(got.reopens) != 1 || got.reopens[0] != "clip" {
		t.Errorf("reopens = %v, want [clip]", got.reopens)
	}

	// Deleting a gone entry is an error, not a panic.
	if err := d.Dispatch(providers.Action{Kind: ActClipDelete, Argv: []string{"0"}}); err == nil {
		t.Error("second delete succeeded")
	}
}

func TestRegisterHandlersGoldenArgv(t *testing.T) {
	// End-to-end through the real launch wiring: RegisterHandlers → dispatch →
	// recorded RunStdin argv, sensitive text gains wl-copy --sensitive.
	s := NewStore()
	e, _ := s.Add(KindText, "text/plain", []byte("tok"), true, "looks like a secret")

	var mu sync.Mutex
	var gotArgv []string
	opts := launch.Options{
		LookPath: func(file string) (string, error) { return "/usr/bin/" + file, nil },
		Getenv: func(key string) string {
			if key == "WAYLAND_DISPLAY" {
				return "wayland-1"
			}
			return ""
		},
		RunStdin: func(argv []string, stdin io.Reader) error {
			io.Copy(io.Discard, stdin)
			mu.Lock()
			gotArgv = append([]string(nil), argv...)
			mu.Unlock()
			return nil
		},
	}
	d := launch.NewDispatcher()
	RegisterHandlers(d, opts, s)

	if err := d.Dispatch(copyAction(e.ID)); err != nil {
		t.Fatalf("Dispatch error: %v", err)
	}
	eventually(t, time.Second, "argv recorded", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return gotArgv != nil
	})
	mu.Lock()
	defer mu.Unlock()
	want := []string{"wl-copy", "--sensitive"}
	if len(gotArgv) != len(want) || gotArgv[0] != want[0] || gotArgv[1] != want[1] {
		t.Errorf("argv = %v, want %v", gotArgv, want)
	}
}
