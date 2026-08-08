package connectors

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/jourdanhaines/banshee/internal/config"
)

// RepoConfigPath returns the path of a repository's .banshee/config.json.
func RepoConfigPath(repoPath string) string {
	return filepath.Join(repoPath, filepath.FromSlash(config.RepoConfigRelPath))
}

// LoadRepoConfig reads <repoPath>/.banshee/config.json. A missing file is not
// an error: it yields a zero RepoConfig with no connector bindings. Unknown
// JSON keys are ignored for forward compatibility.
func LoadRepoConfig(repoPath string) (config.RepoConfig, error) {
	data, err := os.ReadFile(RepoConfigPath(repoPath))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return config.RepoConfig{}, nil
		}
		return config.RepoConfig{}, err
	}
	var rc config.RepoConfig
	if err := json.Unmarshal(data, &rc); err != nil {
		return config.RepoConfig{}, err
	}
	return rc, nil
}

type repoConfEntry struct {
	stamp string
	conf  config.RepoConfig
}

// repoConfCache memoizes per-repo configs, invalidated by the config file's
// mtime and size so an edit is picked up without a daemon restart.
type repoConfCache struct {
	mu      sync.Mutex
	entries map[string]repoConfEntry
	load    func(string) (config.RepoConfig, error)
}

func newRepoConfCache(load func(string) (config.RepoConfig, error)) *repoConfCache {
	return &repoConfCache{entries: map[string]repoConfEntry{}, load: load}
}

func (c *repoConfCache) get(repoPath string) config.RepoConfig {
	stamp := "missing"
	if fi, err := os.Stat(RepoConfigPath(repoPath)); err == nil {
		stamp = fi.ModTime().UTC().Format(time.RFC3339Nano) + "|" + strconv.FormatInt(fi.Size(), 10)
	}
	c.mu.Lock()
	e, hit := c.entries[repoPath]
	c.mu.Unlock()
	if hit && e.stamp == stamp {
		return e.conf
	}
	rc, err := c.load(repoPath)
	if err != nil {
		rc = config.RepoConfig{}
	}
	c.mu.Lock()
	c.entries[repoPath] = repoConfEntry{stamp: stamp, conf: rc}
	c.mu.Unlock()
	return rc
}
