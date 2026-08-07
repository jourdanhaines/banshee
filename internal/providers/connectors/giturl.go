package connectors

import (
	"context"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// OriginResolver returns the browsable https URL of a repository's origin
// remote. ok is false when the path is not a git repo, has no origin, or the
// remote URL cannot be turned into a web URL.
type OriginResolver func(repoPath string) (string, bool)

// gitTimeout bounds a single `git remote get-url` invocation.
const gitTimeout = 2 * time.Second

// schemeRewrite maps git transport schemes onto the browsable scheme.
var schemeRewrite = map[string]string{
	"ssh":     "https",
	"git":     "https",
	"git+ssh": "https",
	"http":    "http",
	"https":   "https",
}

// NormalizeGitURL converts a git remote URL into a browsable web URL.
//
// It understands the scp-like form (git@github.com:user/repo.git), explicit
// transports (ssh://, git://, https://) and strips credentials, ports added by
// ssh transports, and the trailing ".git". Local paths return ok == false.
func NormalizeGitURL(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimSuffix(raw, "/")
	if raw == "" {
		return "", false
	}

	scheme, host, path := "", "", ""
	if i := strings.Index(raw, "://"); i >= 0 {
		u, err := url.Parse(raw)
		if err != nil {
			return "", false
		}
		rewritten, known := schemeRewrite[strings.ToLower(u.Scheme)]
		if !known || u.Host == "" {
			return "", false
		}
		scheme = rewritten
		host = u.Hostname()
		if p := u.Port(); p != "" && rewritten == strings.ToLower(u.Scheme) {
			// Keep an explicit port only when the scheme is unchanged;
			// an ssh port is meaningless for the https URL.
			host += ":" + p
		}
		path = u.Path
	} else {
		// scp-like: [user@]host:path — the part before ':' must be a host.
		i := strings.Index(raw, ":")
		if i <= 0 || strings.Contains(raw[:i], "/") {
			return "", false
		}
		hostPart := raw[:i]
		if at := strings.LastIndex(hostPart, "@"); at >= 0 {
			hostPart = hostPart[at+1:]
		}
		if hostPart == "" {
			return "", false
		}
		scheme, host, path = "https", hostPart, raw[i+1:]
	}

	path = strings.Trim(path, "/")
	path = strings.TrimSuffix(path, ".git")
	if path == "" || host == "" {
		return "", false
	}
	return scheme + "://" + host + "/" + path, true
}

// DeriveOrigin runs `git -C <repoPath> remote get-url origin` and normalizes
// the result. It returns ok == false for non-repositories and repos without an
// origin remote.
func DeriveOrigin(repoPath string) (string, bool) {
	if !isGitRepo(repoPath) {
		return "", false
	}
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", repoPath, "remote", "get-url", "origin")
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.Output()
	if err != nil {
		return "", false
	}
	return NormalizeGitURL(string(out))
}

func isGitRepo(repoPath string) bool {
	_, err := os.Stat(filepath.Join(repoPath, ".git"))
	return err == nil
}

// gitStamp returns a cheap change stamp for a repo's git configuration; it
// changes whenever a remote is added, removed or edited.
func gitStamp(repoPath string) (string, bool) {
	for _, p := range []string{filepath.Join(repoPath, ".git", "config"), filepath.Join(repoPath, ".git")} {
		if fi, err := os.Stat(p); err == nil {
			return p + "|" + fi.ModTime().UTC().Format(time.RFC3339Nano) + "|" + strconv.FormatInt(fi.Size(), 10), true
		}
	}
	return "", false
}

type originEntry struct {
	stamp string
	url   string
	ok    bool
}

// originCache memoizes DeriveOrigin per repo path, invalidated by the mtime
// and size of the repo's .git/config.
type originCache struct {
	mu      sync.Mutex
	entries map[string]originEntry
	derive  func(string) (string, bool)
}

func newOriginCache(derive func(string) (string, bool)) *originCache {
	return &originCache{entries: map[string]originEntry{}, derive: derive}
}

func (c *originCache) get(repoPath string) (string, bool) {
	stamp, isRepo := gitStamp(repoPath)
	if !isRepo {
		return "", false
	}
	c.mu.Lock()
	e, hit := c.entries[repoPath]
	c.mu.Unlock()
	if hit && e.stamp == stamp {
		return e.url, e.ok
	}
	u, ok := c.derive(repoPath)
	c.mu.Lock()
	c.entries[repoPath] = originEntry{stamp: stamp, url: u, ok: ok}
	c.mu.Unlock()
	return u, ok
}
