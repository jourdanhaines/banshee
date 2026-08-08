package connectors

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// SaveRepoBinding writes connectors[connectorID] = binding into
// <repoPath>/.banshee/config.json, creating the directory and file (v: 1)
// when missing. The merge goes through json.RawMessage at both the top level
// and inside "connectors" so unknown keys survive byte-exact — the
// forward-compatibility rule forbids a load-modify-save through RepoConfig,
// which would drop them. The write is atomic (tmp + rename) so a concurrent
// repoConfCache read never sees a torn file; the rename also bumps the
// file's mtime+size stamp, which is what invalidates that cache.
func SaveRepoBinding(repoPath, connectorID, binding string) error {
	binding = strings.TrimSpace(binding)
	if repoPath == "" || connectorID == "" || binding == "" {
		return errors.New("connectors: repo path, connector id and binding are all required")
	}

	path := RepoConfigPath(repoPath)

	top := map[string]json.RawMessage{}
	if data, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(data, &top); err != nil {
			return fmt.Errorf("connectors: %s is not a JSON object; refusing to overwrite: %w", path, err)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}

	conns := map[string]json.RawMessage{}
	if raw, ok := top["connectors"]; ok {
		if err := json.Unmarshal(raw, &conns); err != nil {
			return fmt.Errorf("connectors: %s has a non-object \"connectors\" key; refusing to overwrite: %w", path, err)
		}
	}
	b, err := json.Marshal(binding)
	if err != nil {
		return err
	}
	conns[connectorID] = b
	rawConns, err := json.Marshal(conns)
	if err != nil {
		return err
	}
	top["connectors"] = rawConns
	if _, ok := top["v"]; !ok {
		top["v"] = json.RawMessage("1")
	}

	out, err := json.MarshalIndent(top, "", "  ")
	if err != nil {
		return err
	}
	out = append(out, '\n')

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.tmp.%d", path, os.Getpid())
	if err := os.WriteFile(tmp, out, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
