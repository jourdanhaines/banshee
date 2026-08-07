package cli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"

	"github.com/jourdanhaines/banshee/internal/index"
)

// ErrCancelled reports that the user aborted a picker.
var ErrCancelled = errors.New("cancelled")

// fzfPreview is the repository preview shown by fzf: the repo path in blue
// followed by its README, resolved through the BANSHEE_REPO_LIST environment
// variable so the preview shell needs no banshee call.
const fzfPreview = `name={}
rpath=$(printf '%s\n' "$BANSHEE_REPO_LIST" | grep "|${name}$" | head -1 | cut -d"|" -f1)
printf '\033[1;34m%s\033[0m\n\n' "$rpath"
if [ -f "$rpath/README.md" ]; then cat "$rpath/README.md"; else echo "No preview"; fi`

// SelectRepo resolves query to a repository path: an unambiguous exact
// basename match wins outright, otherwise the user picks from the full list
// (fzf when installed, a numbered prompt otherwise).
func (a *App) SelectRepo(query string) (string, error) {
	repos := a.Index.Repos()
	if len(repos) == 0 {
		return "", errors.New("no git repositories found")
	}
	if query != "" {
		if repo, ok := a.Index.Exact(query); ok {
			return repo.Path, nil
		}
	}

	// Last entry wins for duplicate basenames, matching the v0.3 map build.
	byName := map[string]string{}
	for _, r := range repos {
		byName[r.Name] = r.Path
	}
	names := index.Names(a.Index)

	var (
		choice string
		err    error
	)
	if a.hasFzf() {
		choice, err = a.fzfRepo(names, repos, query)
	} else {
		choice, err = a.numbered("Select a git repository", names)
	}
	if err != nil {
		return "", err
	}
	path, ok := byName[choice]
	if !ok {
		return "", fmt.Errorf("unknown repository %q", choice)
	}
	return path, nil
}

// SelectTargets asks the user which targets belong to a group. current (the
// group's existing targets) is floated to the top of the list. The returned
// order is the pick order under fzf.
func (a *App) SelectTargets(group string, current []string) ([]string, error) {
	pool := a.Res.Pool()
	if len(pool) == 0 {
		return nil, errors.New("no targets available (no repos found, no session configs)")
	}
	ordered := floatFirst(pool, current)

	header := fmt.Sprintf("select targets for group '%s' — TAB to toggle, ENTER to confirm", group)
	if len(current) > 0 {
		header = fmt.Sprintf("editing group '%s' — current: %s — TAB to toggle, ENTER to confirm",
			group, strings.Join(current, ","))
	}

	if a.hasFzf() {
		out, err := a.runFzf(ordered, []string{
			"--multi",
			"--layout=reverse",
			"--border",
			"--prompt=targets> ",
			"--header=" + header,
		}, nil)
		if err != nil {
			return nil, err
		}
		if len(out) == 0 {
			return nil, ErrCancelled
		}
		return out, nil
	}
	return a.numberedMulti(header, ordered)
}

// floatFirst returns pool with the entries of first (in their given order,
// deduplicated, restricted to pool) moved to the front.
func floatFirst(pool, first []string) []string {
	inPool := map[string]bool{}
	for _, p := range pool {
		inPool[p] = true
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(pool))
	for _, f := range first {
		f = strings.TrimSpace(f)
		if f == "" || !inPool[f] || seen[f] {
			continue
		}
		seen[f] = true
		out = append(out, f)
	}
	for _, p := range pool {
		if !seen[p] {
			out = append(out, p)
		}
	}
	return out
}

// hasFzf reports whether the fzf picker can be used. App.HasFzf overrides the
// PATH probe so tests can exercise both pickers without depending on what the
// developer happens to have installed.
func (a *App) hasFzf() bool {
	if a.HasFzf != nil {
		return a.HasFzf()
	}
	_, err := exec.LookPath("fzf")
	return err == nil
}

