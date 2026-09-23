package plugins

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jourdanhaines/banshee/internal/providers"
)

// screenrecDir is the in-repo screen recording plugin shipped as
// plugins/screenrec.
func screenrecDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("..", "..", "..", "plugins", "screenrec"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, ManifestName)); err != nil {
		t.Skipf("screenrec plugin not present: %v", err)
	}
	return dir
}

// screenrecCoreTools is the PATH allowlist the plugin runs against besides
// its stubs. It must track every external command plugin.sh (and the stubs
// below) invoke; jq is real on purpose because its filter is the plugin's
// window-selection logic.
var screenrecCoreTools = []string{
	"sh", "sed", "awk", "tr", "ls", "stat", "date", "head", "tail", "basename",
	"dirname", "mkdir", "mv", "rm", "cp", "cat", "sleep", "grep", "setsid",
	"jq", "find", "sort", "wc", "env",
}

// screenrecStubNames lists every tool the harness can stub.
var screenrecStubNames = []string{
	"wf-recorder", "slurp", "mpv", "wl-copy", "hyprctl", "ffmpeg", "xdg-open", "pactl",
}

// screenrecStubOpts selects the stubbed environment for one plugin process.
type screenrecStubOpts struct {
	// omit leaves these tools out of the stub dir entirely.
	omit []string
	// pactlSources is what `pactl list short sources` prints.
	pactlSources string
	// slurpCancel makes slurp exit 1 like an Esc at the picker.
	slurpCancel bool
}

// screenrecEnv is one hermetic plugin environment.
type screenrecEnv struct {
	// logDir receives one <tool>.log per stub: an invocation per line, argv
	// joined by \x1f. slurp.stdin / wl-copy.stdin hold the last stdin,
	// wf-recorder.pid its pids and wf-recorder.events the signals it caught.
	logDir  string
	outDir  string
	runDir  string
	state   string
	partial string
}

// Canned hyprctl data: DP-1 (ws 1) is focused and HDMI-A-1 shows ws 2, so only
// the mapped, visible clients 0x1 and 0x2 are pickable windows.
const (
	screenrecMonitorsJSON = `[{"name":"DP-1","focused":true,"activeWorkspace":{"id":1},"specialWorkspace":{"id":0}},{"name":"HDMI-A-1","focused":false,"activeWorkspace":{"id":2},"specialWorkspace":{"id":0}}]`
	screenrecClientsJSON  = `[{"address":"0x1","pid":10,"mapped":true,"hidden":false,"workspace":{"id":1},"at":[100,50],"size":[640,480]},{"address":"0x2","pid":11,"mapped":true,"hidden":false,"workspace":{"id":2},"at":[5,5],"size":[10,10]},{"address":"0x3","pid":12,"mapped":true,"hidden":false,"workspace":{"id":3},"at":[1,1],"size":[2,2]},{"address":"0x4","pid":13,"mapped":false,"hidden":false,"workspace":{"id":1},"at":[7,7],"size":[8,8]},{"address":"0x5","pid":14,"mapped":true,"hidden":true,"workspace":{"id":1},"at":[9,9],"size":[3,3]}]`
	screenrecPactlSource  = "0\talsa_input.pci\tmodule\ts16le\tRUNNING\n"
)

// screenrecStubLog is the shared stub prelude: log NAME ARGS... appends one
// \x1f-joined argv line to $SCREENREC_TEST_LOG/NAME.log.
const screenrecStubLog = `#!/bin/sh
log() { n=$1; shift; { printf '%s\037' "$@"; printf '\n'; } >> "$SCREENREC_TEST_LOG/$n.log"; }
`

// screenrecStubBodies are the stub scripts after the prelude. wf-recorder
// re-execs itself with SIGINT/SIGTERM at their defaults: the plugin starts it
// as a background job of a non-interactive sh, which ignores SIGINT, and a
// shell cannot trap a signal ignored on entry (the real binary installs its
// own handler).
var screenrecStubBodies = map[string]string{
	"wf-recorder": `if [ -z "${STUB_SIGRESET:-}" ]; then
	STUB_SIGRESET=1; export STUB_SIGRESET
	exec "$STUB_ENV" --default-signal=INT,TERM /bin/sh "$0" "$@"
fi
log wf-recorder "$@"
echo $$ >> "$SCREENREC_TEST_LOG/wf-recorder.pid"
out=""; prev=""
for a; do [ "$prev" = "-f" ] && out=$a; prev=$a; done
: > "$out"
trap 'printf video >> "$out"; echo INT >> "$SCREENREC_TEST_LOG/wf-recorder.events"; exit 0' INT
trap 'printf video >> "$out"; echo TERM >> "$SCREENREC_TEST_LOG/wf-recorder.events"; exit 0' TERM
while :; do sleep 0.1; done
`,
	"slurp": `log slurp "$@"
r=""; for a; do [ "$a" = "-r" ] && r=1; done
[ -n "$r" ] && cat > "$SCREENREC_TEST_LOG/slurp.stdin"
[ -n "${STUB_SLURP_CANCEL:-}" ] && exit 1
if [ -n "$r" ]; then head -n 1 "$SCREENREC_TEST_LOG/slurp.stdin"; else echo '10,20 300x200'; fi
`,
	"hyprctl": `log hyprctl "$@"
case "$*" in
*monitors*) cat "$STUB_HYPR_DIR/monitors.json" ;;
*clients*) cat "$STUB_HYPR_DIR/clients.json" ;;
*) exit 1 ;;
esac
`,
	"ffmpeg": `log ffmpeg "$@"
in=""; prev=""; last=""
for a; do [ "$prev" = "-i" ] && in=$a; prev=$a; last=$a; done
cp "$in" "$last"
`,
	"wl-copy": `log wl-copy "$@"
cat > "$SCREENREC_TEST_LOG/wl-copy.stdin"
`,
	"mpv":      `log mpv "$@"` + "\n",
	"xdg-open": `log xdg-open "$@"` + "\n",
	"pactl": `log pactl "$@"
[ "$*" = "list short sources" ] && printf '%s' "$STUB_PACTL_SOURCES"
exit 0
`,
}

