package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// writeConf writes a temporary banshee.conf and returns its path.
func writeConf(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "banshee.conf")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadFileRepoFixture(t *testing.T) {
	t.Setenv("HOME", "/home/tester")

	cfg, err := LoadFile("../../contrib/banshee.conf")
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}

	want := Default()
	want.SearchPaths = []string{"/home/tester/dev", "/home/tester/projects", "/home/tester/src"}
	want.MaxDepth = 5
	want.Keybind = "ctrl-f"
	want.CacheTTL = 300
	want.StartupPrompt = true

	if !reflect.DeepEqual(cfg, want) {
		t.Errorf("banshee.conf parsed as\n %+v\nwant\n %+v", cfg, want)
	}
	if cfg.FzfOpts != "" {
		t.Errorf("commented-out fzf_opts leaked: %q", cfg.FzfOpts)
	}
}

func TestLoadFileMissingIsDefaults(t *testing.T) {
	t.Setenv("HOME", "/home/tester")
	cfg, err := LoadFile(filepath.Join(t.TempDir(), "nope.conf"))
	if err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
	want := []string{"/home/tester/dev", "/home/tester/projects", "/home/tester/src"}
	if !reflect.DeepEqual(cfg.SearchPaths, want) {
		t.Errorf("SearchPaths = %v, want %v", cfg.SearchPaths, want)
	}
	if cfg.MaxDepth != 5 || cfg.CacheTTL != 300 || !cfg.StartupPrompt {
		t.Errorf("defaults not applied: %+v", cfg)
	}
}

func TestLoadFileKeys(t *testing.T) {
	t.Setenv("HOME", "/home/tester")

	tests := []struct {
		name  string
		body  string
		check func(t *testing.T, c Config)
	}{
		{
			name: "comments and blanks are skipped",
			body: "# comment\n\n   \nmax_depth = 9\n",
			check: func(t *testing.T, c Config) {
				if c.MaxDepth != 9 {
					t.Errorf("MaxDepth = %d", c.MaxDepth)
				}
			},
		},
		{
			name: "lines without = are skipped",
			body: "garbage line\nmax_depth = 3\n",
			check: func(t *testing.T, c Config) {
				if c.MaxDepth != 3 {
					t.Errorf("MaxDepth = %d", c.MaxDepth)
				}
			},
		},
		{
			name: "unknown keys are ignored",
			body: "future_key = something\nmax_depth = 4\n",
			check: func(t *testing.T, c Config) {
				if c.MaxDepth != 4 {
					t.Errorf("MaxDepth = %d", c.MaxDepth)
				}
			},
		},
		{
			name: "value keeps inner spaces, one outer space trimmed",
			body: "fzf_opts = --color=16 --height=40%\n",
			check: func(t *testing.T, c Config) {
				if c.FzfOpts != "--color=16 --height=40%" {
					t.Errorf("FzfOpts = %q", c.FzfOpts)
				}
			},
		},
		{
			name: "no spaces around = still parses",
			body: "max_depth=7\n",
			check: func(t *testing.T, c Config) {
				if c.MaxDepth != 7 {
					t.Errorf("MaxDepth = %d", c.MaxDepth)
				}
			},
		},
		{
			name: "search_paths splits on comma and expands ~",
			body: "search_paths = ~/dev, /opt/code ,~\n",
			check: func(t *testing.T, c Config) {
				want := []string{"/home/tester/dev", "/opt/code", "/home/tester"}
				if !reflect.DeepEqual(c.SearchPaths, want) {
					t.Errorf("SearchPaths = %v, want %v", c.SearchPaths, want)
				}
			},
		},
		{
			name: "startup_prompt is only true for the literal true",
			body: "startup_prompt = false\n",
			check: func(t *testing.T, c Config) {
				if c.StartupPrompt {
					t.Error("StartupPrompt should be false")
				}
			},
		},
		{
			name: "startup_prompt garbage is false",
			body: "startup_prompt = yes\n",
			check: func(t *testing.T, c Config) {
				if c.StartupPrompt {
					t.Error("StartupPrompt should be false")
				}
			},
		},
		{
			name: "invalid numbers keep defaults",
			body: "max_depth = deep\ncache_ttl = soon\nwindow_opacity = opaque\n",
			check: func(t *testing.T, c Config) {
				if c.MaxDepth != 5 || c.CacheTTL != 300 || c.WindowOpacity != 0.92 {
					t.Errorf("defaults not preserved: %+v", c)
				}
			},
		},
		{
			name: "launcher keys",
			body: "terminal = ghostty\nlauncher_width = 800\nmax_results = 12\naccent = #ff0000\nwindow_opacity = 0.5\nkeyboard_mode = on-demand\n",
			check: func(t *testing.T, c Config) {
				if c.Terminal != "ghostty" || c.LauncherWidth != 800 || c.MaxResults != 12 ||
					c.Accent != "#ff0000" || c.WindowOpacity != 0.5 || c.KeyboardMode != "on-demand" {
					t.Errorf("launcher keys = %+v", c)
				}
			},
		},
		{
			name: "invalid keyboard_mode keeps default",
			body: "keyboard_mode = telepathy\n",
			check: func(t *testing.T, c Config) {
				if c.KeyboardMode != "exclusive" {
					t.Errorf("KeyboardMode = %q", c.KeyboardMode)
				}
			},
		},
		{
			name: "last assignment wins",
			body: "max_depth = 2\nmax_depth = 8\n",
			check: func(t *testing.T, c Config) {
				if c.MaxDepth != 8 {
					t.Errorf("MaxDepth = %d", c.MaxDepth)
				}
			},
		},
		{
			name: "value containing = keeps everything after the first",
			body: "fzf_opts = --bind=ctrl-a:select-all\n",
			check: func(t *testing.T, c Config) {
				if c.FzfOpts != "--bind=ctrl-a:select-all" {
					t.Errorf("FzfOpts = %q", c.FzfOpts)
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := LoadFile(writeConf(t, tc.body))
			if err != nil {
				t.Fatal(err)
			}
			tc.check(t, cfg)
		})
	}
}

func TestTrimOneSpace(t *testing.T) {
	tests := []struct{ in, want string }{
		{"key", "key"},
		{" key ", "key"},
		{"  key  ", " key "}, // only one space is trimmed, matching the shell
		{"", ""},
	}
	for _, tc := range tests {
		if got := trimOneSpace(tc.in); got != tc.want {
			t.Errorf("trimOneSpace(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestExpandPath(t *testing.T) {
	t.Setenv("HOME", "/home/tester")
	tests := []struct{ in, want string }{
		{"~", "/home/tester"},
		{"~/dev", "/home/tester/dev"},
		{"/abs", "/abs"},
		{"rel", "rel"},
		{"~notme", "~notme"},
	}
	for _, tc := range tests {
		if got := ExpandPath(tc.in); got != tc.want {
			t.Errorf("ExpandPath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestXDGPaths(t *testing.T) {
	t.Setenv("HOME", "/home/tester")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	if got := ConfPath(); got != "/home/tester/.config/banshee/banshee.conf" {
		t.Errorf("ConfPath = %q", got)
	}
	if got := RepoCachePath(); got != "/home/tester/.local/share/banshee/repo_cache" {
		t.Errorf("RepoCachePath = %q", got)
	}

	t.Setenv("XDG_CONFIG_HOME", "/xdg/config")
	if got := SessionsDir(); got != "/xdg/config/banshee/sessions" {
		t.Errorf("SessionsDir = %q", got)
	}
}
