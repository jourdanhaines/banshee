package totp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestLoadIndex(t *testing.T) {
	created := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name    string
		write   string // file contents; "" means write no file at all
		want    Index
		wantErr string
	}{
		{
			name:  "missing file yields empty index",
			write: "",
			want:  Index{V: IndexVersion},
		},
		{
			name:  "full index",
			write: `{"v":1,"backend":"keyring","entries":[{"name":"github","issuer":"GitHub","digits":8,"period":60,"algorithm":"SHA256","created":"2026-08-08T12:00:00Z"}]}`,
			want: Index{V: 1, Backend: "keyring", Entries: []Entry{{
				Name: "github", Issuer: "GitHub", Digits: 8, Period: 60,
				Algorithm: "SHA256", Created: created,
			}}},
		},
		{
			name:  "several managers and a per-entry backend",
			write: `{"v":1,"backend":"keyring","backends":["plaintext"],"entries":[{"name":"github"},{"name":"aws","backend":"plaintext"}]}`,
			want: Index{V: 1, Backend: "keyring", Backends: []string{"plaintext"}, Entries: []Entry{
				{Name: "github"},
				{Name: "aws", Backend: "plaintext"},
			}},
		},
		{
			// The shape every index written before multiple managers existed has:
			// it must load as exactly one configured manager, with no upgrade step.
			name:  "legacy single-manager file",
			write: `{"v":1,"backend":"keyring","entries":[{"name":"github"}]}`,
			want:  Index{V: 1, Backend: "keyring", Entries: []Entry{{Name: "github"}}},
		},
		{
			name:  "unknown fields tolerated",
			write: `{"v":1,"backend":"plaintext","future":{"nested":true},"entries":[{"name":"a","icon":"x","tags":["y"]}]}`,
			want:  Index{V: 1, Backend: "plaintext", Entries: []Entry{{Name: "a"}}},
		},
		{
			name:  "missing version treated as v1",
			write: `{"backend":"plaintext"}`,
			want:  Index{V: 1, Backend: "plaintext"},
		},
		{
			name:    "future version refused",
			write:   `{"v":2,"backend":"nimbus"}`,
			wantErr: "version 2",
		},
		{
			name:    "not json",
			write:   `nope`,
			wantErr: "not valid JSON",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "totp.json")
			if tt.write != "" {
				if err := os.WriteFile(path, []byte(tt.write), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			got, err := LoadIndex(path)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("LoadIndex = %+v, want error", got)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error %q does not mention %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("LoadIndex: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("LoadIndex = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// TestSaveIndexRoundTrip covers the write path end to end: the file lands with
// the documented mode, no temp file is left behind, and reading it back yields
// exactly what was written.
func TestSaveIndexRoundTrip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "banshee")
	path := filepath.Join(dir, "totp.json")

	idx := Index{V: IndexVersion, Backend: "plaintext", Backends: []string{"keyring"}, Entries: []Entry{
		{Name: "github", Issuer: "GitHub", Created: time.Date(2026, 8, 8, 9, 30, 0, 0, time.UTC)},
		{Name: "aws", Digits: 8, Period: 60, Algorithm: AlgSHA512, Backend: "keyring"},
	}}
	if err := SaveIndex(path, idx); err != nil {
		t.Fatalf("SaveIndex: %v", err)
	}

	got, err := LoadIndex(path)
	if err != nil {
		t.Fatalf("LoadIndex: %v", err)
	}
	if !reflect.DeepEqual(got, idx) {
		t.Fatalf("round trip = %+v, want %+v", got, idx)
	}

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o644 {
		t.Errorf("index mode = %o, want 644 (metadata is not secret)", perm)
	}
	if di, err := os.Stat(dir); err != nil {
		t.Fatal(err)
	} else if perm := di.Mode().Perm(); perm != 0o755 {
		t.Errorf("index dir mode = %o, want 755", perm)
	}

	names, err := filepath.Glob(filepath.Join(dir, "*.tmp.*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 0 {
		t.Errorf("temp files left behind: %v", names)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(data), "}\n") {
		t.Errorf("index does not end with an indented object and newline: %q", string(data))
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if raw["v"] != float64(IndexVersion) {
		t.Errorf("v = %v, want %d", raw["v"], IndexVersion)
	}
	if raw["backend"] != "plaintext" {
		t.Errorf("backend = %v, want plaintext", raw["backend"])
	}
	// No secret material may ever reach this file.
	if strings.Contains(strings.ToLower(string(data)), "secret") {
		t.Errorf("index mentions a secret: %q", string(data))
	}
}

func TestSaveIndexVersionDefaulted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "totp.json")
	if err := SaveIndex(path, Index{Backend: "plaintext"}); err != nil {
		t.Fatalf("SaveIndex: %v", err)
	}
	got, err := LoadIndex(path)
	if err != nil {
		t.Fatalf("LoadIndex: %v", err)
	}
	if got.V != IndexVersion {
		t.Fatalf("V = %d, want %d", got.V, IndexVersion)
	}
}

func TestSaveIndexOverwrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "totp.json")
	if err := SaveIndex(path, Index{V: 1, Entries: []Entry{{Name: "a"}, {Name: "b"}}}); err != nil {
		t.Fatalf("SaveIndex: %v", err)
	}
	if err := SaveIndex(path, Index{V: 1, Entries: []Entry{{Name: "c"}}}); err != nil {
		t.Fatalf("SaveIndex: %v", err)
	}
	got, err := LoadIndex(path)
	if err != nil {
		t.Fatalf("LoadIndex: %v", err)
	}
	if len(got.Entries) != 1 || got.Entries[0].Name != "c" {
		t.Fatalf("entries = %+v, want just c", got.Entries)
	}
}

