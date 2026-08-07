// Package config loads banshee's user configuration and knows every XDG path
// banshee uses. The banshee.conf "key = value" format is parsed exactly like
// the v0.3 shell implementation: blank lines and #-comments skipped, one
// optional space trimmed around key and value, unknown keys ignored (forward
// compatibility).
//
// The Config struct and path helpers are a frozen Phase-0 contract; Load is
// implemented in Phase 1.
package config

import (
	"os"
	"path/filepath"
	"strings"
)

// Version is the banshee release version.
const Version = "1.0.0"

// Config is the parsed banshee.conf plus defaults.
type Config struct {
	// v0.3 keys — semantics unchanged.
	SearchPaths   []string // comma-separated in conf; ~ expanded
	MaxDepth      int      // repo scan depth, default 5
	Keybind       string   // shell-plugin keybind, default ctrl-f
	FzfOpts       string   // extra fzf args for the CLI picker
	CacheTTL      int      // repo cache TTL seconds, default 300
	StartupPrompt bool     // shell startup restore prompt, default true

	// v1.0 launcher keys — all optional.
	Terminal      string  // empty → auto-detect ($TERMINAL, ghostty, kitty, alacritty, foot)
	LauncherWidth int     // default 640
	MaxResults    int     // default 30
	Accent        string  // CSS color, default #7aa2f7
	WindowOpacity float64 // panel alpha, default 0.86
	KeyboardMode  string  // "exclusive" (default) | "on-demand"
}

// Default returns a Config populated with defaults (no file read).
func Default() Config {
	return Config{
		SearchPaths:   []string{"~/dev", "~/projects", "~/src"},
		MaxDepth:      5,
		Keybind:       "ctrl-f",
		CacheTTL:      300,
		StartupPrompt: true,
		LauncherWidth: 640,
		MaxResults:    30,
		Accent:        "#7aa2f7",
		WindowOpacity: 0.86,
		KeyboardMode:  "exclusive",
	}
}

// ConfigDir returns ~/.config/banshee (honoring $XDG_CONFIG_HOME).
func ConfigDir() string {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "banshee")
	}
	return filepath.Join(homeDir(), ".config", "banshee")
}

// DataDir returns ~/.local/share/banshee (honoring $XDG_DATA_HOME).
func DataDir() string {
	if x := os.Getenv("XDG_DATA_HOME"); x != "" {
		return filepath.Join(x, "banshee")
	}
	return filepath.Join(homeDir(), ".local", "share", "banshee")
}

// StateDir returns ~/.local/state/banshee (honoring $XDG_STATE_HOME).
func StateDir() string {
	if x := os.Getenv("XDG_STATE_HOME"); x != "" {
		return filepath.Join(x, "banshee")
	}
	return filepath.Join(homeDir(), ".local", "state", "banshee")
}

// Well-known file and directory locations derived from the XDG dirs above.
func ConfPath() string       { return filepath.Join(ConfigDir(), "banshee.conf") }
func SessionsDir() string    { return filepath.Join(ConfigDir(), "sessions") }
func GroupsDir() string      { return filepath.Join(ConfigDir(), "groups") }
func PluginsDir() string     { return filepath.Join(ConfigDir(), "plugins") }
func RepoCachePath() string  { return filepath.Join(DataDir(), "repo_cache") }
func LastActionPath() string { return filepath.Join(DataDir(), "last_action") }
func DaemonLogPath() string  { return filepath.Join(StateDir(), "daemon.log") }

// ExpandPath expands a leading ~ or ~/ to the user's home directory.
func ExpandPath(p string) string {
	if p == "~" {
		return homeDir()
	}
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(homeDir(), p[2:])
	}
	return p
}

func homeDir() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return "/"
	}
	return h
}
