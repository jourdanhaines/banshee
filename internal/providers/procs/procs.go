// Package procs turns running processes into "Kill <name>" launcher results.
//
// Processes are read from /proc (configurable root, so the scanner is
// testable), deduplicated by their comm name and aggregated: one result per
// process name carrying every PID that shares it. Activating the result sends
// SIGTERM to all of them, the alternate action SIGKILL — see
// RegisterKillHandler.
//
// The provider deliberately returns nothing for an empty query: killing things
// is destructive and must never show up in the launcher's idle state.
package procs

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"

	"github.com/jourdanhaines/banshee/internal/providers"
)

// ActKillProcs is the Action.Kind emitted by this provider. Action.Argv holds
// the PIDs to signal as decimal strings and Action.Sig the signal to send.
const ActKillProcs = "kill-procs"

// Scorer scores a candidate string against a query, reporting whether it
// matched at all. It mirrors the signature of internal/fuzzy.Score; the
// concrete implementation is injected so this package stays independent of the
// ranking implementation.
type Scorer func(query, candidate string) (int, bool)

// Default provider tuning.
const (
	// DefaultRoot is the procfs mount point.
	DefaultRoot = "/proc"
	// DefaultMaxPIDsShown caps how many PIDs are spelled out in a subtitle;
	// the action still carries every PID.
	DefaultMaxPIDsShown = 6
	// snippetLen caps the command-line snippet in a subtitle.
	snippetLen = 90
)

// Option configures a Provider.
type Option func(*Provider)

// WithRoot overrides the procfs root (tests point this at a fixture tree).
func WithRoot(root string) Option {
	return func(p *Provider) {
		if root != "" {
			p.root = root
		}
	}
}

// WithSelfPID overrides the PID skipped as "us". Zero means skip nothing.
func WithSelfPID(pid int) Option { return func(p *Provider) { p.self = pid } }

// WithMaxResults caps the number of results a query returns. Values <= 0
// mean unlimited (the default).
func WithMaxResults(n int) Option {
	return func(p *Provider) { p.maxResults = n }
}

// WithMinScore drops matches scoring below min, keeping weak process matches
// out of repo- and session-dominated result lists.
func WithMinScore(min int) Option { return func(p *Provider) { p.minScore = min } }

// Provider is the running-process result provider. It rescans /proc on every
// query: the data is cheap to read and stale PIDs are worse than useless.
type Provider struct {
	score      Scorer
	root       string
	self       int
	maxResults int
	minScore   int
}

var _ providers.Provider = (*Provider)(nil)

// New builds a process provider. score must not be nil.
func New(score Scorer, opts ...Option) *Provider {
	p := &Provider{
		score: score,
		root:  DefaultRoot,
		self:  os.Getpid(),
	}
	for _, o := range opts {
		o(p)
	}
	return p
}

// Name implements providers.Provider.
func (p *Provider) Name() string { return "procs" }

// Group is one process name and every live PID running under it.
type Group struct {
	Name string
	PIDs []int
	// Cmdline is the command line of the first PID seen, for context.
	Cmdline string
}

// Query implements providers.Provider. An empty query returns no results.
func (p *Provider) Query(ctx context.Context, q string) ([]providers.Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(q) == "" {
		return nil, nil
	}
	groups, err := p.Scan(ctx)
	if err != nil {
		return nil, err
	}

	type scored struct {
		g     Group
		score int
	}
	matches := make([]scored, 0, 16)
	for _, g := range groups {
		s, ok := p.score(q, g.Name)
		if !ok || s < p.minScore {
			continue
		}
		matches = append(matches, scored{g: g, score: s})
	}
	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].score != matches[j].score {
			return matches[i].score > matches[j].score
		}
		return matches[i].g.Name < matches[j].g.Name
	})
	if p.maxResults > 0 && len(matches) > p.maxResults {
		matches = matches[:p.maxResults]
	}
	out := make([]providers.Result, 0, len(matches))
	for _, m := range matches {
		out = append(out, Result(m.g, m.score))
	}
	return out, nil
}