// TestSaveIndexPreservesUnknownFields is the forward-compatibility rule on the
// write path: an additive field keeps v: 1 by this repo's convention, so
// LoadIndex's version guard never fires for it and a load-modify-save through
// Index would silently delete it. Both a hand-added key and a key from a newer
// banshee must survive an unrelated write.
func TestSaveIndexPreservesUnknownFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "totp.json")
	const before = `{
  "v": 1,
  "backend": "plaintext",
  "comment": "hand written",
  "future": {"nested": true},
  "entries": [
    {"name": "github", "note": "work account", "digits": 8}
  ]
}`
	if err := os.WriteFile(path, []byte(before), 0o644); err != nil {
		t.Fatal(err)
	}

	idx, err := LoadIndex(path)
	if err != nil {
		t.Fatalf("LoadIndex: %v", err)
	}
	if err := idx.Add(Entry{Name: "aws"}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := SaveIndex(path, idx); err != nil {
		t.Fatalf("SaveIndex: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got["comment"] != "hand written" {
		t.Errorf("top-level \"comment\" = %v, want it preserved", got["comment"])
	}
	if nested, ok := got["future"].(map[string]any); !ok || nested["nested"] != true {
		t.Errorf("top-level \"future\" = %v, want it preserved", got["future"])
	}
	entries, ok := got["entries"].([]any)
	if !ok || len(entries) != 2 {
		t.Fatalf("entries = %v, want the original plus the new one", got["entries"])
	}
	first, ok := entries[0].(map[string]any)
	if !ok {
		t.Fatalf("first entry = %v, want an object", entries[0])
	}
	if first["note"] != "work account" {
		t.Errorf("entry \"note\" = %v, want it preserved", first["note"])
	}
	if first["digits"] != float64(8) {
		t.Errorf("entry \"digits\" = %v, want the value this build owns rewritten as 8", first["digits"])
	}
	second, ok := entries[1].(map[string]any)
	if !ok || second["name"] != "aws" {
		t.Fatalf("second entry = %v, want the newly added aws", entries[1])
	}
	if _, leaked := second["note"]; leaked {
		t.Errorf("new entry inherited another entry's unknown key: %v", second)
	}
}

// TestSaveIndexRefusesNonObject guards the merge: a file that is not an index
// must not be silently replaced by one.
func TestSaveIndexRefusesNonObject(t *testing.T) {
	tests := []struct {
		name  string
		write string
		wantS string
	}{
		{name: "not an object", write: `["a"]`, wantS: "not a JSON object"},
		{name: "entries is not an array", write: `{"v":1,"entries":{"a":1}}`, wantS: "malformed \"entries\""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "totp.json")
			if err := os.WriteFile(path, []byte(tt.write), 0o644); err != nil {
				t.Fatal(err)
			}
			err := SaveIndex(path, Index{V: 1, Backend: "plaintext"})
			if err == nil || !strings.Contains(err.Error(), tt.wantS) {
				t.Fatalf("SaveIndex error = %v, want one mentioning %q", err, tt.wantS)
			}
			data, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if string(data) != tt.write {
				t.Errorf("file was rewritten to %q, want it left alone", string(data))
			}
		})
	}
}

