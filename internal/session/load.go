package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// MigrationError reports a config still using the banshee 0.2.0 top-level
// "sessions" wrapper, which v0.3 replaced with one file per target.
type MigrationError struct {
	Path string
}

func (e *MigrationError) Error() string {
	return fmt.Sprintf("%s uses the 0.2.0 %q wrapper which is no longer supported; "+
		"split each session into its own ~/.config/banshee/sessions/<target>.json file (drop the wrapper)",
		e.Path, "sessions")
}

// SessionPath returns sessions/<target>.json inside dir.
func SessionPath(dir, target string) string { return filepath.Join(dir, target+".json") }

// GroupPath returns groups/<name>.json inside dir.
func GroupPath(dir, name string) string { return filepath.Join(dir, name+".json") }

// LoadSession reads and validates one sessions/<target>.json file.
func LoadSession(path string) (Session, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Session{}, err
	}
	return ParseSession(b, path)
}

// ParseSession validates raw session JSON. Validation mirrors the v0.3 shell
// validator case for case: legacy wrapper, "v" must be 1, non-empty "name",
// non-empty "windows", and every window needs a non-empty "panes" array.
// Unknown keys are ignored.
func ParseSession(b []byte, path string) (Session, error) {
	raw, err := decodeObject(b, path)
	if err != nil {
		return Session{}, err
	}
	if _, ok := raw["sessions"]; ok {
		return Session{}, &MigrationError{Path: path}
	}
	if err := checkVersion(raw, path); err != nil {
		return Session{}, err
	}
	if err := checkName(raw, path); err != nil {
		return Session{}, err
	}

	var windows []json.RawMessage
	if err := unmarshalKey(raw, "windows", &windows); err != nil || len(windows) == 0 {
		return Session{}, fmt.Errorf("%s %q must be a non-empty array", path, "windows")
	}
	for _, w := range windows {
		var probe struct {
			Panes []json.RawMessage `json:"panes"`
		}
		if err := json.Unmarshal(w, &probe); err != nil || len(probe.Panes) == 0 {
			return Session{}, fmt.Errorf("%s each window needs a non-empty %q array", path, "panes")
		}
	}

	var s Session
	if err := json.Unmarshal(b, &s); err != nil {
		return Session{}, fmt.Errorf("invalid JSON in %s: %w", path, err)
	}
	return s, nil
}

// LoadGroup reads and validates one groups/<name>.json file.
func LoadGroup(path string) (Group, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Group{}, err
	}
	return ParseGroup(b, path)
}

// ParseGroup validates raw group JSON: "v" must be 1, "name" non-empty and
// "targets" a non-empty array of non-empty strings.
func ParseGroup(b []byte, path string) (Group, error) {
	raw, err := decodeObject(b, path)
	if err != nil {
		return Group{}, err
	}
	if err := checkVersion(raw, path); err != nil {
		return Group{}, err
	}
	if err := checkName(raw, path); err != nil {
		return Group{}, err
	}
	var g Group
	if err := json.Unmarshal(b, &g); err != nil {
		return Group{}, targetsError(path)
	}
	if len(g.Targets) == 0 {
		return Group{}, targetsError(path)
	}
	for _, t := range g.Targets {
		if t == "" {
			return Group{}, targetsError(path)
		}
	}
	return g, nil
}

func targetsError(path string) error {
	return fmt.Errorf("%s %q must be a non-empty array of strings", path, "targets")
}

func decodeObject(b []byte, path string) (map[string]json.RawMessage, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("invalid JSON in %s: %w", path, err)
	}
	return raw, nil
}

func checkVersion(raw map[string]json.RawMessage, path string) error {
	var v int
	if err := unmarshalKey(raw, "v", &v); err != nil || v != SchemaVersion {
		return fmt.Errorf("%s missing or unsupported %q (must be %d)", path, "v", SchemaVersion)
	}
	return nil
}

func checkName(raw map[string]json.RawMessage, path string) error {
	var name string
	if err := unmarshalKey(raw, "name", &name); err != nil || name == "" {
		return fmt.Errorf("%s missing non-empty %q", path, "name")
	}
	return nil
}

func unmarshalKey(raw map[string]json.RawMessage, key string, dst any) error {
	v, ok := raw[key]
	if !ok {
		return fmt.Errorf("missing key %q", key)
	}
	return json.Unmarshal(v, dst)
}

// DefaultTemplate is the starter session config written for a new target,
// byte-identical to the v0.3 heredoc (placeholders included).
//
// target is JSON-encoded rather than interpolated raw: it comes from argv
// (`banshee -s <target>`) or from a repository basename, so a name containing
// a quote or a backslash would otherwise produce a config that cannot parse.
func DefaultTemplate(target string) []byte {
	return []byte(fmt.Sprintf(`{
  "v": 1,
  "name": %s,
  "windows": [
    {
      "name": "<window_name>",
      "panes": [
        { "run": "<target_command>" }
      ]
    }
  ]
}
`, jsonString(target)))
}

// DefaultGroupTemplate is the starter group config for name with the given
// targets, byte-identical to the v0.3 heredoc. name and targets are
// JSON-encoded, so no user-supplied name can produce invalid JSON.
func DefaultGroupTemplate(name string, targets []string) []byte {
	if targets == nil {
		targets = []string{}
	}
	arr, err := json.Marshal(targets)
	if err != nil { // targets are plain strings; cannot fail
		arr = []byte("[]")
	}
	return []byte(fmt.Sprintf(`{
  "v": 1,
  "name": %s,
  "targets": %s
}
`, jsonString(name), arr))
}

// jsonString renders s as a JSON string literal, quotes included. Plain names
// come out exactly as `"name"` did before, so the templates stay byte-identical
// to the v0.3 heredocs for every name a user is likely to pick.
func jsonString(s string) string {
	b, err := json.Marshal(s)
	if err != nil { // a Go string is always encodable
		return `""`
	}
	return string(b)
}

// WriteTemplate creates sessions/<target>.json from DefaultTemplate,
// creating dir if needed. It reports whether the file was created (false when
// it already existed).
func WriteTemplate(dir, target string) (bool, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false, err
	}
	path := SessionPath(dir, target)
	if _, err := os.Stat(path); err == nil {
		return false, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return false, err
	}
	if err := os.WriteFile(path, DefaultTemplate(target), 0o644); err != nil {
		return false, err
	}
	return true, nil
}

// WriteGroup validates a group config for name and targets and only then
// writes groups/<name>.json.
//
// The order matters: validating after the write would leave a permanently
// broken file on disk that `banshee -l` reports as "(invalid group config)"
// forever, for an input the user can fix by simply answering the prompt again.
func WriteGroup(dir, name string, targets []string) error {
	path := GroupPath(dir, name)
	b := DefaultGroupTemplate(name, targets)
	if _, err := ParseGroup(b, path); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

// ListTargets returns the sorted target names (filenames without .json) in a
// sessions directory. A missing directory yields no names and no error.
func ListTargets(dir string) []string { return listJSONNames(dir) }

// ListGroups returns the sorted group names in a groups directory.
func ListGroups(dir string) []string { return listJSONNames(dir) }

func listJSONNames(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		out = append(out, strings.TrimSuffix(e.Name(), ".json"))
	}
	sort.Strings(out)
	return out
}
