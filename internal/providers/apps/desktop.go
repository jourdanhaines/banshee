package apps

import (
	"bufio"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// DesktopDirs returns the freedesktop application directories in lookup order:
// $XDG_DATA_HOME/applications first, then each $XDG_DATA_DIRS entry.
func DesktopDirs() []string {
	var dirs []string
	if home := os.Getenv("XDG_DATA_HOME"); home != "" {
		dirs = append(dirs, filepath.Join(home, "applications"))
	} else if h, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(h, ".local", "share", "applications"))
	}
	data := os.Getenv("XDG_DATA_DIRS")
	if data == "" {
		data = "/usr/local/share:/usr/share"
	}
	for _, d := range strings.Split(data, ":") {
		if d = strings.TrimSpace(d); d != "" {
			dirs = append(dirs, filepath.Join(d, "applications"))
		}
	}
	return dirs
}

// enricher reads the fields the GAppInfo interface does not expose
// (GenericName, Keywords) straight from the .desktop file.
type enricher struct {
	dirs  []string
	cache map[string]desktopEntry
}

type desktopEntry struct {
	genericName string
	keywords    []string
}

func newEnricher(dirs []string) *enricher {
	return &enricher{dirs: dirs, cache: map[string]desktopEntry{}}
}

// lookup returns the GenericName and Keywords of the .desktop file with the
// given ID, or zero values when the file cannot be found or read.
func (e *enricher) lookup(id string) (string, []string) {
	if ent, ok := e.cache[id]; ok {
		return ent.genericName, ent.keywords
	}
	var ent desktopEntry
	for _, rel := range desktopIDPaths(id) {
		for _, dir := range e.dirs {
			f, err := os.Open(filepath.Join(dir, rel))
			if err != nil {
				continue
			}
			kv := parseDesktopEntry(f)
			f.Close()
			ent = desktopEntry{
				genericName: kv["GenericName"],
				keywords:    splitList(kv["Keywords"]),
			}
			e.cache[id] = ent
			return ent.genericName, ent.keywords
		}
	}
	e.cache[id] = ent
	return ent.genericName, ent.keywords
}

// desktopIDPaths expands a desktop ID into the relative paths it may live at.
// IDs of files in subdirectories are built by replacing "/" with "-", so each
// dash is a candidate directory separator.
func desktopIDPaths(id string) []string {
	paths := []string{id}
	b := []byte(id)
	for i, c := range b {
		if c != '-' {
			continue
		}
		alt := make([]byte, len(b))
		copy(alt, b)
		alt[i] = '/'
		paths = append(paths, string(alt))
	}
	return paths
}

// parseDesktopEntry reads the "[Desktop Entry]" group of a desktop file into a
// key/value map. Locale-suffixed keys (Name[de]) and other groups are ignored;
// the first value for a key wins. Unknown keys are kept — callers pick what
// they need — so new fields never break parsing.
func parseDesktopEntry(r io.Reader) map[string]string {
	kv := map[string]string{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	inEntry := false
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			inEntry = strings.EqualFold(strings.Trim(line, "[]"), "Desktop Entry")
			continue
		}
		if !inEntry {
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq < 0 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		if key == "" || strings.ContainsRune(key, '[') {
			continue // locale-specific variant
		}
		if _, dup := kv[key]; dup {
			continue
		}
		kv[key] = strings.TrimSpace(line[eq+1:])
	}
	return kv
}

// splitList splits a desktop-file ";"-separated list, dropping empty entries.
func splitList(v string) []string {
	if v == "" {
		return nil
	}
	var out []string
	for _, p := range strings.Split(v, ";") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
