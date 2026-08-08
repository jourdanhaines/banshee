package apps

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParseDesktopEntry(t *testing.T) {
	const raw = `# comment
[Desktop Entry]
Name=Firefox
Name[de]=Feuerfuchs
GenericName = Web Browser
Keywords=Internet;WWW;
malformed line
=novalue
Name=Duplicate Ignored

[Desktop Action new]
GenericName=Not This One
`
	got := parseDesktopEntry(strings.NewReader(raw))
	want := map[string]string{
		"Name":        "Firefox",
		"GenericName": "Web Browser",
		"Keywords":    "Internet;WWW;",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseDesktopEntry = %#v, want %#v", got, want)
	}
}

func TestSplitList(t *testing.T) {
	tests := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{";;", nil},
		{"Internet;WWW;", []string{"Internet", "WWW"}},
		{"shell;;  prompt ;", []string{"shell", "prompt"}},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := splitList(tt.in); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("splitList(%q) = %#v, want %#v", tt.in, got, tt.want)
			}
		})
	}
}

func TestDesktopIDPaths(t *testing.T) {
	tests := []struct {
		id   string
		want []string
	}{
		{"firefox.desktop", []string{"firefox.desktop"}},
		{"kde-konsole.desktop", []string{"kde-konsole.desktop", "kde/konsole.desktop"}},
		{"a-b-c.desktop", []string{"a-b-c.desktop", "a/b-c.desktop", "a-b/c.desktop"}},
	}
	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			if got := desktopIDPaths(tt.id); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("desktopIDPaths(%q) = %#v, want %#v", tt.id, got, tt.want)
			}
		})
	}
}

func TestEnricherLookup(t *testing.T) {
	dir := filepath.Join("testdata", "applications")
	tests := []struct {
		name         string
		id           string
		wantGeneric  string
		wantKeywords []string
	}{
		{"top level file", "firefox.desktop", "Web Browser", []string{"Internet", "WWW", "Browser", "Web"}},
		{"subdirectory id", "kde-konsole.desktop", "Terminal", []string{"shell", "prompt"}},
		{"no optional fields", "bare.desktop", "", nil},
		{"missing file", "nope.desktop", "", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnricher([]string{dir})
			generic, keywords := e.lookup(tt.id)
			if generic != tt.wantGeneric {
				t.Fatalf("generic = %q, want %q", generic, tt.wantGeneric)
			}
			if !reflect.DeepEqual(keywords, tt.wantKeywords) {
				t.Fatalf("keywords = %#v, want %#v", keywords, tt.wantKeywords)
			}
			// Second lookup must hit the cache and agree.
			g2, k2 := e.lookup(tt.id)
			if g2 != generic || !reflect.DeepEqual(k2, keywords) {
				t.Fatalf("cached lookup differs: %q %#v", g2, k2)
			}
		})
	}
}

func TestDesktopDirs(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "/home/u/.data")
	t.Setenv("XDG_DATA_DIRS", "/opt/share: :/usr/share")
	want := []string{"/home/u/.data/applications", "/opt/share/applications", "/usr/share/applications"}
	if got := DesktopDirs(); !reflect.DeepEqual(got, want) {
		t.Fatalf("DesktopDirs = %#v, want %#v", got, want)
	}

	t.Setenv("XDG_DATA_DIRS", "")
	got := DesktopDirs()
	want = []string{"/home/u/.data/applications", "/usr/local/share/applications", "/usr/share/applications"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("DesktopDirs (default dirs) = %#v, want %#v", got, want)
	}
}
