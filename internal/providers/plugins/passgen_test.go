package plugins

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jourdanhaines/banshee/internal/providers"
)

// Charsets the plugin declares for its text variants.
const (
	passgenAlnum       = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
	passgenUnambiguous = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789"
	passgenSymbols     = "!@#$%^&*()-_=+[]{};:,.<>?/"
)

// passgenCoreTools is the PATH allowlist besides the wl-copy stub. setsid is
// left out on purpose: the plugin then copies synchronously, so wl-copy.stdin
// is complete before the notify arrives.
var passgenCoreTools = []string{"sh", "sed", "cat"}

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

// passgenFields is the form the plugin must declare for one generator order:
// the 8 option fields, then one unlabelled generated field per variant.
func passgenFields(order []string) []providers.FormField {
	cond := func(k, v string) providers.Condition {
		return providers.Condition{Key: k, Values: []string{v}}
	}
	when := func(v string) []providers.Condition { return []providers.Condition{cond("generator", v)} }
	fields := []providers.FormField{
		{Key: "generator", Label: "Generator", Options: order},
		{Key: "length", Label: "Length", Slider: &providers.SliderSpec{Min: 8, Max: 128, Step: 1, Default: 24}, ShowWhen: when("Text password")},
		{Key: "symbols", Label: "Symbols", Checkbox: true, Checked: true, ShowWhen: when("Text password")},
		{Key: "ambiguous", Label: "Exclude ambiguous (0O1lI|)", Checkbox: true, ShowWhen: when("Text password")},
		{Key: "jwt_bits", Label: "Bits", Options: []string{"256", "512"}, ShowWhen: when("JWT secret")},
		{Key: "jwt_encoding", Label: "Encoding", Options: []string{"base64url", "base64", "hex"}, ShowWhen: when("JWT secret")},
		{Key: "key_bits", Label: "Bits", Options: []string{"256", "128", "512"}, ShowWhen: when("Key")},
		{Key: "key_encoding", Label: "Encoding", Options: []string{"hex", "base64", "base64url"}, ShowWhen: when("Key")},
	}
	onoff := map[string]string{"on": "true", "off": "false"}
	for _, sym := range []string{"on", "off"} {
		for _, amb := range []string{"on", "off"} {
			cs := passgenAlnum
			if amb == "on" {
				cs = passgenUnambiguous
			}
			if sym == "on" {
				cs += passgenSymbols
			}
			fields = append(fields, providers.FormField{
				Key:      "gen_text_" + sym + "_" + amb,
				Generate: &providers.GenerateSpec{Charset: cs, LengthFrom: "length"},
				ShowWhen: []providers.Condition{cond("generator", "Text password"), cond("symbols", onoff[sym]), cond("ambiguous", onoff[amb])},
			})
		}
	}
	fields = append(fields, providers.FormField{
		Key:      "gen_uuid",
		Generate: &providers.GenerateSpec{Pattern: "xxxxxxxx-xxxx-4xxx-[89ab]xxx-xxxxxxxxxxxx", Charset: "0123456789abcdef"},
		ShowWhen: when("UUIDv4"),
	})
	for _, g := range []struct {
		kind, gen  string
		bits, encs []string
	}{
		{"jwt", "JWT secret", []string{"256", "512"}, []string{"base64url", "base64", "hex"}},
		{"key", "Key", []string{"256", "128", "512"}, []string{"hex", "base64", "base64url"}},
	} {
		for _, bits := range g.bits {
			for _, enc := range g.encs {
				n, _ := strconv.Atoi(bits)
				fields = append(fields, providers.FormField{
					Key:      "gen_" + g.kind + "_" + bits + "_" + enc,
					Generate: &providers.GenerateSpec{Bytes: n / 8, Encoding: enc},
					ShowWhen: []providers.Condition{cond("generator", g.gen), cond(g.kind+"_bits", bits), cond(g.kind+"_encoding", enc)},
				})
			}
		}
	}
	return fields
}

// TestPassgenGeneratedFields pins the shape the launcher depends on: 28
// fields, the 20 generated ones unlabelled and placed after key_encoding.
func TestPassgenGeneratedFields(t *testing.T) {
	p := startPassgen(t, false)
	rows := p.query(t, "pass")
	if len(rows) != 1 || rows[0].Form == nil {
		t.Fatalf("rows = %+v, want one form row", rows)
	}
	fields := rows[0].Form.Fields
	if len(fields) != 28 {
		t.Fatalf("%d fields, want 28", len(fields))
	}
	if fields[7].Key != "key_encoding" {
		t.Fatalf("field 7 = %q, want key_encoding", fields[7].Key)
	}
	for _, f := range fields[8:] {
		if !strings.HasPrefix(f.Key, "gen_") || f.Generate == nil || f.Label != "" || len(f.ShowWhen) == 0 {
			t.Errorf("field %+v: want an unlabelled gen_* field with a spec and show_when", f)
		}
	}
}