// newScreenrecEnv replaces PATH with stubs plus the core allowlist, points
// HOME/XDG dirs at fresh temp dirs and registers a cleanup that kills any
// stub recorder the plugin left running (it is setsid'd, so the host's
// process-group kill never reaches it).
func newScreenrecEnv(t *testing.T, o screenrecStubOpts) *screenrecEnv {
	t.Helper()
	envBin, err := exec.LookPath("env")
	if err != nil {
		t.Skipf("env not on PATH: %v", err)
	}
	if err := exec.Command(envBin, "--default-signal=INT", "true").Run(); err != nil {
		t.Skipf("env --default-signal unsupported: %v", err)
	}
	coreBin := t.TempDir()
	for _, tool := range screenrecCoreTools {
		p, err := exec.LookPath(tool)
		if err != nil {
			t.Skipf("%s not on PATH: %v", tool, err)
		}
		if err := os.Symlink(p, filepath.Join(coreBin, tool)); err != nil {
			t.Fatal(err)
		}
	}
	stubBin := t.TempDir()
	for _, name := range screenrecStubNames {
		if slices.Contains(o.omit, name) {
			continue
		}
		body := screenrecStubLog + screenrecStubBodies[name]
		if err := os.WriteFile(filepath.Join(stubBin, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	hyprDir := t.TempDir()
	for name, data := range map[string]string{
		"monitors.json": screenrecMonitorsJSON,
		"clients.json":  screenrecClientsJSON,
	} {
		if err := os.WriteFile(filepath.Join(hyprDir, name), []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	home := t.TempDir()
	e := &screenrecEnv{
		logDir: t.TempDir(),
		runDir: t.TempDir(),
		outDir: filepath.Join(home, "Videos", "Recordings"),
	}
	e.state = filepath.Join(e.runDir, "banshee", "screenrec.state")
	e.partial = filepath.Join(e.outDir, ".partial")

	t.Setenv("PATH", stubBin+":"+coreBin)
	t.Setenv("HOME", home)
	t.Setenv("XDG_RUNTIME_DIR", e.runDir)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_VIDEOS_DIR", "")
	os.Unsetenv("XDG_VIDEOS_DIR")
	t.Setenv("SCREENREC_TEST_LOG", e.logDir)
	t.Setenv("STUB_HYPR_DIR", hyprDir)
	t.Setenv("STUB_ENV", envBin)
	t.Setenv("STUB_SIGRESET", "")
	t.Setenv("STUB_PACTL_SOURCES", o.pactlSources)
	cancel := ""
	if o.slurpCancel {
		cancel = "1"
	}
	t.Setenv("STUB_SLURP_CANCEL", cancel)

	t.Cleanup(func() {
		data, _ := os.ReadFile(filepath.Join(e.logDir, "wf-recorder.pid"))
		for _, f := range strings.Fields(string(data)) {
			if pid, err := strconv.Atoi(f); err == nil && pid > 1 {
				// The stub is a session leader: take its sleep children too.
				_ = syscall.Kill(-pid, syscall.SIGKILL)
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	})
	return e
}

// invocations returns every logged argv of one stub, oldest first.
func (e *screenrecEnv) invocations(tool string) [][]string {
	data, err := os.ReadFile(filepath.Join(e.logDir, tool+".log"))
	if err != nil {
		return nil
	}
	var out [][]string
	for _, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		out = append(out, strings.Split(strings.TrimSuffix(line, "\x1f"), "\x1f"))
	}
	return out
}

// logFile returns a stub side file (stdin capture, events), empty if absent.
func (e *screenrecEnv) logFile(name string) string {
	data, _ := os.ReadFile(filepath.Join(e.logDir, name))
	return string(data)
}

// readState parses the plugin's key=value state file; nil when absent.
func (e *screenrecEnv) readState() map[string]string {
	data, err := os.ReadFile(e.state)
	if err != nil {
		return nil
	}
	m := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			m[k] = v
		}
	}
	return m
}

// stageScreenrec returns a plugin root holding the shipped plugin: symlinked
// when overrides is empty, else copied with overrides appended to the shipped
// config (the plugin has no env seam, so config is the only knob).
func stageScreenrec(t *testing.T, overrides string) string {
	t.Helper()
	src := screenrecDir(t)
	root := t.TempDir()
	staged := filepath.Join(root, "screenrec")
	if overrides == "" {
		if err := os.Symlink(src, staged); err != nil {
			t.Skipf("cannot stage screenrec plugin: %v", err)
		}
		return root
	}
	if err := os.MkdirAll(staged, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []struct {
		name string
		mode os.FileMode
	}{{"manifest.json", 0o644}, {"plugin.sh", 0o755}} {
		data, err := os.ReadFile(filepath.Join(src, f.name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(staged, f.name), data, f.mode); err != nil {
			t.Fatal(err)
		}
	}
	conf, err := os.ReadFile(filepath.Join(src, "config"))
	if err != nil {
		t.Fatal(err)
	}
	conf = append(conf, "\n"+overrides+"\n"...)
	if err := os.WriteFile(filepath.Join(staged, "config"), conf, 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

// screenrecNotify is one notify message the host handed to the sink.
type screenrecNotify struct {
	n       WireNotify
	respond func(action string, closed bool, reason int)
}

// screenrecHost is a loaded host running the screenrec plugin.
type screenrecHost struct {
	h    *Host
	prov providers.Provider
	sunk chan screenrecNotify
}

// startScreenrec loads the plugin root into a fresh host whose notify sink
// feeds a channel.
func startScreenrec(t *testing.T, root string) *screenrecHost {
	t.Helper()
	sunk := make(chan screenrecNotify, 16)
	h := NewHost(root, Options{
		Timeout: 2 * time.Second,
		Notify: func(pluginID string, n WireNotify, respond func(string, bool, int)) {
			if pluginID == "screenrec" {
				sunk <- screenrecNotify{n, respond}
			}
		},
	})
	t.Cleanup(h.Shutdown)
	if err := h.Load(); err != nil {
		t.Fatal(err)
	}
	provs := h.Providers()
	if len(provs) != 1 {
		t.Fatalf("providers = %+v, want the screenrec plugin", provs)
	}
	return &screenrecHost{h: h, prov: provs[0], sunk: sunk}
}

// queryUntil repeats `rec <sub>` until ok accepts the rows: a query can time
// out empty while the plugin starts or sits in a synchronous picker.
func (s *screenrecHost) queryUntil(t *testing.T, sub, what string, ok func([]providers.Result) bool) []providers.Result {
	t.Helper()
	var rows []providers.Result
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var err error
		rows, err = s.prov.Query(context.Background(), strings.TrimSpace("rec "+sub))
		if err != nil {
			t.Fatal(err)
		}
		if ok(rows) {
			return rows
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("query %q: timed out waiting for %s; last rows = %s", sub, what, screenrecIDs(rows))
	return nil
}

// query returns the first non-empty answer to `rec <sub>`.
func (s *screenrecHost) query(t *testing.T, sub string) []providers.Result {
	t.Helper()
	return s.queryUntil(t, sub, "any rows", func(r []providers.Result) bool { return len(r) > 0 })
}

// next returns the next sunk notify or fails.
func (s *screenrecHost) next(t *testing.T, what string) screenrecNotify {
	t.Helper()
	select {
	case n := <-s.sunk:
		return n
	case <-time.After(5 * time.Second):
		t.Fatalf("no notify for %s", what)
		return screenrecNotify{}
	}
}

// quiet asserts no notify arrives for d.
func (s *screenrecHost) quiet(t *testing.T, d time.Duration) {
	t.Helper()
	select {
	case n := <-s.sunk:
		t.Fatalf("unexpected notify %+v", n.n)
	case <-time.After(d):
	}
}

// screenrecIDs lists result ids with the plugin prefix trimmed.
func screenrecIDs(rows []providers.Result) []string {
	ids := make([]string, len(rows))
	for i, r := range rows {
		ids[i] = strings.TrimPrefix(r.ID, "plugin:screenrec:")
	}
	return ids
}

// argAfter returns the argument following flag, or "" when flag is absent.
func argAfter(argv []string, flag string) string {
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == flag {
			return argv[i+1]
		}
	}
	return ""
}

// seedFile writes a file (and its parents) with a fixed mtime.
func seedFile(t *testing.T, path string, mtime time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

// dirNames lists a directory's entries; nil when it is missing.
func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	return names
}

var screenrecPartialRe = regexp.MustCompile(`^screenrec-\d{8}-\d{6}\.mp4$`)

// startRecording submits a record row and waits until the plugin reports the
// recording (state file + "Recording…" notify), returning both.
func (s *screenrecHost) startRecording(t *testing.T, e *screenrecEnv, id string, values map[string]string) (map[string]string, screenrecNotify) {
	t.Helper()
	s.query(t, "") // starts the plugin
	if err := s.h.Submit("screenrec", id, values); err != nil {
		t.Fatal(err)
	}
	var st map[string]string
	eventually(t, 5*time.Second, "state phase=recording", func() bool {
		st = e.readState()
		return st["phase"] == "recording"
	})
	n := s.next(t, "recording start")
	if n.n.Summary != "Recording…" {
		t.Fatalf("first notify = %+v, want Recording…", n.n)
	}
	return st, n
}

// assertSaved checks the finished recording on disk, clipboard, player,
// notify and state, then that it tops the recent rows.
func assertSaved(t *testing.T, s *screenrecHost, e *screenrecEnv, final string, saved WireNotify, timeoutMS int) {
	t.Helper()
	if saved.ID != "screenrec:rec" || saved.RequireInput || saved.TimeoutMS != timeoutMS {
		t.Errorf("saved notify = %+v, want id screenrec:rec, timeout %d", saved, timeoutMS)
	}
	if want := []WireNotifyAction{{Key: "default", Label: "Open folder"}}; !reflect.DeepEqual(saved.Actions, want) {
		t.Errorf("saved actions = %+v, want %+v", saved.Actions, want)
	}
	if !strings.Contains(saved.Body, filepath.Base(final)) || !strings.Contains(saved.Body, "copied to clipboard") {
		t.Errorf("saved body = %q, want basename and copied to clipboard", saved.Body)
	}
	if _, err := os.Stat(final); err != nil {
		t.Errorf("final file: %v", err)
	}
	if left := dirNames(t, e.partial); len(left) != 0 {
		t.Errorf(".partial = %v, want empty", left)
	}
	eventually(t, 5*time.Second, "state cleared", func() bool {
		_, err := os.Stat(e.state)
		return os.IsNotExist(err)
	})
	eventually(t, 5*time.Second, "wl-copy and mpv", func() bool {
		return len(e.invocations("wl-copy")) > 0 && len(e.invocations("mpv")) > 0 &&
			e.logFile("wl-copy.stdin") != ""
	})
	wl := e.invocations("wl-copy")[0]
	if argAfter(wl, "-t") != "text/uri-list" {
		t.Errorf("wl-copy argv = %q, want -t text/uri-list", wl)
	}
	if got, want := e.logFile("wl-copy.stdin"), "file://"+final+"\n"; got != want {
		t.Errorf("wl-copy stdin = %q, want %q", got, want)
	}
	mpv := e.invocations("mpv")[0]
	if !slices.Contains(mpv, "--loop=inf") || !slices.Contains(mpv, "--really-quiet") ||
		len(mpv) < 2 || mpv[len(mpv)-1] != final || mpv[len(mpv)-2] != "--" {
		t.Errorf("mpv argv = %q, want --loop=inf --really-quiet -- %s", mpv, final)
	}
	rows := s.queryUntil(t, "", "idle rows", func(r []providers.Result) bool {
		return len(r) > 0 && r[0].ID == "plugin:screenrec:rec:region"
	})
	for _, r := range rows {
		if strings.HasPrefix(r.ID, "plugin:screenrec:recent:") {
			if want := "plugin:screenrec:recent:" + filepath.Base(final); r.ID != want {
				t.Errorf("first recent row = %q, want %q", r.ID, want)
			}
			return
		}
	}
	t.Errorf("no recent row after save: %v", screenrecIDs(rows))
}

// TestScreenrecMissingDeps proves a missing required tool collapses the
// plugin to a single install-hint row naming it.
func TestScreenrecMissingDeps(t *testing.T) {
	for _, tc := range []struct {
		name     string
		omit     []string
		wantPkgs []string
	}{
		{"ffmpeg", []string{"ffmpeg"}, []string{"ffmpeg"}},
		{"recorder and picker", []string{"wf-recorder", "slurp"}, []string{"wf-recorder", "slurp"}},
		{"mapped packages", []string{"wl-copy", "hyprctl"}, []string{"wl-clipboard", "hyprland"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			newScreenrecEnv(t, screenrecStubOpts{omit: tc.omit, pactlSources: screenrecPactlSource})
			s := startScreenrec(t, stageScreenrec(t, ""))
			rows := s.query(t, "")
			if len(rows) != 1 || rows[0].ID != "plugin:screenrec:deps" {
				t.Fatalf("rows = %v, want only deps", screenrecIDs(rows))
			}
			r := rows[0]
			if !strings.HasPrefix(r.Title, "screenrec: install ") {
				t.Errorf("title = %q", r.Title)
			}
			for _, tool := range tc.omit {
				if !strings.Contains(r.Title, tool) {
					t.Errorf("title %q does not name %s", r.Title, tool)
				}
			}
			if !strings.Contains(r.Subtitle, "pacman -S") {
				t.Errorf("subtitle = %q, want a pacman hint", r.Subtitle)
			}
			for _, pkg := range tc.wantPkgs {
				if !strings.Contains(r.Subtitle, pkg) {
					t.Errorf("subtitle %q does not name package %s", r.Subtitle, pkg)
				}
			}
			if r.Action.Kind != providers.ActPluginCallback || r.Form != nil {
				t.Errorf("deps row action = %q form = %+v, want callback, no form", r.Action.Kind, r.Form)
			}
		})
	}
}

// TestScreenrecIdleRows proves the idle row set, its order, forms, and the
// remainder filter, and that only prefixed mp4/gif files count as recordings.
func TestScreenrecIdleRows(t *testing.T) {
	for _, tc := range []struct {
		name string
		sub  string
		want []string
	}{
		{"all", "", []string{"rec:region", "rec:window", "rec:screen", "clear",
			"recent:screenrec-20260102-000000.gif", "recent:screenrec-20260101-000000.mp4"}},
		{"window", "win", []string{"rec:window"}},
		{"case-insensitive", "REGION", []string{"rec:region"}},
		{"clear", "clear", []string{"clear"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newScreenrecEnv(t, screenrecStubOpts{pactlSources: screenrecPactlSource})
			old := time.Now().Add(-2 * time.Hour)
			seedFile(t, filepath.Join(e.outDir, "screenrec-20260101-000000.mp4"), old)
			seedFile(t, filepath.Join(e.outDir, "screenrec-20260102-000000.gif"), old.Add(time.Hour))
			for _, decoy := range []string{"other.mp4", "screenrec-x.mkv", ".partial/x.mp4"} {
				seedFile(t, filepath.Join(e.outDir, decoy), time.Now())
			}
			s := startScreenrec(t, stageScreenrec(t, ""))
			rows := s.query(t, tc.sub)
			if got := screenrecIDs(rows); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("rows = %v, want %v", got, tc.want)
			}
			titles := map[string]string{
				"rec:region": "Record region", "rec:window": "Record window", "rec:screen": "Record screen",
			}
			scores := map[string]int{"rec:region": 100, "rec:window": 95, "rec:screen": 90, "clear": 80}
			recent := 0
			for _, r := range rows {
				id := strings.TrimPrefix(r.ID, "plugin:screenrec:")
				switch {
				case strings.HasPrefix(id, "rec:"):
					if r.Title != titles[id] || r.Score != scores[id] {
						t.Errorf("%s = (%q, %d)", id, r.Title, r.Score)
					}
					f := r.Form
					if f == nil || len(f.Fields) != 2 ||
						f.Fields[0].Key != "format" || f.Fields[0].Label != "Format" ||
						!reflect.DeepEqual(f.Fields[0].Options, []string{"MP4", "GIF"}) ||
						f.Fields[1].Key != "audio" || f.Fields[1].Label != "Audio" ||
						!reflect.DeepEqual(f.Fields[1].Options, []string{"Off", "On"}) ||
						f.SubmitLabel != "Start Recording" {
						t.Errorf("%s form = %+v, want format [MP4 GIF] then audio [Off On], submit \"Start Recording\"", id, f)
					}
				case id == "clear":
					if !strings.HasPrefix(r.Title, "Clear recordings (2 files, ") || r.Score != 80 {
						t.Errorf("clear row = (%q, %d)", r.Title, r.Score)
					}
					f := r.Form
					if f == nil || f.Title != fmt.Sprintf("Delete 2 recordings from %s?", e.outDir) ||
						len(f.Fields) != 1 || f.Fields[0].Key != "confirm" || !f.Fields[0].Required ||
						f.SubmitLabel != "Delete" {
						t.Errorf("clear form = %+v", f)
					}
				case strings.HasPrefix(id, "recent:"):
					if r.Title != strings.TrimPrefix(id, "recent:") || r.Score != 70-recent ||
						!strings.Contains(r.Subtitle, " · ") {
						t.Errorf("recent row %d = (%q, %q, %d)", recent, r.Title, r.Subtitle, r.Score)
					}
					recent++
				}
			}
		})
	}
}

// TestScreenrecPrefix proves the shipped manifest accepts any prefix of
// "record" from three letters on, and nothing shorter or longer.
func TestScreenrecPrefix(t *testing.T) {
	newScreenrecEnv(t, screenrecStubOpts{pactlSources: screenrecPactlSource})
	s := startScreenrec(t, stageScreenrec(t, ""))
	for _, tc := range []struct {
		name  string
		query string
		want  []string // nil means the gate rejects the query
	}{
		{"full prefix", "record", []string{"rec:region", "rec:window", "rec:screen"}},
		{"partial prefix", "reco", []string{"rec:region", "rec:window", "rec:screen"}},
		{"partial prefix with filter", "recor win", []string{"rec:window"}},
		{"below minimum", "re", nil},
		{"longer than prefix", "recording", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.want == nil {
				rows, err := s.prov.Query(context.Background(), tc.query)
				if err != nil || rows != nil {
					t.Fatalf("Query(%q) = (%v, %v), want (nil, nil)", tc.query, screenrecIDs(rows), err)
				}
				return
			}
			var rows []providers.Result
			deadline := time.Now().Add(5 * time.Second)
			for len(rows) == 0 && time.Now().Before(deadline) {
				var err error
				if rows, err = s.prov.Query(context.Background(), tc.query); err != nil {
					t.Fatal(err)
				}
			}
			if got := screenrecIDs(rows); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("Query(%q) rows = %v, want %v", tc.query, got, tc.want)
			}
		})
	}
}

// TestScreenrecFormatDefault proves DEFAULT_FORMAT picks which format the
// dropdown preselects (its first option).
func TestScreenrecFormatDefault(t *testing.T) {
	for _, tc := range []struct {
		name      string
		overrides string
		want      []string
	}{
		{"shipped", "", []string{"MP4", "GIF"}},
		{"gif", "DEFAULT_FORMAT=gif", []string{"GIF", "MP4"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			newScreenrecEnv(t, screenrecStubOpts{pactlSources: screenrecPactlSource})
			s := startScreenrec(t, stageScreenrec(t, tc.overrides))
			rows := s.query(t, "")
			for _, r := range rows[:3] {
				if r.Form == nil || len(r.Form.Fields) == 0 || !reflect.DeepEqual(r.Form.Fields[0].Options, tc.want) {
					t.Errorf("%s format field = %+v, want %v", r.ID, r.Form, tc.want)
				}
			}
		})
	}
}

// TestScreenrecAudioOptions proves the audio probe: a listed source or no
// probe tool at all offers Off/On, an empty source list disables audio.
func TestScreenrecAudioOptions(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts screenrecStubOpts
		want []string
	}{
		{"pactl source", screenrecStubOpts{pactlSources: screenrecPactlSource}, []string{"Off", "On"}},
		{"pactl empty", screenrecStubOpts{}, []string{"Unavailable (no audio source)"}},
		{"no probe tool", screenrecStubOpts{omit: []string{"pactl"}}, []string{"Off", "On"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			newScreenrecEnv(t, tc.opts)
			s := startScreenrec(t, stageScreenrec(t, ""))
			rows := s.query(t, "")
			for _, r := range rows[:3] {
				if r.Form == nil || len(r.Form.Fields) != 2 || !reflect.DeepEqual(r.Form.Fields[1].Options, tc.want) {
					t.Errorf("%s audio field = %+v, want %v", r.ID, r.Form, tc.want)
				}
			}
		})
	}
}

// TestScreenrecRecordLifecycle walks a region MP4 recording end to end:
// argv, state, the Recording… notify, the stop-only row, stop via the row,
// and the saved file handed to the clipboard and player.
func TestScreenrecRecordLifecycle(t *testing.T) {
	e := newScreenrecEnv(t, screenrecStubOpts{pactlSources: screenrecPactlSource})
	s := startScreenrec(t, stageScreenrec(t, ""))

	st, recN := s.startRecording(t, e, "rec:region", map[string]string{"format": "MP4", "audio": "On"})
	rec := recN.n

	inv := e.invocations("wf-recorder")
	if len(inv) != 1 {
		t.Fatalf("wf-recorder invocations = %q, want one", inv)
	}
	argv := inv[0]
	partial := argAfter(argv, "-f")
	if filepath.Dir(partial) != e.partial || !screenrecPartialRe.MatchString(filepath.Base(partial)) {
		t.Errorf("-f = %q, want %s/screenrec-YYYYMMDD-HHMMSS.mp4", partial, e.partial)
	}
	if !slices.Contains(argv, "-y") || argAfter(argv, "-g") != "10,20 300x200" ||
		!slices.Contains(argv, "--audio") || slices.Contains(argv, "-c") {
		t.Errorf("wf-recorder argv = %q, want -y, -g 10,20 300x200, --audio, no -c", argv)
	}

	name := strings.TrimSuffix(filepath.Base(partial), ".mp4")
	final := filepath.Join(e.outDir, name+".mp4")
	pids := strings.Fields(e.logFile("wf-recorder.pid"))
	if len(pids) != 1 || st["pid"] != pids[0] {
		t.Errorf("state pid = %q, stub pids = %v", st["pid"], pids)
	}
	if st["file"] != partial || st["final"] != final || st["format"] != "mp4" ||
		st["target"] != "region 10,20 300x200" {
		t.Errorf("state = %v", st)
	}
	if _, err := strconv.ParseInt(st["started"], 10, 64); err != nil {
		t.Errorf("state started = %q, want epoch", st["started"])
	}

	if rec.ID != "screenrec:rec" || !rec.RequireInput ||
		!reflect.DeepEqual(rec.Actions, []WireNotifyAction{{Key: "stop", Label: "Stop"}}) ||
		!strings.Contains(rec.Body, "region") || !strings.Contains(rec.Body, name) {
		t.Errorf("recording notify = %+v", rec)
	}

	rows := s.queryUntil(t, "anything", "stop row", func(r []providers.Result) bool {
		return len(r) > 0 && r[0].ID == "plugin:screenrec:stop"
	})
	if len(rows) != 1 || rows[0].Title != "Stop recording" ||
		!strings.Contains(rows[0].Subtitle, "started") || !strings.Contains(rows[0].Subtitle, name) {
		t.Fatalf("recording rows = %+v, want only the stop row", rows)
	}

	if err := s.h.Activate("screenrec", "stop"); err != nil {
		t.Fatal(err)
	}
	eventually(t, 5*time.Second, "wf-recorder INT", func() bool {
		return strings.Contains(e.logFile("wf-recorder.events"), "INT")
	})
	saved := s.next(t, "recording saved")
	if saved.n.Summary != "Recording saved" {
		t.Fatalf("notify after stop = %+v, want Recording saved", saved.n)
	}
	assertSaved(t, s, e, final, saved.n, 8000)
}

// untilSaved drains notifies up to "Recording saved", returning them in order.
func (s *screenrecHost) untilSaved(t *testing.T) []screenrecNotify {
	t.Helper()
	var got []screenrecNotify
	for {
		n := s.next(t, "Recording saved")
		got = append(got, n)
		if n.n.Summary == "Recording saved" {
			return got
		}
		if len(got) > 4 {
			t.Fatalf("no Recording saved among %d notifies, last %+v", len(got), n.n)
		}
	}
}

// TestScreenrecNotifyActions proves the notification buttons drive the same
// paths as the launcher: Stop ends the recording, and the saved
// notification's default action opens the output folder.
func TestScreenrecNotifyActions(t *testing.T) {
	e := newScreenrecEnv(t, screenrecStubOpts{pactlSources: screenrecPactlSource})
	s := startScreenrec(t, stageScreenrec(t, ""))
	st, rec := s.startRecording(t, e, "rec:region", map[string]string{"format": "MP4", "audio": "Off"})
	if slices.Contains(e.invocations("wf-recorder")[0], "--audio") {
		t.Error("audio Off still passed --audio")
	}

	rec.respond("stop", false, 0)
	eventually(t, 5*time.Second, "wf-recorder INT", func() bool {
		return strings.Contains(e.logFile("wf-recorder.events"), "INT")
	})
	saved := s.untilSaved(t)
	last := saved[len(saved)-1]
	assertSaved(t, s, e, st["final"], last.n, 8000)

	last.respond("default", false, 0)
	eventually(t, 5*time.Second, "xdg-open", func() bool { return len(e.invocations("xdg-open")) > 0 })
	if got := e.invocations("xdg-open")[0]; !reflect.DeepEqual(got, []string{e.outDir}) {
		t.Errorf("xdg-open argv = %q, want [%s]", got, e.outDir)
	}
}

// TestScreenrecGif proves the GIF pipeline: no audio capture, a palette
// ffmpeg pass (optionally scaled) on stop, the mp4 removed and the gif handed
// on, with a Converting notify between start and save.
func TestScreenrecGif(t *testing.T) {
	for _, tc := range []struct {
		name      string
		overrides string
		wantScale string
	}{
		{"native width", "", ""},
		{"scaled", "GIF_WIDTH=800", "scale=800:-1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newScreenrecEnv(t, screenrecStubOpts{pactlSources: screenrecPactlSource})
			s := startScreenrec(t, stageScreenrec(t, tc.overrides))
			st, rec := s.startRecording(t, e, "rec:region", map[string]string{"format": "GIF", "audio": "On"})
			if argv := e.invocations("wf-recorder")[0]; slices.Contains(argv, "--audio") {
				t.Errorf("gif argv = %q, want no --audio", argv)
			}
			partial, final := st["file"], st["final"]
			if st["format"] != "gif" || !strings.HasSuffix(final, ".gif") || filepath.Dir(final) != e.outDir {
				t.Errorf("state = %v, want format=gif and a .gif final in %s", st, e.outDir)
			}
			if !strings.Contains(rec.n.Body, strings.TrimSuffix(filepath.Base(final), ".gif")) {
				t.Errorf("recording body = %q", rec.n.Body)
			}

			if err := s.h.Activate("screenrec", "stop"); err != nil {
				t.Fatal(err)
			}
			seq := s.untilSaved(t)
			var sums []string
			for _, n := range seq {
				sums = append(sums, n.n.Summary)
			}
			if want := []string{"Converting to GIF…", "Recording saved"}; !reflect.DeepEqual(sums, want) {
				t.Errorf("notifies after stop = %q, want %q", sums, want)
			}

			ff := e.invocations("ffmpeg")
			if len(ff) != 1 {
				t.Fatalf("ffmpeg invocations = %q, want one", ff)
			}
			vf := strings.Join(ff[0], " ")
			for _, want := range []string{"fps=15", "palettegen", "paletteuse"} {
				if !strings.Contains(vf, want) {
					t.Errorf("ffmpeg argv %q lacks %s", ff[0], want)
				}
			}
			if tc.wantScale == "" && strings.Contains(vf, "scale=") {
				t.Errorf("ffmpeg argv %q scales at native width", ff[0])
			}
			if tc.wantScale != "" && !strings.Contains(vf, tc.wantScale) {
				t.Errorf("ffmpeg argv %q lacks %s", ff[0], tc.wantScale)
			}
			if _, err := os.Stat(partial); !os.IsNotExist(err) {
				t.Errorf("partial mp4 still present: %v", err)
			}
			assertSaved(t, s, e, final, seq[len(seq)-1].n, 8000)
		})
	}
}

// TestScreenrecWindowAndScreen proves window mode offers slurp only the
// visible mapped windows and records the pick, and screen mode records the
// focused output without any picker.
func TestScreenrecWindowAndScreen(t *testing.T) {
	for _, tc := range []struct {
		name       string
		id         string
		wantSlurp  string // expected slurp stdin; "" means slurp never runs
		wantGeom   string
		wantOutput string
		wantTarget string
	}{
		{"window", "rec:window", "100,50 640x480\n5,5 10x10\n", "100,50 640x480", "", "window 100,50 640x480"},
		{"screen", "rec:screen", "", "", "DP-1", "screen DP-1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newScreenrecEnv(t, screenrecStubOpts{pactlSources: screenrecPactlSource})
			s := startScreenrec(t, stageScreenrec(t, ""))
			st, _ := s.startRecording(t, e, tc.id, map[string]string{"format": "MP4", "audio": "Off"})
			argv := e.invocations("wf-recorder")[0]
			if argAfter(argv, "-g") != tc.wantGeom || argAfter(argv, "-o") != tc.wantOutput {
				t.Errorf("wf-recorder argv = %q, want -g %q -o %q", argv, tc.wantGeom, tc.wantOutput)
			}
			if tc.wantGeom == "" && slices.Contains(argv, "-g") {
				t.Errorf("screen argv = %q carries -g", argv)
			}
			if st["target"] != tc.wantTarget {
				t.Errorf("state target = %q, want %q", st["target"], tc.wantTarget)
			}
			slurp := e.invocations("slurp")
			if tc.wantSlurp == "" {
				if len(slurp) != 0 {
					t.Errorf("slurp ran in screen mode: %q", slurp)
				}
				return
			}
			if len(slurp) != 1 || !slices.Contains(slurp[0], "-r") {
				t.Errorf("slurp invocations = %q, want one with -r", slurp)
			}
			if got := e.logFile("slurp.stdin"); got != tc.wantSlurp {
				t.Errorf("slurp stdin = %q, want %q", got, tc.wantSlurp)
			}
		})
	}
}

// TestScreenrecSlurpCancel proves an aborted pick starts nothing: no
// recorder, no state and no notification.
func TestScreenrecSlurpCancel(t *testing.T) {
	for _, tc := range []struct {
		name string
		id   string
	}{
		{"region", "rec:region"},
		{"window", "rec:window"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newScreenrecEnv(t, screenrecStubOpts{pactlSources: screenrecPactlSource, slurpCancel: true})
			s := startScreenrec(t, stageScreenrec(t, ""))
			s.query(t, "")
			if err := s.h.Submit("screenrec", tc.id, map[string]string{"format": "MP4", "audio": "Off"}); err != nil {
				t.Fatal(err)
			}
			eventually(t, 5*time.Second, "slurp to run", func() bool { return len(e.invocations("slurp")) > 0 })
			s.quiet(t, 300*time.Millisecond)
			if inv := e.invocations("wf-recorder"); len(inv) != 0 {
				t.Errorf("wf-recorder ran after cancel: %q", inv)
			}
			if st := e.readState(); st != nil {
				t.Errorf("state after cancel = %v", st)
			}
		})
	}
}

// TestScreenrecConfigOverrides proves the config knobs reach the recorder
// argv and switch off playback, clipboard and the default notify timeout.
func TestScreenrecConfigOverrides(t *testing.T) {
	e := newScreenrecEnv(t, screenrecStubOpts{pactlSources: screenrecPactlSource})
	s := startScreenrec(t, stageScreenrec(t, `CODEC=libx264
AUTOPLAY=false
COPY_TO_CLIPBOARD=false
WF_RECORDER_ARGS="-r 30"
NOTIFY_TIMEOUT_SECONDS=2`))
	st, _ := s.startRecording(t, e, "rec:region", map[string]string{"format": "MP4", "audio": "Off"})
	argv := e.invocations("wf-recorder")[0]
	if argAfter(argv, "-c") != "libx264" || argAfter(argv, "-r") != "30" {
		t.Errorf("wf-recorder argv = %q, want -c libx264 and -r 30", argv)
	}
	if err := s.h.Activate("screenrec", "stop"); err != nil {
		t.Fatal(err)
	}
	seq := s.untilSaved(t)
	saved := seq[len(seq)-1].n
	if strings.Contains(saved.Body, "copied") || saved.TimeoutMS != 2000 {
		t.Errorf("saved notify = %+v, want no clipboard mention and timeout 2000", saved)
	}
	if _, err := os.Stat(st["final"]); err != nil {
		t.Errorf("final file: %v", err)
	}
	time.Sleep(200 * time.Millisecond)
	if mpv, wl := e.invocations("mpv"), e.invocations("wl-copy"); len(mpv) != 0 || len(wl) != 0 {
		t.Errorf("mpv = %q wl-copy = %q, want neither with AUTOPLAY/COPY_TO_CLIPBOARD off", mpv, wl)
	}
}

// screenrecSeedClear seeds two recordings plus decoys the clear must spare.
func screenrecSeedClear(t *testing.T, e *screenrecEnv) (recordings, decoys []string) {
	t.Helper()
	for _, n := range []string{"screenrec-a.mp4", "screenrec-b.gif"} {
		recordings = append(recordings, filepath.Join(e.outDir, n))
	}
	for _, n := range []string{"other.mp4", "screenrec-x.mkv", ".partial/x.mp4"} {
		decoys = append(decoys, filepath.Join(e.outDir, n))
	}
	for _, p := range append(slices.Clone(recordings), decoys...) {
		seedFile(t, p, time.Now().Add(-time.Minute))
	}
	return recordings, decoys
}

// TestScreenrecClear proves clear deletes only prefixed recordings, only on a
// case-insensitive "delete", and never while a recording runs.
func TestScreenrecClear(t *testing.T) {
	for _, tc := range []struct {
		name        string
		confirm     string
		wantDeleted bool
		wantSummary string
	}{
		{"wrong word", "nope", false, "Nothing deleted"},
		{"upper case", "DELETE", true, "Deleted 2 recordings"},
		{"lower case", "delete", true, "Deleted 2 recordings"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newScreenrecEnv(t, screenrecStubOpts{pactlSources: screenrecPactlSource})
			recs, decoys := screenrecSeedClear(t, e)
			s := startScreenrec(t, stageScreenrec(t, ""))
			s.query(t, "")
			if err := s.h.Submit("screenrec", "clear", map[string]string{"confirm": tc.confirm}); err != nil {
				t.Fatal(err)
			}
			n := s.next(t, "clear result")
			if n.n.ID != "screenrec:info" || n.n.Summary != tc.wantSummary {
				t.Errorf("notify = %+v, want screenrec:info %q", n.n, tc.wantSummary)
			}
			for _, p := range recs {
				if _, err := os.Stat(p); os.IsNotExist(err) != tc.wantDeleted {
					t.Errorf("%s: stat err = %v, want deleted=%v", filepath.Base(p), err, tc.wantDeleted)
				}
			}
			for _, p := range decoys {
				if _, err := os.Stat(p); err != nil {
					t.Errorf("decoy %s: %v", p, err)
				}
			}
		})
	}

	t.Run("while recording", func(t *testing.T) {
		e := newScreenrecEnv(t, screenrecStubOpts{pactlSources: screenrecPactlSource})
		recs, _ := screenrecSeedClear(t, e)
		s := startScreenrec(t, stageScreenrec(t, ""))
		s.startRecording(t, e, "rec:region", map[string]string{"format": "MP4", "audio": "Off"})
		rows := s.queryUntil(t, "clear", "stop row", func(r []providers.Result) bool {
			return len(r) > 0 && r[0].ID == "plugin:screenrec:stop"
		})
		if got := screenrecIDs(rows); !reflect.DeepEqual(got, []string{"stop"}) {
			t.Errorf("rows while recording = %v, want only stop", got)
		}
		if err := s.h.Submit("screenrec", "clear", map[string]string{"confirm": "delete"}); err != nil {
			t.Fatal(err)
		}
		n := s.next(t, "clear refusal")
		if n.n.ID != "screenrec:info" || n.n.Summary != "Stop the recording first" {
			t.Errorf("notify = %+v, want the refusal", n.n)
		}
		for _, p := range recs {
			if _, err := os.Stat(p); err != nil {
				t.Errorf("%s deleted while recording: %v", filepath.Base(p), err)
			}
		}
	})
}

