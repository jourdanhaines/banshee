package cliphist

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Supervision constants, mirroring the exec-plugin host: a crashing wl-paste
// is restarted with exponential backoff, and one that keeps dying is disabled
// until the next Start (i.e. `banshee reload`) rather than looping forever.
const (
	defaultRestartBackoff = 250 * time.Millisecond
	defaultMaxBackoff     = 5 * time.Second
	defaultCrashWindow    = 30 * time.Second
	defaultCrashLimit     = 3
	// captureTimeout bounds one list-types + typed-fetch round trip.
	captureTimeout = 2 * time.Second
)

// signalArgv is the long-lived watcher child. The spawned per-event command
// drains the clipboard payload (so wl-paste never sees EPIPE) and only then
// emits one newline on the inherited stdout — meaning a signal line is never
// observed before the content it announces is committed to the clipboard.
var signalArgv = []string{"wl-paste", "--watch", "sh", "-c", "cat >/dev/null; echo"}

// WatcherOptions injects everything the watcher touches outside this package.
// All fields may be nil/zero; defaults are the real thing.
type WatcherOptions struct {
	// LookPath resolves a binary name. nil uses exec.LookPath.
	LookPath func(file string) (string, error)
	// Getenv reads the environment. nil uses os.Getenv.
	Getenv func(key string) string
	// StartCmd starts the long-lived signal child and returns its stdout,
	// a wait joiner and a killer. nil spawns the real wl-paste in its own
	// process group.
	StartCmd func(argv []string) (stdout io.ReadCloser, wait func() error, kill func(), err error)
	// Run executes one short-lived capture command and returns its stdout.
	// nil uses exec.CommandContext.
	Run func(ctx context.Context, argv []string) ([]byte, error)
	// Log receives supervision events. Capture logs carry kind/size/MIME
	// only — never clipboard content. nil discards.
	Log func(format string, args ...any)

	// Backoff/crash knobs; zero values take the plugin-host defaults above.
	RestartBackoff time.Duration
	MaxBackoff     time.Duration
	CrashWindow    time.Duration
	CrashLimit     int
}

// Watcher owns the wl-paste --watch child and turns its change signals into
// Store entries. Boot starts it once per daemon (when clipboard_history is
// on) and shuts it down at exit; Start/Shutdown may cycle across reloads.
type Watcher struct {
	store *Store
	opts  WatcherOptions

	mu      sync.Mutex
	running bool
	cancel  context.CancelFunc
	done    chan struct{}
}

// NewWatcher returns a stopped watcher feeding store.
func NewWatcher(store *Store, opts WatcherOptions) *Watcher {
	if opts.RestartBackoff <= 0 {
		opts.RestartBackoff = defaultRestartBackoff
	}
	if opts.MaxBackoff <= 0 {
		opts.MaxBackoff = defaultMaxBackoff
	}
	if opts.CrashWindow <= 0 {
		opts.CrashWindow = defaultCrashWindow
	}
	if opts.CrashLimit <= 0 {
		opts.CrashLimit = defaultCrashLimit
	}
	return &Watcher{store: store, opts: opts}
}