// TestPassgenSubmit proves the plugin copies exactly the generated value for
// the chosen options, through wl-copy's stdin only.
func TestPassgenSubmit(t *testing.T) {
	p := startPassgen(t, false)
	p.query(t, "pass") // starts the plugin
	text := map[string]string{"generator": "Text password", "length": "24"}
	with := func(base map[string]string, kv ...string) map[string]string {
		m := map[string]string{}
		for k, v := range base {
			m[k] = v
		}
		for i := 0; i < len(kv); i += 2 {
			m[kv[i]] = kv[i+1]
		}
		return m
	}
	for _, tc := range []struct {
		name    string
		values  map[string]string
		summary string
		want    string // "" means nothing is copied
	}{
		{"text symbols on, ambiguous off", with(text, "symbols", "true", "ambiguous", "false", "gen_text_on_off", "Ab1"+passgenSymbols),
			"Password copied", "Ab1" + passgenSymbols},
		{"text symbols off", with(text, "symbols", "false", "ambiguous", "false", "gen_text_off_off", "Abc123xyz"),
			"Password copied", "Abc123xyz"},
		{"text ambiguous on", with(text, "symbols", "true", "ambiguous", "true", "gen_text_on_on", "Ab2<&>"),
			"Password copied", "Ab2<&>"},
		{"text symbols off ambiguous on", with(text, "symbols", "false", "ambiguous", "true", "gen_text_off_on", "Zz23"),
			"Password copied", "Zz23"},
		{"text checkboxes absent", with(text, "gen_text_on_off", "dflt$%^"), "Password copied", "dflt$%^"},
		{"expected key wins over others", with(text, "symbols", "false", "gen_text_off_off", "right", "gen_jwt_256_hex", "wrong"),
			"Password copied", "right"},
		{"uuid", map[string]string{"generator": "UUIDv4", "gen_uuid": "0f1e2d3c-4b5a-4968-a776-655443322110"},
			"UUID copied", "0f1e2d3c-4b5a-4968-a776-655443322110"},
		{"jwt 512 hex", map[string]string{"generator": "JWT secret", "jwt_bits": "512", "jwt_encoding": "hex", "gen_jwt_512_hex": "abcdef0123"},
			"JWT secret copied", "abcdef0123"},
		{"jwt 256 base64", map[string]string{"generator": "JWT secret", "jwt_bits": "256", "jwt_encoding": "base64", "gen_jwt_256_base64": "q+/w=="},
			"JWT secret copied", "q+/w=="},
		{"jwt bad bits and encoding default", map[string]string{"generator": "JWT secret", "jwt_bits": "100", "jwt_encoding": "rot13",
			"gen_jwt_256_base64url": "dflt-_jwt", "gen_jwt_512_hex": "zz"},
			"JWT secret copied", "dflt-_jwt"},
		{"key 128 base64url", map[string]string{"generator": "Key", "key_bits": "128", "key_encoding": "base64url", "gen_key_128_base64url": "k-_ey"},
			"Key copied", "k-_ey"},
		{"key bad bits and encoding default", map[string]string{"generator": "Key", "key_bits": "1024", "key_encoding": "octal",
			"gen_key_256_hex": "00ff", "gen_key_512_base64": "zz"},
			"Key copied", "00ff"},
		{"unknown generator is a password", map[string]string{"generator": "Diceware", "gen_text_on_off": "fallback1"},
			"Password copied", "fallback1"},
		{"fallback scan finds another key", map[string]string{"generator": "Key", "key_bits": "256", "key_encoding": "hex", "gen_key_512_base64": "other+key"},
			"Key copied", "other+key"},
		{"empty expected value falls back", map[string]string{"generator": "UUIDv4", "gen_uuid": "", "gen_text_on_off": "x<y"},
			"UUID copied", "x<y"},
		{"no generated value", map[string]string{"generator": "Text password", "length": "24"}, "Update banshee to generate", ""},
		{"only empty generated values", map[string]string{"generator": "UUIDv4", "gen_uuid": ""}, "Update banshee to generate", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n, value := p.submit(t, tc.values)
			if n.ID != "passgen:copied" || n.Summary != tc.summary || n.Body != "" || n.TimeoutMS != 3000 {
				t.Errorf("notify = %+v, want %q with no body", n, tc.summary)
			}
			if tc.want == "" {
				if _, err := os.Stat(filepath.Join(p.logDir, "wl-copy.stdin")); !os.IsNotExist(err) {
					t.Fatalf("wl-copy ran (stat err %v), want nothing copied", err)
				}
				return
			}
			if value != tc.want {
				t.Fatalf("copied %q, want %q", value, tc.want)
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

// TestPassgenNoWlCopy proves a missing wl-copy is reported, not fatal.
func TestPassgenNoWlCopy(t *testing.T) {
	p := startPassgen(t, true)
	p.query(t, "pass")
	for i := 0; i < 2; i++ {
		n, value := p.submit(t, map[string]string{"generator": "Text password", "gen_text_on_off": "secret"})
		if n.Summary != "wl-copy not found" || value != "" {
			t.Fatalf("notify = %+v, copied %q; want wl-copy not found", n, value)
		}
	}
}