// TestScreenrecStalePid proves a state file whose pid is gone is recovered on
// startup: an interrupted recording's partial is discarded with a notice, a
// stale finishing phase is dropped silently.
func TestScreenrecStalePid(t *testing.T) {
	for _, tc := range []struct {
		name        string
		phase       string
		wantSummary string // "" means no notify
	}{
		{"recording", "recording", "Recording lost"},
		{"finishing", "finishing", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newScreenrecEnv(t, screenrecStubOpts{pactlSources: screenrecPactlSource})
			partial := filepath.Join(e.partial, "screenrec-stale.mp4")
			seedFile(t, partial, time.Now())
			if err := os.MkdirAll(filepath.Dir(e.state), 0o755); err != nil {
				t.Fatal(err)
			}
			// 999999999 exceeds any pid_max, so kill -0 always fails.
			state := fmt.Sprintf("phase=%s\npid=999999999\nfile=%s\nfinal=%s\nformat=mp4\ntarget=region 1,1 2x2\nstarted=1\n",
				tc.phase, partial, filepath.Join(e.outDir, "screenrec-stale.mp4"))
			if err := os.WriteFile(e.state, []byte(state), 0o644); err != nil {
				t.Fatal(err)
			}
			before := dirNames(t, e.outDir)

			s := startScreenrec(t, stageScreenrec(t, ""))
			rows := s.query(t, "")
			if len(rows) == 0 || rows[0].ID != "plugin:screenrec:rec:region" {
				t.Errorf("rows = %v, want idle rows", screenrecIDs(rows))
			}
			if _, err := os.Stat(e.state); !os.IsNotExist(err) {
				t.Errorf("stale state kept: %v", err)
			}
			if after := dirNames(t, e.outDir); !reflect.DeepEqual(after, before) {
				t.Errorf("output dir = %v, want unchanged %v", after, before)
			}
			if tc.wantSummary == "" {
				s.quiet(t, 300*time.Millisecond)
				return
			}
			n := s.next(t, "stale recording")
			if n.n.ID != "screenrec:info" || n.n.Summary != tc.wantSummary {
				t.Errorf("notify = %+v, want screenrec:info %q", n.n, tc.wantSummary)
			}
			if left := dirNames(t, e.partial); len(left) != 0 {
				t.Errorf(".partial = %v, want the stale partial deleted", left)
			}
		})
	}
}

