package plugins

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/jourdanhaines/banshee/internal/providers"
	"github.com/jourdanhaines/banshee/internal/providers/connectors"
)

// Defaults for Options.
const (
	// DefaultTimeout is the soft per-query deadline. Results that arrive
	// later are discarded; the query returns whatever arrived in time.
	DefaultTimeout = 150 * time.Millisecond
	// DefaultRestartBackoff is the pause after a crash before the plugin is
	// started again; it doubles per consecutive crash up to MaxBackoff.
	DefaultRestartBackoff = 250 * time.Millisecond
	// MaxBackoff caps the restart backoff.
	MaxBackoff = 5 * time.Second
	// DefaultCrashWindow is the sliding window for crash counting.
	DefaultCrashWindow = 30 * time.Second
	// DefaultCrashLimit is how many crashes within the window disable a
	// plugin until the host is reloaded.
	DefaultCrashLimit = 3
	// shutdownGrace is how long a plugin gets to exit after "shutdown", and
	// again how long the kill that follows is waited on.
	shutdownGrace = 500 * time.Millisecond
	// maxLine bounds a single JSON line read from a plugin.
	maxLine = 1 << 20
)

// ErrDisabled is returned when a plugin has been disabled after repeated
// crashes; it stays disabled until the host is reloaded.
var ErrDisabled = errors.New("plugins: plugin disabled after repeated crashes")

// ErrClosed is returned by a plugin that has been shut down. Shutdown is
// terminal: the host builds fresh ExecPlugins on every Load, so a query that
// races a reload must not resurrect a plugin nobody references any more.
var ErrClosed = errors.New("plugins: plugin has been shut down")

// Options tunes plugin-host behavior. The zero value uses the Default*
// constants.
type Options struct {
	// Timeout is the soft per-query deadline for plugins whose manifest does
	// not set exec.timeout_ms.
	Timeout time.Duration
	// RestartBackoff is the base pause after a crash.
	RestartBackoff time.Duration
	// CrashWindow is the sliding window crashes are counted in.
	CrashWindow time.Duration
	// CrashLimit is the crash count within CrashWindow that disables a plugin.
	CrashLimit int
	// Stderr receives plugin stderr; nil discards it.
	Stderr io.Writer
}

func (o Options) withDefaults() Options {
	if o.Timeout <= 0 {
		o.Timeout = DefaultTimeout
	}
	if o.RestartBackoff <= 0 {
		o.RestartBackoff = DefaultRestartBackoff
	}
	if o.CrashWindow <= 0 {
		o.CrashWindow = DefaultCrashWindow
	}
	if o.CrashLimit <= 0 {
		o.CrashLimit = DefaultCrashLimit
	}
	return o
}

// pending collects the results of one in-flight query.
type pending struct {
	seq  uint64
	mu   sync.Mutex
	res  []providers.Result
	done chan struct{}
	once sync.Once
}

func (p *pending) add(rs []providers.Result) {
	p.mu.Lock()
	p.res = append(p.res, rs...)
	p.mu.Unlock()
}

func (p *pending) snapshot() []providers.Result {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]providers.Result(nil), p.res...)
}

func (p *pending) finish() { p.once.Do(func() { close(p.done) }) }

// ExecPlugin is a providers.Provider backed by a long-running child process
// speaking the JSON-lines plugin protocol. It is safe for concurrent use.
type ExecPlugin struct {
	m       connectors.Manifest
	opts    Options
	timeout time.Duration
	prefix  string

	mu       sync.Mutex
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	stdout   io.ReadCloser
	exited   chan struct{}
	gen      uint64 // process generation; stale goroutines are ignored
	running  bool
	stopping bool
	closed   bool // terminal: set by Shutdown, never cleared
	seq      uint64
	cur      *pending
	crashes  []time.Time
	disabled bool
	nextTry  time.Time

	// wmu serializes stdin writes, which happen without holding mu so a full
	// pipe can never deadlock against the reader.
	wmu sync.Mutex
}

