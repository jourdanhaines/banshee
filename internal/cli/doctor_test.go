package cli

import (
	"strings"
	"testing"
)

func TestDoctorReport(t *testing.T) {
	// Keep the ipc probe inside the sandbox: no daemon is listening there.
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	ta := newTestApp(t)
	ta.Run([]string{"doctor"}) // exit code depends on what is installed

	got := ta.out.String()
	for _, want := range []string{
		"Tools:",
		"tmux",
		"terminal",
		"Config:",
		"Index:",
		"Daemon:",
		"match:namespace = banshee",
		"$menu = banshee toggle",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("doctor output missing %q\ngot:\n%s", want, got)
		}
	}
}

func TestHyprlandSnippet(t *testing.T) {
	want := []string{
		"layerrule {",
		"    name = banshee",
		"    match:namespace = banshee",
		"    blur = on",
		"    ignore_alpha = 0",
		"}",
		"$menu = banshee toggle",
	}
	got := strings.Split(strings.TrimRight(HyprlandSnippet, "\n"), "\n")
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("snippet = %v, want %v", got, want)
	}
}
