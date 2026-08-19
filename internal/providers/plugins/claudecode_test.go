package plugins

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// claudeCodeDir is the in-repo claude-code plugin shipped as
// plugins/claude-code.
func claudeCodeDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("..", "..", "..", "plugins", "claude-code"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, ManifestName)); err != nil {
		t.Skipf("claude-code plugin not present: %v", err)
	}
	return dir
}

// TestClaudeCodePlugin exercises the shipped plugin end to end: the host
// starts it in the background, hook.sh forwards a fake Claude Code hook event
// through the FIFO, and the notify message lands in the host's sink.
func TestClaudeCodePlugin(t *testing.T) {
	src := claudeCodeDir(t)
	runtimeDir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)

	root := t.TempDir()
	if err := os.Symlink(src, filepath.Join(root, "claude-code")); err != nil {
		t.Skipf("cannot stage claude-code plugin: %v", err)
	}

	sunk := make(chan WireNotify, 4)
	h := NewHost(root, Options{
		Timeout: 2 * time.Second,
		Notify: func(pluginID string, n WireNotify, respond func(string, bool, int)) {
			if pluginID == "claude-code" {
				sunk <- n
			}
		},
	})
	t.Cleanup(h.Shutdown)
	if err := h.Load(); err != nil {
		t.Fatal(err)
	}

	// Background with no prefix: the plugin never appears in the query path.
	if provs := h.Providers(); len(provs) != 0 {
		t.Fatalf("providers = %+v, want none", provs)
	}

	h.StartBackground()
	fifo := filepath.Join(runtimeDir, "banshee", "claude-code.fifo")
	deadline := time.Now().Add(5 * time.Second)
	for {
		if fi, err := os.Stat(fifo); err == nil && fi.Mode()&os.ModeNamedPipe != 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("plugin never created its FIFO")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Deliver a hook event the way Claude Code would: JSON on hook.sh's stdin.
	hook := exec.Command(filepath.Join(src, "hook.sh"))
	hook.Stdin = strings.NewReader(
		`{"hook_event_name":"Notification","message":"needs permission","cwd":"/home/u/dev/x","session_id":"s1"}`)
	if out, err := hook.CombinedOutput(); err != nil {
		t.Fatalf("hook.sh: %v: %s", err, out)
	}

	select {
	case n := <-sunk:
		if n.ID != "claude:s1" {
			t.Errorf("id = %q, want claude:s1", n.ID)
		}
		if n.Summary != "Claude Code needs input" {
			t.Errorf("summary = %q", n.Summary)
		}
		if !strings.Contains(n.Body, "needs permission") || !strings.Contains(n.Body, "/home/u/dev/x") {
			t.Errorf("body = %q, want message and cwd", n.Body)
		}
		// The shipped config defaults to REQUIRE_INPUT=true.
		if !n.RequireInput {
			t.Error("require_input = false, want true")
		}
		if len(n.Actions) != 1 || n.Actions[0].Key != "default" {
			t.Errorf("actions = %+v, want the default Focus action", n.Actions)
		}
		// The shipped config defaults to SOUND_FILE="" — silent.
		if n.Sound != "" {
			t.Errorf("sound = %q, want empty by default", n.Sound)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no notify message reached the sink")
	}

	// Stop is filtered by the default EVENTS list (it fires on every turn
	// end, even while subagents still run), as are Notification events whose
	// notification_type is outside NOTIFY_TYPES. The FIFO is ordered, so the
	// passing events below arriving with the expected summaries proves the
	// dropped ones sent first never notified.
	for _, drop := range []string{
		`{"hook_event_name":"Stop","cwd":"/home/u/dev/x","session_id":"s1"}`,
		`{"hook_event_name":"Notification","notification_type":"idle_prompt","message":"waiting","session_id":"s1"}`,
		`{"hook_event_name":"Notification","notification_type":"permission_prompt","message":"needs permission","session_id":"s1"}`,
	} {
		hook = exec.Command(filepath.Join(src, "hook.sh"))
		hook.Stdin = strings.NewReader(drop)
		if out, err := hook.CombinedOutput(); err != nil {
			t.Fatalf("hook.sh: %v: %s", err, out)
		}
	}
	for _, tc := range []struct {
		payload string
		summary string
	}{
		{`{"hook_event_name":"Notification","notification_type":"agent_needs_input","message":"question","session_id":"s1"}`,
			"Claude Code needs input"},
		{`{"hook_event_name":"Notification","notification_type":"agent_completed","message":"done","session_id":"s1"}`,
			"Claude Code finished"},
		{`{"hook_event_name":"PermissionRequest","tool_name":"ExitPlanMode","session_id":"s1"}`,
			"Claude Code plan ready for review"},
		{`{"hook_event_name":"PermissionRequest","tool_name":"Bash","session_id":"s1"}`,
			"Claude Code awaiting approval"},
	} {
		hook = exec.Command(filepath.Join(src, "hook.sh"))
		hook.Stdin = strings.NewReader(tc.payload)
		if out, err := hook.CombinedOutput(); err != nil {
			t.Fatalf("hook.sh: %v: %s", err, out)
		}
		select {
		case n := <-sunk:
			if n.ID != "claude:s1" || n.Summary != tc.summary {
				t.Errorf("notify = %+v, want summary %q", n, tc.summary)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("no notify for %s", tc.payload)
		}
	}

	// An event outside EVENTS is filtered by the plugin.
	hook = exec.Command(filepath.Join(src, "hook.sh"))
	hook.Stdin = strings.NewReader(`{"hook_event_name":"PreToolUse","session_id":"s1"}`)
	if out, err := hook.CombinedOutput(); err != nil {
		t.Fatalf("hook.sh: %v: %s", err, out)
	}
	select {
	case n := <-sunk:
		t.Fatalf("filtered event produced a notify: %+v", n)
	case <-time.After(300 * time.Millisecond):
	}
}

// TestClaudeCodePluginSound stages a copy of the shipped plugin with a
// SOUND_FILE config and asserts the notify message carries the resolved path.
func TestClaudeCodePluginSound(t *testing.T) {
	src := claudeCodeDir(t)
	runtimeDir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)

	root := t.TempDir()
	staged := filepath.Join(root, "claude-code")
	if err := os.MkdirAll(staged, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []struct {
		name string
		mode os.FileMode
	}{
		{"manifest.json", 0o644},
		{"plugin.sh", 0o755},
		{"hook.sh", 0o755},
	} {
		data, err := os.ReadFile(filepath.Join(src, f.name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(staged, f.name), data, f.mode); err != nil {
			t.Fatal(err)
		}
	}
	// EVENTS opts Stop back in, covering the non-default path.
	if err := os.WriteFile(filepath.Join(staged, "config"),
		[]byte("SOUND_FILE=alert.wav\nEVENTS=\"Notification PermissionRequest Stop\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	sunk := make(chan WireNotify, 2)
	h := NewHost(root, Options{
		Timeout: 2 * time.Second,
		Notify: func(pluginID string, n WireNotify, respond func(string, bool, int)) {
			sunk <- n
		},
	})
	t.Cleanup(h.Shutdown)
	if err := h.Load(); err != nil {
		t.Fatal(err)
	}
	h.StartBackground()

	fifo := filepath.Join(runtimeDir, "banshee", "claude-code.fifo")
	deadline := time.Now().Add(5 * time.Second)
	for {
		if fi, err := os.Stat(fifo); err == nil && fi.Mode()&os.ModeNamedPipe != 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("plugin never created its FIFO")
		}
		time.Sleep(10 * time.Millisecond)
	}

	hook := exec.Command(filepath.Join(staged, "hook.sh"))
	hook.Stdin = strings.NewReader(
		`{"hook_event_name":"Notification","message":"m","session_id":"s2"}`)
	if out, err := hook.CombinedOutput(); err != nil {
		t.Fatalf("hook.sh: %v: %s", err, out)
	}
	select {
	case n := <-sunk:
		if want := filepath.Join(staged, "alert.wav"); n.Sound != want {
			t.Errorf("sound = %q, want %q", n.Sound, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no notify message reached the sink")
	}

	// With Stop opted back in via EVENTS, a Stop event notifies again.
	hook = exec.Command(filepath.Join(staged, "hook.sh"))
	hook.Stdin = strings.NewReader(
		`{"hook_event_name":"Stop","session_id":"s2"}`)
	if out, err := hook.CombinedOutput(); err != nil {
		t.Fatalf("hook.sh: %v: %s", err, out)
	}
	select {
	case n := <-sunk:
		if n.Summary != "Claude Code finished" {
			t.Errorf("stop notify = %+v", n)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no notify for the opted-in Stop event")
	}
}