// NewExecPlugin builds a provider for an exec-type manifest. The process is
// started lazily on the first matching query.
func NewExecPlugin(m connectors.Manifest, opts Options) (*ExecPlugin, error) {
	if m.Type != connectors.TypeExec || m.Exec == nil {
		return nil, fmt.Errorf("plugins: %q is not an exec plugin", m.ID)
	}
	opts = opts.withDefaults()
	p := &ExecPlugin{m: m, opts: opts, timeout: opts.Timeout, prefix: m.Exec.Prefix}
	if ms := m.Exec.TimeoutMS; ms > 0 {
		// Clamped again here (ParseManifest already clamps) so a Manifest built
		// in code cannot stall the aggregator: every provider is joined before
		// Query returns, so one plugin's timeout is the whole launcher's.
		if ms > connectors.MaxExecTimeoutMS {
			ms = connectors.MaxExecTimeoutMS
		}
		p.timeout = time.Duration(ms) * time.Millisecond
	}
	return p, nil
}

// ID returns the plugin id.
func (p *ExecPlugin) ID() string { return p.m.ID }

// Name implements providers.Provider.
func (p *ExecPlugin) Name() string { return "plugin:" + p.m.ID }

// Disabled reports whether the plugin was disabled after repeated crashes.
func (p *ExecPlugin) Disabled() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.disabled
}

// MatchQuery applies the plugin's prefix gate. ok is false when the plugin
// should not see this query at all; otherwise the returned string is the query
// with the prefix (and one separating space) removed. The comparison is
// case-insensitive, and the prefix must be followed by a space or by nothing:
// with prefix "wifi", "wifi" and "WiFi list" match, "wifikill" does not.
func (p *ExecPlugin) MatchQuery(q string) (string, bool) {
	q = strings.TrimSpace(q)
	if q == "" {
		return "", false
	}
	if p.prefix == "" {
		return q, true
	}
	if len(q) < len(p.prefix) || !strings.EqualFold(q[:len(p.prefix)], p.prefix) {
		return "", false
	}
	rest := q[len(p.prefix):]
	if rest != "" && rest[0] != ' ' {
		return "", false
	}
	return strings.TrimSpace(rest), true
}

// Query implements providers.Provider. It sends a query event and waits for
// results until the plugin reports done, the soft timeout elapses (partial
// results are returned) or ctx is cancelled.
func (p *ExecPlugin) Query(ctx context.Context, q string) ([]providers.Result, error) {
	sub, ok := p.MatchQuery(q)
	if !ok {
		return nil, nil
	}
	pend, err := p.send(sub)
	if err != nil {
		// Disabled, backing off or torn down: an expected, already-reported
		// state, not a per-keystroke error for the aggregator to log.
		if errors.Is(err, ErrDisabled) || errors.Is(err, errBackoff) || errors.Is(err, ErrClosed) {
			return nil, nil
		}
		return nil, err
	}
	timer := time.NewTimer(p.timeout)
	defer timer.Stop()
	var cancelled bool
	select {
	case <-pend.done:
	case <-timer.C:
	case <-ctx.Done():
		cancelled = true
	}
	p.retire(pend)
	if cancelled {
		return nil, ctx.Err()
	}
	return pend.snapshot(), nil
}

// Activate sends an activate event for one of the plugin's results. It does
// not wait for the acknowledgement.
func (p *ExecPlugin) Activate(resultID string) error {
	p.mu.Lock()
	if err := p.ensureStartedLocked(); err != nil {
		p.mu.Unlock()
		return err
	}
	p.seq++
	ev := Event{V: ProtoVersion, Event: EventActivate, Seq: p.seq, ID: resultID}
	stdin := p.stdin
	p.mu.Unlock()
	return p.write(stdin, ev)
}

// Submit sends a form result's submitted values to the plugin. Like Activate
// it is fire-and-forget — with the same caveat: a plugin that has crashed
// since emitting the result is restarted here and has no memory of the
// result id, so the values are silently dropped. An acknowledged submit
// would need a second pending slot and is left as future work.
func (p *ExecPlugin) Submit(resultID string, values map[string]string) error {
	p.mu.Lock()
	if err := p.ensureStartedLocked(); err != nil {
		p.mu.Unlock()
		return err
	}
	p.seq++
	ev := Event{V: ProtoVersion, Event: EventSubmit, Seq: p.seq, ID: resultID, Values: values}
	stdin := p.stdin
	p.mu.Unlock()
	return p.write(stdin, ev)
}

