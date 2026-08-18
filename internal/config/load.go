package config

import (
	"bufio"
	"errors"
	"io/fs"
	"os"
	"strconv"
	"strings"
)

// Load reads ~/.config/banshee/banshee.conf (honoring $XDG_CONFIG_HOME) on top
// of Default. A missing file is not an error.
func Load() (Config, error) {
	return LoadFile(ConfPath())
}

// LoadFile parses one banshee.conf file on top of Default. A missing file
// yields the defaults and a nil error.
//
// The grammar is a byte-for-byte port of the v0.3 shell parser:
//
//   - leading/trailing spaces and tabs are stripped from every line
//     (bash `read -r line` with the default IFS),
//   - blank lines and lines starting with '#' are skipped,
//   - lines without '=' are skipped,
//   - the key is everything before the first '=', the value everything after,
//   - exactly one leading and one trailing space is trimmed from each
//     (so `key = value` works, `key  =  value` does not),
//   - unknown keys are ignored for forward compatibility.
//
// Values that fail to parse (a non-numeric max_depth, say) leave the default
// in place rather than failing the load, so a typo never bricks the launcher.
// SearchPaths are returned with a leading ~ already expanded.
func LoadFile(path string) (Config, error) {
	cfg := Default()
	cfg.SearchPaths = expandAll(cfg.SearchPaths)

	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return cfg, nil
		}
		return cfg, err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.Trim(sc.Text(), " \t")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		eq := strings.Index(line, "=")
		if eq < 0 {
			continue
		}
		key := trimOneSpace(line[:eq])
		value := trimOneSpace(line[eq+1:])
		applyKey(&cfg, key, value)
	}
	if err := sc.Err(); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// applyKey folds one parsed key/value pair into cfg. Unknown keys are ignored.
func applyKey(cfg *Config, key, value string) {
	switch key {
	case "search_paths":
		cfg.SearchPaths = expandAll(splitList(value))
	case "max_depth":
		setInt(&cfg.MaxDepth, value)
	case "keybind":
		cfg.Keybind = value
	case "fzf_opts":
		cfg.FzfOpts = value
	case "cache_ttl":
		setInt(&cfg.CacheTTL, value)
	case "startup_prompt":
		// Parity with the shell: anything other than "true" is false.
		cfg.StartupPrompt = value == "true"
	case "terminal":
		cfg.Terminal = value
	case "launcher_width":
		setInt(&cfg.LauncherWidth, value)
	case "max_results":
		setInt(&cfg.MaxResults, value)
	case "accent":
		cfg.Accent = value
	case "window_opacity":
		if f, err := strconv.ParseFloat(value, 64); err == nil {
			cfg.WindowOpacity = f
		}
	case "clipboard_history":
		// Parity with startup_prompt: anything other than "true" is false.
		cfg.ClipboardHistory = value == "true"
	case "notifications":
		// Parity with startup_prompt: anything other than "true" is false.
		cfg.Notifications = value == "true"
	case "keyboard_mode":
		switch value {
		case KeyboardModeExclusive, KeyboardModeOnDemand:
			cfg.KeyboardMode = value
		}
	}
}

// Keyboard modes accepted by the keyboard_mode key.
const (
	KeyboardModeExclusive = "exclusive"
	KeyboardModeOnDemand  = "on-demand"
)

// splitList splits a comma-separated conf value, trimming one space around
// each entry (shell parity) and dropping empties.
func splitList(value string) []string {
	var out []string
	for _, part := range strings.Split(value, ",") {
		part = trimOneSpace(part)
		if part == "" {
			continue
		}
		out = append(out, part)
	}
	return out
}

// trimOneSpace removes at most one leading and one trailing space, matching
// the shell's ${v## } / ${v%% } trimming.
func trimOneSpace(s string) string {
	s = strings.TrimPrefix(s, " ")
	s = strings.TrimSuffix(s, " ")
	return s
}

func expandAll(paths []string) []string {
	if paths == nil {
		return nil
	}
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		out = append(out, ExpandPath(p))
	}
	return out
}

func setInt(dst *int, value string) {
	if n, err := strconv.Atoi(strings.TrimSpace(value)); err == nil {
		*dst = n
	}
}

// EnsureDirs creates the config, sessions, groups, data and state directories.
// It is safe to call repeatedly.
func EnsureDirs() error {
	for _, d := range []string{ConfigDir(), SessionsDir(), GroupsDir(), DataDir(), StateDir()} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	return nil
}