// Start launches the supervision loop. It reports (rather than silently
// ignoring) an environment where clipboard history cannot work, so boot can
// log why the feature is absent; calling Start on a running watcher is a
// no-op. A previous crash-limit disable is cleared — Start after reload gives
// wl-paste a fresh chance.
func (w *Watcher) Start() error {
	if w.getenv("WAYLAND_DISPLAY") == "" {
		return errors.New("cliphist: no Wayland session; clipboard history disabled")
	}
	if _, err := w.lookPath("wl-paste"); err != nil {
		return errors.New("cliphist: wl-paste not found (install wl-clipboard); clipboard history disabled")
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	if w.running {
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	w.running = true
	w.cancel = cancel
	w.done = make(chan struct{})
	go w.supervise(ctx, w.done)
	return nil
}

// Shutdown stops the loop and kills the child. Safe to call when stopped.
func (w *Watcher) Shutdown() {
	w.mu.Lock()
	if !w.running {
		w.mu.Unlock()
		return
	}
	w.running = false
	cancel, done := w.cancel, w.done
	w.mu.Unlock()

	cancel()
	<-done
}

// supervise runs the child, restarting on exit with backoff, until ctx is
// canceled or the crash limit trips.
func (w *Watcher) supervise(ctx context.Context, done chan<- struct{}) {
	defer close(done)

	backoff := w.opts.RestartBackoff
	var crashes []time.Time

	for ctx.Err() == nil {
		started := time.Now()
		err := w.runChild(ctx)
		if ctx.Err() != nil {
			return
		}
		w.logf("cliphist: watcher exited: %v", err)

		// A child that survived a full crash window earns a fresh slate.
		if time.Since(started) > w.opts.CrashWindow {
			crashes = nil
			backoff = w.opts.RestartBackoff
		}
		crashes = append(crashes, time.Now())
		crashes = pruneOld(crashes, w.opts.CrashWindow)
		if len(crashes) >= w.opts.CrashLimit {
			w.logf("cliphist: watcher crashed %d times in %s; disabled until reload", len(crashes), w.opts.CrashWindow)
			return
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff *= 2; backoff > w.opts.MaxBackoff {
			backoff = w.opts.MaxBackoff
		}
	}
}

// runChild spawns one wl-paste --watch child and consumes its signal lines
// until it exits or ctx is canceled.
func (w *Watcher) runChild(ctx context.Context) error {
	stdout, wait, kill, err := w.startCmd(signalArgv)
	if err != nil {
		return err
	}

	killed := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			kill()
			// Unblock the scanner even if the kill raced the child's exit.
			stdout.Close()
		case <-killed:
		}
	}()

	sc := bufio.NewScanner(stdout)
	for sc.Scan() {
		if ctx.Err() != nil {
			break
		}
		w.capture(ctx)
	}
	close(killed)
	stdout.Close()
	return wait()
}

// capture pulls the current clipboard: list the offered types, classify,
// fetch under the chosen type, run the sensitivity heuristics and record.
func (w *Watcher) capture(ctx context.Context) {
	cctx, cancel := context.WithTimeout(ctx, captureTimeout)
	defer cancel()

	raw, err := w.run(cctx, []string{"wl-paste", "--list-types"})
	if err != nil {
		// Includes the empty-clipboard case (wl-paste exits non-zero); there
		// is nothing to record either way.
		return
	}
	types := strings.Fields(string(raw))
	kind, fetchType, mime, hinted, ok := classify(types)
	if !ok {
		return
	}

	data, err := w.run(cctx, []string{"wl-paste", "--no-newline", "--type", fetchType})
	if err != nil {
		w.logf("cliphist: fetch %s failed: %v", fetchType, err)
		return
	}
	if len(data) == 0 {
		return
	}

	sensitive, reason := false, ""
	if hinted {
		sensitive, reason = true, MaskReasonHint
	} else if kind == KindText {
		sensitive, reason = LooksSecret(string(data))
	}

	if _, added := w.store.Add(kind, mime, data, sensitive, reason); added {
		w.logf("cliphist: captured %s (%s, %d bytes)", kindName(kind), mime, len(data))
	}
}

// classify picks what to fetch from a clipboard offer's type list. Priority:
// copied files beat an image thumbnail some file managers also offer, images
// beat the text fallback browsers offer next to them. Images are limited to
// the pixbuf-safe png/jpeg allowlist — an offer with only e.g. image/webp is
// skipped, not mangled. No usable type (the nil/clear states) reports !ok.
func classify(types []string) (kind Kind, fetchType, mime string, hinted, ok bool) {
	has := make(map[string]bool, len(types))
	anyText := false
	for _, t := range types {
		has[t] = true
		if strings.HasPrefix(t, "text/") || t == "UTF8_STRING" || t == "STRING" || t == "TEXT" {
			anyText = true
		}
	}
	hinted = has[hintMIME]

	switch {
	case has["text/uri-list"]:
		return KindFiles, "text/uri-list", "text/uri-list", hinted, true
	case has["image/png"]:
		return KindImage, "image/png", "image/png", hinted, true
	case has["image/jpeg"]:
		return KindImage, "image/jpeg", "image/jpeg", hinted, true
	case anyText:
		// The generic "text" selector lets wl-paste pick whichever concrete
		// text type the source offered.
		return KindText, "text", "text/plain", hinted, true
	default:
		return 0, "", "", hinted, false
	}
}

func kindName(k Kind) string {
	switch k {
	case KindImage:
		return "image"
	case KindFiles:
		return "files"
	default:
		return "text"
	}
}

// pruneOld drops crash timestamps older than window.
func pruneOld(ts []time.Time, window time.Duration) []time.Time {
	cutoff := time.Now().Add(-window)
	out := ts[:0]
	for _, t := range ts {
		if t.After(cutoff) {
			out = append(out, t)
		}
	}
	return out
}

func (w *Watcher) lookPath(file string) (string, error) {
	if w.opts.LookPath != nil {
		return w.opts.LookPath(file)
	}
	return exec.LookPath(file)
}

func (w *Watcher) getenv(key string) string {
	if w.opts.Getenv != nil {
		return w.opts.Getenv(key)
	}
	return os.Getenv(key)
}

func (w *Watcher) startCmd(argv []string) (io.ReadCloser, func() error, func(), error) {
	if w.opts.StartCmd != nil {
		return w.opts.StartCmd(argv)
	}
	return startRealCmd(argv)
}

func (w *Watcher) run(ctx context.Context, argv []string) ([]byte, error) {
	if w.opts.Run != nil {
		return w.opts.Run(ctx, argv)
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	return cmd.Output()
}

func (w *Watcher) logf(format string, args ...any) {
	if w.opts.Log != nil {
		w.opts.Log(format, args...)
	}
}

// startRealCmd spawns argv in its own process group so kill reaches the
// per-event helpers wl-paste forks, exactly like the exec-plugin host does
// for its children.
func startRealCmd(argv []string) (io.ReadCloser, func() error, func(), error) {
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, nil, fmt.Errorf("cliphist: start %s: %w", argv[0], err)
	}
	kill := func() {
		syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	return stdout, cmd.Wait, kill, nil
}