// Scan reads the procfs root and returns one Group per process name, sorted by
// name. Kernel threads (empty cmdline), the current process and processes that
// disappear mid-scan are skipped.
func (p *Provider) Scan(ctx context.Context) ([]Group, error) {
	entries, err := os.ReadDir(p.root)
	if err != nil {
		return nil, err
	}
	byName := make(map[string]*Group, len(entries))
	for i, e := range entries {
		if i%64 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		pid, ok := pidOf(e.Name())
		if !ok || (p.self != 0 && pid == p.self) {
			continue
		}
		dir := filepath.Join(p.root, e.Name())
		comm, err := readTrimmed(filepath.Join(dir, "comm"))
		if err != nil || comm == "" {
			continue // exited between ReadDir and now
		}
		argv := readArgv(filepath.Join(dir, "cmdline"))
		if len(argv) == 0 {
			continue // kernel thread
		}
		name := displayName(comm, argv[0])
		g, seen := byName[name]
		if !seen {
			g = &Group{Name: name, Cmdline: snippet(argv)}
			byName[name] = g
		}
		g.PIDs = append(g.PIDs, pid)
	}

	out := make([]Group, 0, len(byName))
	for _, g := range byName {
		sort.Ints(g.PIDs)
		out = append(out, *g)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Result converts a process group to a launcher row: SIGTERM on activation,
// SIGKILL on the alternate action.
func Result(g Group, score int) providers.Result {
	argv := pidArgv(g.PIDs)
	term := providers.Action{Kind: ActKillProcs, Argv: argv, Sig: syscall.SIGTERM}
	alt := providers.Action{Kind: ActKillProcs, Argv: argv, Sig: syscall.SIGKILL}
	return providers.Result{
		ID:        "kill:" + g.Name,
		Title:     "Kill " + g.Name,
		Subtitle:  subtitle(g),
		Icon:      providers.Icon{ThemeName: "process-stop-symbolic"},
		Category:  providers.CatKill,
		Score:     score,
		Action:    term,
		AltAction: &alt,
	}
}

// subtitle renders "pid 1234 · /usr/bin/foo --bar" for a single process and
// "3 processes · pids 1, 2, 3 · /usr/bin/foo" for a group.
func subtitle(g Group) string {
	var b strings.Builder
	switch {
	case len(g.PIDs) == 1:
		b.WriteString("pid ")
		b.WriteString(strconv.Itoa(g.PIDs[0]))
	default:
		b.WriteString(strconv.Itoa(len(g.PIDs)))
		b.WriteString(" processes · pids ")
		shown := g.PIDs
		extra := 0
		if len(shown) > DefaultMaxPIDsShown {
			extra = len(shown) - DefaultMaxPIDsShown
			shown = shown[:DefaultMaxPIDsShown]
		}
		for i, pid := range shown {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(strconv.Itoa(pid))
		}
		if extra > 0 {
			b.WriteString(" +")
			b.WriteString(strconv.Itoa(extra))
			b.WriteString(" more")
		}
	}
	if g.Cmdline != "" {
		b.WriteString(" · ")
		b.WriteString(g.Cmdline)
	}
	return b.String()
}

func pidArgv(pids []int) []string {
	argv := make([]string, len(pids))
	for i, pid := range pids {
		argv[i] = strconv.Itoa(pid)
	}
	return argv
}

func pidOf(name string) (int, bool) {
	for _, c := range name {
		if c < '0' || c > '9' {
			return 0, false
		}
	}
	pid, err := strconv.Atoi(name)
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, true
}

func readTrimmed(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// readArgv reads a NUL-separated /proc/<pid>/cmdline into its arguments.
func readArgv(path string) []string {
	b, err := os.ReadFile(path)
	if err != nil || len(b) == 0 {
		return nil
	}
	var argv []string
	for _, part := range strings.Split(string(b), "\x00") {
		if part != "" {
			argv = append(argv, part)
		}
	}
	return argv
}

// displayName prefers the executable's basename when comm is truncated.
// The kernel caps comm at 15 bytes, so long binary names arrive clipped.
func displayName(comm, argv0 string) string {
	if len(comm) < 15 {
		return comm
	}
	base := filepath.Base(argv0)
	if base != "" && base != "." && base != string(filepath.Separator) && strings.HasPrefix(base, comm) {
		return base
	}
	return comm
}

// snippet joins argv into a single truncated line for the subtitle.
func snippet(argv []string) string {
	s := strings.Join(argv, " ")
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > snippetLen {
		s = strings.TrimRight(string(r[:snippetLen]), " ") + "…"
	}
	return s
}