// Shutdown asks the plugin to exit, kills its process group if it does not,
// and gives up waiting rather than blocking the caller forever.
//
// Shutdown is terminal: the plugin can never be started again. Host.Load
// replaces the whole plugin set, so a query goroutine that raced the reload
// would otherwise call ensureStarted on a torn-down plugin and spawn an orphan
// child nothing references.
//
// The bounded waits matter because both callers run on the GTK main loop
// (Host.Load from `banshee reload`) or on the daemon's exit path: a plugin
// that forked a helper holding its stdout keeps the pipe open after the direct
// child dies, so an unbounded wait on the reader would freeze the launcher.
func (p *ExecPlugin) Shutdown() {
	p.mu.Lock()
	p.closed = true
	if !p.running {
		p.mu.Unlock()
		return
	}
	p.stopping = true
	cmd, stdin, stdout, exited := p.cmd, p.stdin, p.stdout, p.exited
	if p.cur != nil {
		p.cur.finish()
		p.cur = nil
	}
	p.mu.Unlock()

	_ = p.write(stdin, Event{V: ProtoVersion, Event: EventShutdown})
	if stdin != nil {
		_ = stdin.Close()
	}
	select {
	case <-exited:
		return
	case <-time.After(shutdownGrace):
	}

	killGroup(cmd)
	// Closing our end of stdout unblocks readLoop even when a grandchild still
	// holds the write end, which is what lets waitLoop reach cmd.Wait.
	if stdout != nil {
		_ = stdout.Close()
	}
	select {
	case <-exited:
	case <-time.After(shutdownGrace):
		// The child is unreapable from here (a helper is holding a pipe open
		// past SIGKILL). Leaking the goroutine beats wedging the main loop.
	}
}

// killGroup SIGKILLs the plugin and everything it spawned. Plugins are started
// with Setpgid so a shell plugin's background helpers die with it; the direct
// process is killed as well in case the group signal fails.
func killGroup(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	pid := cmd.Process.Pid
	if pid > 0 {
		_ = syscall.Kill(-pid, syscall.SIGKILL)
	}
	_ = cmd.Process.Kill()
}

var errBackoff = errors.New("plugins: plugin restarting")

// send starts the plugin if needed and writes a query event, returning the
// pending slot the reader will fill.
func (p *ExecPlugin) send(q string) (*pending, error) {
	p.mu.Lock()
	if err := p.ensureStartedLocked(); err != nil {
		p.mu.Unlock()
		return nil, err
	}
	p.seq++
	pend := &pending{seq: p.seq, done: make(chan struct{})}
	if p.cur != nil {
		// Supersede the previous query; its late results are now stale.
		p.cur.finish()
	}
	p.cur = pend
	stdin := p.stdin
	p.mu.Unlock()

	if err := p.write(stdin, Event{V: ProtoVersion, Event: EventQuery, Seq: pend.seq, Query: q}); err != nil {
		p.retire(pend)
		return nil, err
	}
	return pend, nil
}

// retire clears pend as the current query so any later message for its seq is
// discarded. Called with p.mu unheld.
func (p *ExecPlugin) retire(pend *pending) {
	p.mu.Lock()
	if p.cur == pend {
		p.cur = nil
	}
	p.mu.Unlock()
	pend.finish()
}

// write serializes one event onto the plugin's stdin. It must be called
// without holding p.mu.
func (p *ExecPlugin) write(stdin io.Writer, e Event) error {
	if stdin == nil {
		return errors.New("plugins: plugin stdin closed")
	}
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	p.wmu.Lock()
	defer p.wmu.Unlock()
	_, err = stdin.Write(append(line, '\n'))
	return err
}

func (p *ExecPlugin) ensureStartedLocked() error {
	if p.closed {
		return ErrClosed
	}
	if p.disabled {
		return ErrDisabled
	}
	if p.running {
		return nil
	}
	if time.Now().Before(p.nextTry) {
		return errBackoff
	}
	if err := p.startLocked(); err != nil {
		// A plugin that cannot even start (missing binary, no execute bit, bad
		// interpreter) is the most common failure there is. Counting it as a
		// crash gives it the same backoff-then-disable treatment as one that
		// dies later, instead of a fork+exec attempt on every keystroke.
		p.recordCrashLocked(time.Now())
		return err
	}
	return nil
}

