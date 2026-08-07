package connectors

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/jourdanhaines/banshee/internal/providers"
)

func TestParseManifest(t *testing.T) {
	tests := []struct {
		name    string
		json    string
		wantErr bool
		check   func(t *testing.T, m Manifest)
	}{
		{
			name: "url plugin",
			json: `{"v":1,"id":"railway","name":"Railway","icon":"railway.svg","accent":"#a78bfa",
			        "type":"url","url":{"template":"https://railway.com/project/{binding}",
			        "title":"Open {repo} on Railway","requires_binding":true}}`,
			check: func(t *testing.T, m Manifest) {
				if m.Type != TypeURL || m.URL.Template != "https://railway.com/project/{binding}" {
					t.Fatalf("bad url spec: %+v", m.URL)
				}
				if !m.URL.RequiresBinding {
					t.Fatal("requires_binding not parsed")
				}
				if m.Category != providers.CatConnector {
					t.Fatalf("category = %d, want CatConnector", m.Category)
				}
			},
		},
		{
			name: "exec plugin",
			json: `{"v":1,"id":"wifi","name":"Wi-Fi","icon":"network-wireless-symbolic","type":"exec",
			        "exec":{"bin":"./plugin","args":["--json"],"prefix":"wifi","timeout_ms":300}}`,
			check: func(t *testing.T, m Manifest) {
				if m.Exec.Bin != "./plugin" || m.Exec.Prefix != "wifi" || m.Exec.TimeoutMS != 300 {
					t.Fatalf("bad exec spec: %+v", m.Exec)
				}
				if len(m.Exec.Args) != 1 || m.Exec.Args[0] != "--json" {
					t.Fatalf("bad args: %+v", m.Exec.Args)
				}
			},
		},
		{
			name: "unknown keys ignored and name defaults to id",
			json: `{"v":1,"id":"future","type":"url","url":{"template":"https://x/{binding}"},
			        "tomorrow":{"nested":true},"extra":42}`,
			check: func(t *testing.T, m Manifest) {
				if m.Name != "future" {
					t.Fatalf("name = %q, want defaulted to id", m.Name)
				}
			},
		},
		{"wrong version", `{"v":2,"id":"x","type":"url","url":{"template":"https://x"}}`, true, nil},
		{"missing version", `{"id":"x","type":"url","url":{"template":"https://x"}}`, true, nil},
		{"missing id", `{"v":1,"type":"url","url":{"template":"https://x"}}`, true, nil},
		{"path traversal id", `{"v":1,"id":"../evil","type":"url","url":{"template":"https://x"}}`, true, nil},
		{"missing type", `{"v":1,"id":"x"}`, true, nil},
		{"unknown type", `{"v":1,"id":"x","type":"dbus"}`, true, nil},
		{"url without template", `{"v":1,"id":"x","type":"url","url":{"title":"t"}}`, true, nil},
		{"url without url block", `{"v":1,"id":"x","type":"url"}`, true, nil},
		{"exec without bin", `{"v":1,"id":"x","type":"exec","exec":{"prefix":"p"}}`, true, nil},
		{"exec negative timeout", `{"v":1,"id":"x","type":"exec","exec":{"bin":"p","timeout_ms":-1}}`, true, nil},
		{"malformed json", `{`, true, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, err := ParseManifest([]byte(tt.json), "/plugins/x")
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %+v", m)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if m.Dir != "/plugins/x" {
				t.Fatalf("Dir = %q, want /plugins/x", m.Dir)
			}
			if tt.check != nil {
				tt.check(t, m)
			}
		})
	}
}

func TestBuiltinsValid(t *testing.T) {
	for _, m := range Builtins() {
		if err := m.Validate(); err != nil {
			t.Errorf("builtin %q invalid: %v", m.ID, err)
		}
	}
}

func TestResolveIcon(t *testing.T) {
	tests := []struct {
		name string
		icon string
		dir  string
		want providers.Icon
	}{
		{"theme name", "network-wireless-symbolic", "/plugins/wifi", providers.Icon{ThemeName: "network-wireless-symbolic"}},
		{"builtin name", "github", "", providers.Icon{Builtin: "github"}},
		{"builtin name beats theme lookup", "railway", "/plugins/rw", providers.Icon{Builtin: "railway"}},
		{"relative path", "railway.svg", "/plugins/rw", providers.Icon{Path: filepath.Join("/plugins/rw", "railway.svg")}},
		{"nested relative path", "icons/logo.png", "/plugins/rw", providers.Icon{Path: filepath.Join("/plugins/rw", "icons/logo.png")}},
		{"absolute path", "/usr/share/icons/x.svg", "/plugins/rw", providers.Icon{Path: "/usr/share/icons/x.svg"}},
		{"empty", "", "/plugins/rw", providers.Icon{}},
		{"no dir keeps relative", "logo.svg", "", providers.Icon{Path: "logo.svg"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ResolveIcon(tt.icon, tt.dir); got != tt.want {
				t.Fatalf("ResolveIcon(%q, %q) = %+v, want %+v", tt.icon, tt.dir, got, tt.want)
			}
		})
	}
}

// TestParseManifestClampsExecTimeout: ConcurrentAggregator waits for every
// provider, so an exec plugin's soft timeout is an upper bound on how long the
// whole launcher takes to paint. A manifest asking for ten minutes must not get
// them.
func TestParseManifestClampsExecTimeout(t *testing.T) {
	tests := []struct {
		name string
		in   int
		want int
	}{
		{"zero means the host default", 0, 0},
		{"a sane value is kept", 150, 150},
		{"the ceiling is kept", MaxExecTimeoutMS, MaxExecTimeoutMS},
		{"an absurd value is clamped", 600000, MaxExecTimeoutMS},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := fmt.Sprintf(
				`{"v":1,"id":"slow","type":"exec","exec":{"bin":"./p","timeout_ms":%d}}`, tt.in)
			m, err := ParseManifest([]byte(body), "/plugins/slow")
			if err != nil {
				t.Fatalf("ParseManifest: %v", err)
			}
			if m.Exec.TimeoutMS != tt.want {
				t.Fatalf("timeout_ms = %d, want %d", m.Exec.TimeoutMS, tt.want)
			}
		})
	}
}