func (a *App) fzfRepo(names []string, repos []index.Repo, query string) (string, error) {
	var list strings.Builder
	for _, r := range repos {
		list.WriteString(r.Path)
		list.WriteString("|")
		list.WriteString(r.Name)
		list.WriteString("\n")
	}

	args := []string{
		"--layout=reverse",
		"--border",
		"--prompt=banshee> ",
		"--header=Select a git repository",
		"--preview=" + fzfPreview,
		"--preview-label-pos=0",
		"--preview-window=right:50%",
	}
	if query != "" {
		args = append(args, "--query="+query)
	}

	out, err := a.runFzf(names, args, []string{"BANSHEE_REPO_LIST=" + list.String()})
	if err != nil {
		return "", err
	}
	if len(out) == 0 {
		return "", ErrCancelled
	}
	return out[0], nil
}

// runFzf pipes lines into fzf and returns the selected lines. Extra options
// from banshee.conf's fzf_opts are appended.
//
// fzf_opts is split on whitespace — it is not run through a shell, so quoting
// and shell expansion do not apply (`--color=16 --height=40%` works,
// `--header="two words"` does not).
func (a *App) runFzf(lines []string, args []string, env []string) ([]string, error) {
	args = append(args, splitOpts(a.Cfg.FzfOpts)...)

	cmd := exec.Command("fzf", args...)
	cmd.Stdin = strings.NewReader(strings.Join(lines, "\n") + "\n")
	cmd.Stderr = a.Err
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	out, err := cmd.Output()
	if err != nil {
		// fzf exits 1 on no match and 130 on abort: both mean "cancelled".
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return nil, ErrCancelled
		}
		return nil, err
	}
	var sel []string
	for _, l := range strings.Split(string(out), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			sel = append(sel, l)
		}
	}
	return sel, nil
}

// splitOpts splits a conf option string on whitespace.
func splitOpts(s string) []string { return strings.Fields(s) }

// numbered is the no-fzf fallback picker: a numbered list on stderr and one
// line of input.
func (a *App) numbered(header string, items []string) (string, error) {
	if len(items) == 0 {
		return "", ErrCancelled
	}
	fmt.Fprintf(a.Err, "%s\n", header)
	for i, item := range items {
		fmt.Fprintf(a.Err, "  %3d) %s\n", i+1, item)
	}
	line, err := a.prompt("banshee> ")
	if err != nil {
		return "", ErrCancelled
	}
	idx, err := parseIndex(line, len(items))
	if err != nil {
		return "", err
	}
	return items[idx], nil
}

// numberedMulti is the no-fzf fallback multi-select: numbers separated by
// spaces or commas.
func (a *App) numberedMulti(header string, items []string) ([]string, error) {
	if len(items) == 0 {
		return nil, ErrCancelled
	}
	fmt.Fprintf(a.Err, "%s\n", header)
	for i, item := range items {
		fmt.Fprintf(a.Err, "  %3d) %s\n", i+1, item)
	}
	line, err := a.prompt("targets (space or comma separated numbers)> ")
	if err != nil {
		return nil, ErrCancelled
	}
	fields := strings.FieldsFunc(line, func(r rune) bool { return r == ' ' || r == ',' || r == '\t' })
	if len(fields) == 0 {
		return nil, ErrCancelled
	}
	var out []string
	seen := map[string]bool{}
	for _, f := range fields {
		idx, err := parseIndex(f, len(items))
		if err != nil {
			return nil, err
		}
		if !seen[items[idx]] {
			seen[items[idx]] = true
			out = append(out, items[idx])
		}
	}
	return out, nil
}

func parseIndex(s string, n int) (int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, ErrCancelled
	}
	i, err := strconv.Atoi(s)
	if err != nil || i < 1 || i > n {
		return 0, fmt.Errorf("invalid selection %q", s)
	}
	return i - 1, nil
}

// sortedUnique returns s sorted with duplicates removed.
func sortedUnique(s []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(s))
	for _, v := range s {
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}