func (p *ExecPlugin) startLocked() error {
	bin := p.m.Exec.Bin
	if strings.ContainsRune(bin, os.PathSeparator) {
		if !filepath.IsAbs(bin) {
			bin = filepath.Join(p.m.Dir, bin)
		}
	}
	cmd := exec.Command(bin, p.m.Exec.Args...)
	cmd.Dir = p.m.Dir
	cmd.Env = append(os.Environ(),
		"BANSHEE_PLUGIN_ID="+p.m.ID,
		"BANSHEE_PLUGIN_DIR="+p.m.Dir,
		"BANSHEE_PLUGIN_PROTO="+fmt.Sprint(ProtoVersion),
	)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return err
	}
	cmd.Stderr = p.opts.Stderr
	// Own process group: a shell plugin that backgrounds a helper would
	// otherwise keep the helper (and our stdout pipe) alive past SIGKILL.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return err
	}

	p.gen++
	gen := p.gen
	p.cmd = cmd
	p.stdin = stdin
	p.stdout = stdout
	p.exited = make(chan struct{})
	p.running = true
	p.stopping = false

	readDone := make(chan struct{})
	go p.readLoop(gen, cmd, stdout, readDone)
	go p.waitLoop(gen, cmd, p.exited, readDone)
	return nil
}

func (p *ExecPlugin) readLoop(gen uint64, cmd *exec.Cmd, stdout io.Reader, readDone chan struct{}) {
	// Wait() closes the pipe, so it must not run until this loop returns.
	defer close(readDone)
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), maxLine)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || line[0] != '{' {
			continue
		}
		var msg Message
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			continue // malformed lines are ignored, never fatal
		}
		if msg.Event != "" && msg.Event != EventResults {
			continue // activated / unknown events carry no results
		}
		p.deliver(gen, msg)
	}
	// A scanner error (most often bufio.ErrTooLong on a line past maxLine) ends
	// the loop with the child still alive and still writing. Left alone it
	// would fill the pipe, never exit, never be classified as a crash, and
	// answer every later query with nothing. Kill it so waitLoop can.
	if err := sc.Err(); err != nil {
		killGroup(cmd)
	}
}

// deliver merges a message into the current pending query, discarding stale
// sequence numbers.
func (p *ExecPlugin) deliver(gen uint64, msg Message) {
	p.mu.Lock()
	if gen != p.gen || p.cur == nil || p.cur.seq != msg.Seq {
		p.mu.Unlock()
		return
	}
	cur := p.cur
	m := p.m
	p.mu.Unlock()

	if len(msg.Results) > 0 {
		out := make([]providers.Result, 0, len(msg.Results))
		for _, w := range msg.Results {
			if w.Title == "" {
				continue
			}
			out = append(out, w.toResult(m))
		}
		cur.add(out)
	}
	if msg.Done {
		cur.finish()
	}
}

func (p *ExecPlugin) waitLoop(gen uint64, cmd *exec.Cmd, exited, readDone chan struct{}) {
	<-readDone
	_ = cmd.Wait()
	defer close(exited)
	p.mu.Lock()
	defer p.mu.Unlock()
	if gen != p.gen {
		return
	}
	p.running = false
	p.stdin = nil
	p.stdout = nil
	if p.cur != nil {
		p.cur.finish()
		p.cur = nil
	}
	if p.stopping {
		p.stopping = false
		return
	}
	p.recordCrashLocked(time.Now())
}

func (p *ExecPlugin) recordCrashLocked(now time.Time) {
	cutoff := now.Add(-p.opts.CrashWindow)
	kept := p.crashes[:0]
	for _, t := range p.crashes {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	p.crashes = append(kept, now)
	if len(p.crashes) >= p.opts.CrashLimit {
		p.disabled = true
		return
	}
	backoff := p.opts.RestartBackoff << (len(p.crashes) - 1)
	if backoff > MaxBackoff {
		backoff = MaxBackoff
	}
	p.nextTry = now.Add(backoff)
}
