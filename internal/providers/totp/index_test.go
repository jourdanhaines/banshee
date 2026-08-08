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

	idx := Index{V: IndexVersion, Backend: "plaintext", Entries: []Entry{
		{Name: "github", Issuer: "GitHub", Created: time.Date(2026, 8, 8, 9, 30, 0, 0, time.UTC)},
		{Name: "aws", Digits: 8, Period: 60, Algorithm: AlgSHA512},
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
