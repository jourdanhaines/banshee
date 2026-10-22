package plugins

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jourdanhaines/banshee/internal/providers"
)

// passgenSymbols is the plugin's symbol set; every one must survive tr.
const passgenSymbols = "!@#$%^&*()-_=+[]{};:,.<>?/"

// passgenCoreTools is the PATH allowlist besides the wl-copy stub. setsid is
// left out on purpose: the plugin then copies synchronously, so wl-copy.stdin
// is complete before the notify arrives.
var passgenCoreTools = []string{"sh", "sed", "tr", "od", "head", "base64", "cat"}

// passgenStub logs argv (\x1f-joined) and captures stdin, never echoing it.
const passgenStub = `#!/bin/sh
{ printf '%s\037' "$@"; printf '\n'; } >> "$PASSGEN_TEST_LOG/wl-copy.log"
cat > "$PASSGEN_TEST_LOG/wl-copy.stdin"
`

// passgenHost is a loaded host running the shipped passgen plugin.
type passgenHost struct {
	h      *Host
	prov   providers.Provider
	sunk   chan WireNotify
	logDir string
}

// startPassgen stages plugins/passgen under a hermetic PATH and HOME; with
// noWlCopy the stub is left out entirely.
func startPassgen(t *testing.T, noWlCopy bool) *passgenHost {
	t.Helper()
	src, err := filepath.Abs(filepath.Join("..", "..", "..", "plugins", "passgen"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(src, ManifestName)); err != nil {
		t.Skipf("passgen plugin not present: %v", err)
	}
	coreBin := t.TempDir()
	for _, tool := range passgenCoreTools {
		p, err := exec.LookPath(tool)
		if err != nil {
			t.Skipf("%s not on PATH: %v", tool, err)
		}
		if err := os.Symlink(p, filepath.Join(coreBin, tool)); err != nil {
			t.Fatal(err)
		}
	}
	stubBin := t.TempDir()
	if !noWlCopy {
		if err := os.WriteFile(filepath.Join(stubBin, "wl-copy"), []byte(passgenStub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	logDir := t.TempDir()
	t.Setenv("PATH", stubBin+":"+coreBin)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("PASSGEN_TEST_LOG", logDir)

	root := t.TempDir()
	if err := os.Symlink(src, filepath.Join(root, "passgen")); err != nil {
		t.Skipf("cannot stage passgen plugin: %v", err)
	}
	sunk := make(chan WireNotify, 16)
	h := NewHost(root, Options{
		Timeout: 2 * time.Second,
		Notify: func(pluginID string, n WireNotify, _ func(string, bool, int)) {
			if pluginID == "passgen" {
				sunk <- n
			}
		},
	})
	t.Cleanup(h.Shutdown)
	if err := h.Load(); err != nil {
		t.Fatal(err)
	}
	provs := h.Providers()
	if len(provs) != 1 {
		t.Fatalf("providers = %+v, want the passgen plugin", provs)
	}
	return &passgenHost{h: h, prov: provs[0], sunk: sunk, logDir: logDir}
}

// query retries q until it answers: the first one may race plugin startup.
func (p *passgenHost) query(t *testing.T, q string) []providers.Result {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		rows, err := p.prov.Query(context.Background(), q)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) > 0 || time.Now().After(deadline) {
			return rows
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// submit sends one form submission and returns the notify plus the copied
// value (empty when wl-copy never ran).
func (p *passgenHost) submit(t *testing.T, values map[string]string) (WireNotify, string) {
	t.Helper()
	stdin := filepath.Join(p.logDir, "wl-copy.stdin")
	_ = os.Remove(stdin)
	if err := p.h.Submit("passgen", "gen", values); err != nil {
		t.Fatal(err)
	}
	select {
	case n := <-p.sunk:
		data, _ := os.ReadFile(stdin)
		return n, string(data)
	case <-time.After(5 * time.Second):
		t.Fatalf("no notify for submit %v", values)
		return WireNotify{}, ""
	}
}

// wlCopyArgv returns the last logged wl-copy argv.
func (p *passgenHost) wlCopyArgv(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(p.logDir, "wl-copy.log"))
	if err != nil {
		t.Fatalf("wl-copy never ran: %v", err)
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	return strings.Split(strings.TrimSuffix(lines[len(lines)-1], "\x1f"), "\x1f")
}

// TestPassgenQuery proves every accepted prefix yields the one form row, with
// the generator order following the prefix, and that the gate rejects others.
func TestPassgenQuery(t *testing.T) {
	p := startPassgen(t, false)
	passOrder := []string{"Text password", "UUIDv4", "JWT secret", "Key"}
	keyOrder := []string{"Key", "Text password", "UUIDv4", "JWT secret"}
	for _, tc := range []struct {
		name  string
		query string
		title string
		order []string // nil means the gate rejects the query
	}{
		{"full prefix", "pass", "Generate password", passOrder},
		{"short prefix", "pas", "Generate password", passOrder},
		{"gen prefix", "gen", "Generate password", passOrder},
		{"key prefix", "key", "Generate key", keyOrder},
		{"trailing text ignored", "pass anything", "Generate password", passOrder},
		{"below minimum", "pa", "", nil},
		{"longer than prefix", "password", "", nil},
		{"empty", "", "", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.order == nil {
				rows, err := p.prov.Query(context.Background(), tc.query)
				if err != nil || rows != nil {
					t.Fatalf("Query(%q) = (%+v, %v), want (nil, nil)", tc.query, rows, err)
				}
				return
			}
			rows := p.query(t, tc.query)
			if len(rows) != 1 {
				t.Fatalf("Query(%q) = %d rows, want 1: %+v", tc.query, len(rows), rows)
			}
			r := rows[0]
			if r.ID != "plugin:passgen:gen" || r.Title != tc.title || r.Form == nil {
				t.Fatalf("row = (%q, %q, form %v)", r.ID, r.Title, r.Form != nil)
			}
			if r.Form.Title != "Generate" || r.Form.SubmitLabel != "Generate" {
				t.Errorf("form = (%q, %q), want Generate/Generate", r.Form.Title, r.Form.SubmitLabel)
			}
			if got := r.Form.Fields; !reflect.DeepEqual(got, passgenFields(tc.order)) {
				t.Errorf("fields =\n%+v\nwant\n%+v", got, passgenFields(tc.order))
			}
		})
	}
}

// passgenFields is the form the plugin must declare for one generator order.
func passgenFields(order []string) []providers.FormField {
	when := func(v string) *providers.ShowWhen {
		return &providers.ShowWhen{Key: "generator", Values: []string{v}}
	}
	return []providers.FormField{
		{Key: "generator", Label: "Generator", Options: order},
		{Key: "length", Label: "Length", Slider: &providers.SliderSpec{Min: 8, Max: 128, Step: 1, Default: 24}, ShowWhen: when("Text password")},
		{Key: "symbols", Label: "Symbols", Checkbox: true, Checked: true, ShowWhen: when("Text password")},
		{Key: "ambiguous", Label: "Exclude ambiguous (0O1lI|)", Checkbox: true, ShowWhen: when("Text password")},
		{Key: "jwt_bits", Label: "Bits", Options: []string{"256", "512"}, ShowWhen: when("JWT secret")},
		{Key: "jwt_encoding", Label: "Encoding", Options: []string{"base64url", "base64", "hex"}, ShowWhen: when("JWT secret")},
		{Key: "key_bits", Label: "Bits", Options: []string{"256", "128", "512"}, ShowWhen: when("Key")},
		{Key: "key_encoding", Label: "Encoding", Options: []string{"hex", "base64", "base64url"}, ShowWhen: when("Key")},
	}
}

// passgenText matches a text password of n characters from the full set.
func passgenText(n int) *regexp.Regexp {
	return regexp.MustCompile(`^[A-Za-z0-9` + regexp.QuoteMeta(passgenSymbols) + `]{` + strconv.Itoa(n) + `}$`)
}

// TestPassgenSubmit proves each generator's output shape, the tolerant value
// parsing an older banshee relies on, and that the value only reaches stdin.
func TestPassgenSubmit(t *testing.T) {
	p := startPassgen(t, false)
	p.query(t, "pass") // starts the plugin
	for _, tc := range []struct {
		name    string
		values  map[string]string
		summary string
		want    *regexp.Regexp
		reject  string // characters that must never appear
	}{
		{"text 32 with symbols", map[string]string{"generator": "Text password", "length": "32", "symbols": "true", "ambiguous": "false"},
			"Password copied", passgenText(32), ""},
		{"text without symbols", map[string]string{"generator": "Text password", "length": "24", "symbols": "false"},
			"Password copied", regexp.MustCompile(`^[A-Za-z0-9]{24}$`), ""},
		{"text excluding ambiguous", map[string]string{"generator": "Text password", "length": "128", "ambiguous": "true"},
			"Password copied", passgenText(128), "0O1lI|"},
		{"length absent", map[string]string{"generator": "Text password"}, "Password copied", passgenText(24), ""},
		{"length not a number", map[string]string{"generator": "Text password", "length": "abc"}, "Password copied", passgenText(24), ""},
		{"length above max", map[string]string{"generator": "Text password", "length": "500"}, "Password copied", passgenText(128), ""},
		{"length below min", map[string]string{"generator": "Text password", "length": "3"}, "Password copied", passgenText(8), ""},
		{"uuid", map[string]string{"generator": "UUIDv4"}, "UUID copied",
			regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`), ""},
		{"jwt 256 base64url", map[string]string{"generator": "JWT secret", "jwt_bits": "256", "jwt_encoding": "base64url"},
			"JWT secret copied", regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`), ""},
		{"jwt 256 base64", map[string]string{"generator": "JWT secret", "jwt_bits": "256", "jwt_encoding": "base64"},
			"JWT secret copied", regexp.MustCompile(`^[A-Za-z0-9+/]{43}=$`), ""},
		{"jwt 512 hex", map[string]string{"generator": "JWT secret", "jwt_bits": "512", "jwt_encoding": "hex"},
			"JWT secret copied", regexp.MustCompile(`^[0-9a-f]{128}$`), ""},
		{"jwt bad bits and encoding", map[string]string{"generator": "JWT secret", "jwt_bits": "100", "jwt_encoding": "rot13"},
			"JWT secret copied", regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`), ""},
		{"key 128 hex", map[string]string{"generator": "Key", "key_bits": "128", "key_encoding": "hex"},
			"Key copied", regexp.MustCompile(`^[0-9a-f]{32}$`), ""},
		{"key 512 base64url", map[string]string{"generator": "Key", "key_bits": "512", "key_encoding": "base64url"},
			"Key copied", regexp.MustCompile(`^[A-Za-z0-9_-]{86}$`), ""},
		{"key bad bits and encoding", map[string]string{"generator": "Key", "key_bits": "1024", "key_encoding": "octal"},
			"Key copied", regexp.MustCompile(`^[0-9a-f]{64}$`), ""},
		{"unknown generator", map[string]string{"generator": "Diceware"}, "Password copied", passgenText(24), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n, value := p.submit(t, tc.values)
			if n.ID != "passgen:copied" || n.Summary != tc.summary || n.Body != "" || n.TimeoutMS != 3000 {
				t.Errorf("notify = %+v, want %q with no body", n, tc.summary)
			}
			if !tc.want.MatchString(value) {
				t.Fatalf("copied %q, want %s", value, tc.want)
			}
			if strings.ContainsAny(value, tc.reject) {
				t.Errorf("copied %q contains one of %q", value, tc.reject)
			}
			argv := p.wlCopyArgv(t)
			if !reflect.DeepEqual(argv, []string{"--sensitive"}) {
				t.Errorf("wl-copy argv = %q, want [--sensitive]", argv)
			}
			if strings.Contains(strings.Join(argv, " ")+n.Summary+n.Body, value) {
				t.Errorf("value %q leaked outside wl-copy's stdin", value)
			}
		})
	}
}

// TestPassgenSymbolCoverage proves tr reads every symbol literally: escaping
// mistakes ('-' ranges, '[' classes) silently drop or add characters.
func TestPassgenSymbolCoverage(t *testing.T) {
	p := startPassgen(t, false)
	p.query(t, "pass")
	seen := map[rune]bool{}
	for i := 0; i < 20; i++ {
		_, value := p.submit(t, map[string]string{"generator": "Text password", "length": "128"})
		if !passgenText(128).MatchString(value) {
			t.Fatalf("copied %q outside the charset", value)
		}
		for _, r := range value {
			seen[r] = true
		}
	}
	for _, r := range passgenSymbols {
		if !seen[r] {
			t.Errorf("symbol %q never generated", r)
		}
	}
}

// TestPassgenNoWlCopy proves a missing wl-copy is reported, not fatal.
func TestPassgenNoWlCopy(t *testing.T) {
	p := startPassgen(t, true)
	p.query(t, "pass")
	for i := 0; i < 2; i++ {
		n, value := p.submit(t, map[string]string{"generator": "Text password"})
		if n.Summary != "wl-copy not found" || value != "" {
			t.Fatalf("notify = %+v, copied %q; want wl-copy not found", n, value)
		}
	}
}