func TestSaveIndexNoPath(t *testing.T) {
	if err := SaveIndex("", Index{V: 1}); err == nil {
		t.Fatal("SaveIndex(\"\") = nil, want error")
	}
}

func TestIndexAdd(t *testing.T) {
	tests := []struct {
		name      string
		start     Index
		add       Entry
		wantErr   string
		wantNames []string
	}{
		{
			name: "first entry", start: Index{V: 1},
			add:       Entry{Name: "github"},
			wantNames: []string{"github"},
		},
		{
			name: "appends in order", start: Index{V: 1, Entries: []Entry{{Name: "aws"}}},
			add:       Entry{Name: "github"},
			wantNames: []string{"aws", "github"},
		},
		{
			name: "name trimmed", start: Index{V: 1},
			add:       Entry{Name: "  github  "},
			wantNames: []string{"github"},
		},
		{
			name: "duplicate rejected", start: Index{V: 1, Entries: []Entry{{Name: "github"}}},
			add: Entry{Name: "github"}, wantErr: "already exists",
			wantNames: []string{"github"},
		},
		{
			name: "duplicate rejected case insensitively", start: Index{V: 1, Entries: []Entry{{Name: "GitHub"}}},
			add: Entry{Name: "github"}, wantErr: "already exists",
			wantNames: []string{"GitHub"},
		},
		{
			name: "empty name rejected", start: Index{V: 1},
			add: Entry{Name: "   "}, wantErr: "needs a name",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			idx := tt.start
			err := idx.Add(tt.add)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("Add = nil, want error")
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error %q does not mention %q", err, tt.wantErr)
				}
			} else if err != nil {
				t.Fatalf("Add: %v", err)
			}
			var names []string
			for _, e := range idx.Entries {
				names = append(names, e.Name)
			}
			if !reflect.DeepEqual(names, tt.wantNames) {
				t.Fatalf("names = %v, want %v", names, tt.wantNames)
			}
		})
	}
}