// TestScreenrecRecentActivate proves Enter on a recent row replays and copies
// that file silently, and that ids naming no valid recording do nothing.
func TestScreenrecRecentActivate(t *testing.T) {
	const name = "screenrec-20260101-000000.mp4"
	for _, tc := range []struct {
		name string
		id   string
		want bool
	}{
		{"existing", "recent:" + name, true},
		{"missing", "recent:screenrec-19990101-000000.mp4", false},
		{"traversal", "recent:../Recordings/" + name, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newScreenrecEnv(t, screenrecStubOpts{pactlSources: screenrecPactlSource})
			path := filepath.Join(e.outDir, name)
			seedFile(t, path, time.Now().Add(-time.Minute))
			s := startScreenrec(t, stageScreenrec(t, ""))
			s.query(t, "")
			if err := s.h.Activate("screenrec", tc.id); err != nil {
				t.Fatal(err)
			}
			if !tc.want {
				s.quiet(t, 300*time.Millisecond)
				if mpv, wl := e.invocations("mpv"), e.invocations("wl-copy"); len(mpv) != 0 || len(wl) != 0 {
					t.Errorf("mpv = %q wl-copy = %q, want neither", mpv, wl)
				}
				return
			}
			eventually(t, 5*time.Second, "mpv and wl-copy", func() bool {
				return len(e.invocations("mpv")) > 0 && e.logFile("wl-copy.stdin") != ""
			})
			if mpv := e.invocations("mpv")[0]; mpv[len(mpv)-1] != path {
				t.Errorf("mpv argv = %q, want %s last", mpv, path)
			}
			if got, want := e.logFile("wl-copy.stdin"), "file://"+path+"\n"; got != want {
				t.Errorf("wl-copy stdin = %q, want %q", got, want)
			}
			s.quiet(t, 300*time.Millisecond)
		})
	}
}