// TestIndexConfigured pins the one place the two manager fields are read
// together: the default is always first, blanks and duplicates never reach the
// caller, and a file from before this build reports exactly its one manager —
// which is what makes every multi-manager path degrade instead of branching.
func TestIndexConfigured(t *testing.T) {
	tests := []struct {
		name string
		idx  Index
		want []string
	}{
		{name: "empty index", idx: Index{V: 1}, want: nil},
		{name: "legacy single manager", idx: Index{V: 1, Backend: "keyring"}, want: []string{"keyring"}},
		{
			name: "default first, then the rest in order",
			idx:  Index{V: 1, Backend: "keyring", Backends: []string{"plaintext", "nimbus"}},
			want: []string{"keyring", "plaintext", "nimbus"},
		},
		{
			name: "a duplicate of the default is dropped",
			idx:  Index{V: 1, Backend: "keyring", Backends: []string{"keyring", "plaintext"}},
			want: []string{"keyring", "plaintext"},
		},
		{
			name: "duplicates within the list are dropped",
			idx:  Index{V: 1, Backend: "keyring", Backends: []string{"plaintext", "plaintext"}},
			want: []string{"keyring", "plaintext"},
		},
		{
			name: "blanks are dropped and names trimmed",
			idx:  Index{V: 1, Backend: " keyring ", Backends: []string{"", "  ", "plaintext"}},
			want: []string{"keyring", "plaintext"},
		},
		{
			// A hand-edited file that emptied "backend" but kept "backends" still
			// has to name a default, or every entry would route nowhere.
			name: "an empty default promotes the first of the rest",
			idx:  Index{V: 1, Backends: []string{"plaintext", "nimbus"}},
			want: []string{"plaintext", "nimbus"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.idx.Configured()
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("Configured() = %v, want %v", got, tt.want)
			}
			wantDefault := ""
			if len(tt.want) > 0 {
				wantDefault = tt.want[0]
			}
			if got := tt.idx.DefaultBackend(); got != wantDefault {
				t.Errorf("DefaultBackend() = %q, want %q", got, wantDefault)
			}
		})
	}
}

// TestIndexAddBackend covers the invariant the on-disk format leans on: the
// legacy Backend field always holds Configured()[0], so a single-manager index
// keeps the exact shape an older banshee reads and a save can never delete the
// only key that says where the seeds are.
func TestIndexAddBackend(t *testing.T) {
	tests := []struct {
		name         string
		start        Index
		add          string
		wantBackend  string
		wantBackends []string
	}{
		{
			name:  "the first manager lands in the legacy field alone",
			start: Index{V: 1}, add: "keyring",
			wantBackend: "keyring",
		},
		{name: "the second lands in the list", start: Index{V: 1, Backend: "keyring"}, add: "plaintext",
			wantBackend: "keyring", wantBackends: []string{"plaintext"}},
		{
			name:  "appending is idempotent for the default",
			start: Index{V: 1, Backend: "keyring", Backends: []string{"plaintext"}}, add: "keyring",
			wantBackend: "keyring", wantBackends: []string{"plaintext"},
		},
		{
			name:  "appending is idempotent for a listed manager",
			start: Index{V: 1, Backend: "keyring", Backends: []string{"plaintext"}}, add: "plaintext",
			wantBackend: "keyring", wantBackends: []string{"plaintext"},
		},
		{
			name:  "a third manager appends in order",
			start: Index{V: 1, Backend: "keyring", Backends: []string{"plaintext"}}, add: "nimbus",
			wantBackend: "keyring", wantBackends: []string{"plaintext", "nimbus"},
		},
		{
			// A hand-edited file that dropped "backend": filling it in would move
			// the default onto the new manager and send every entry that names
			// none looking in a vault that has never held it.
			name:  "an empty legacy field is not filled in behind an existing default",
			start: Index{V: 1, Backends: []string{"plaintext"}}, add: "keyring",
			wantBackend: "", wantBackends: []string{"plaintext", "keyring"},
		},
		{name: "the name is trimmed", start: Index{V: 1}, add: "  keyring  ", wantBackend: "keyring"},
		{name: "a blank name is ignored", start: Index{V: 1, Backend: "keyring"}, add: "   ", wantBackend: "keyring"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			idx := tt.start
			idx.AddBackend(tt.add)
			if idx.Backend != tt.wantBackend {
				t.Errorf("Backend = %q, want %q", idx.Backend, tt.wantBackend)
			}
			if !reflect.DeepEqual(idx.Backends, tt.wantBackends) {
				t.Errorf("Backends = %v, want %v", idx.Backends, tt.wantBackends)
			}
			// The invariant, as far as it can hold: whenever the legacy field is
			// populated at all it is the default, so an older banshee reading the
			// file finds the manager new entries go to.
			if c := idx.Configured(); idx.Backend != "" && (len(c) == 0 || c[0] != idx.Backend) {
				t.Errorf("INVARIANT broken: Configured() = %v, Backend = %q", c, idx.Backend)
			}
			// Adding a manager never re-routes the entries that name none.
			if before, after := tt.start.DefaultBackend(), idx.DefaultBackend(); before != "" && before != after {
				t.Errorf("DefaultBackend moved from %q to %q; existing entries would be looked up elsewhere", before, after)
			}
		})
	}
}

// TestEntryBackendOr pins the fallback every seed read and write goes through: an
// empty Backend means "the index's default", never "nowhere", which is what keeps
// entries written before multiple managers existed readable.
func TestEntryBackendOr(t *testing.T) {
	tests := []struct {
		name  string
		entry Entry
		def   string
		want  string
	}{
		{name: "empty falls back", entry: Entry{Name: "x"}, def: "keyring", want: "keyring"},
		{name: "blank falls back", entry: Entry{Name: "x", Backend: "   "}, def: "keyring", want: "keyring"},
		{name: "explicit wins", entry: Entry{Name: "x", Backend: "plaintext"}, def: "keyring", want: "plaintext"},
		{name: "explicit is trimmed", entry: Entry{Name: "x", Backend: " plaintext "}, def: "keyring", want: "plaintext"},
		{name: "no default and no backend routes nowhere", entry: Entry{Name: "x"}, def: "", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.entry.BackendOr(tt.def); got != tt.want {
				t.Fatalf("BackendOr(%q) = %q, want %q", tt.def, got, tt.want)
			}
		})
	}
}

func TestIndexFind(t *testing.T) {
	idx := Index{V: 1, Entries: []Entry{
		{Name: "GitHub", Issuer: "GitHub"},
		{Name: "aws", Digits: 8},
	}}
	tests := []struct {
		name     string
		query    string
		wantOK   bool
		wantName string
	}{
		{"exact", "aws", true, "aws"},
		{"case insensitive", "github", true, "GitHub"},
		{"spelling preserved", "GITHUB", true, "GitHub"},
		{"trimmed", "  aws ", true, "aws"},
		{"missing", "gitlab", false, ""},
		{"empty", "", false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := idx.Find(tt.query)
			if ok != tt.wantOK {
				t.Fatalf("Find ok = %v, want %v", ok, tt.wantOK)
			}
			if got.Name != tt.wantName {
				t.Fatalf("Find name = %q, want %q", got.Name, tt.wantName)
			}
		})
	}
}

func TestEntryParams(t *testing.T) {
	tests := []struct {
		name  string
		entry Entry
		want  Params
	}{
		{"zero entry uses defaults", Entry{Name: "x"}, DefaultParams()},
		{"digits only", Entry{Name: "x", Digits: 8}, Params{Digits: 8, Period: 30, Algorithm: AlgSHA1}},
		{"period only", Entry{Name: "x", Period: 60}, Params{Digits: 6, Period: 60, Algorithm: AlgSHA1}},
		{"algorithm only", Entry{Name: "x", Algorithm: AlgSHA512}, Params{Digits: 6, Period: 30, Algorithm: AlgSHA512}},
		{"blank algorithm ignored", Entry{Name: "x", Algorithm: "  "}, DefaultParams()},
		{"all set", Entry{Name: "x", Digits: 8, Period: 15, Algorithm: AlgSHA256}, Params{Digits: 8, Period: 15, Algorithm: AlgSHA256}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.entry.Params(); got != tt.want {
				t.Fatalf("Params = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// TestEntryParamsProduceCodes guards the defaulting contract end to end: an
// entry stored with no algorithm fields at all must still yield the RFC
// vector, since that is what an old or hand-written index looks like.
func TestEntryParamsProduceCodes(t *testing.T) {
	got, err := Code(seedSHA1, time.Unix(59, 0).UTC(), Entry{Name: "rfc"}.Params())
	if err != nil {
		t.Fatalf("Code: %v", err)
	}
	if want := "287082"; got != want {
		t.Fatalf("Code = %q, want %q", got, want)
	}
}
