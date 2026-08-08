package secrets

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// tempStore returns a Plaintext rooted in a fresh temp directory, plus the
// file path, so no test can reach the developer's real secrets file.
func tempStore(t *testing.T) (*Plaintext, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "secrets", "secrets.json")
	return NewPlaintext(path), path
}

func TestPlaintextRoundtrip(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		value string
	}{
		{"simple", "totp/github", "JBSWY3DPEHPK3PXP"},
		{"slashes in name", "totp/work/vpn", "GEZDGNBVGY3TQOJQ"},
		{"unicode name", "totp/café", "MZXW6==="},
		{"empty value", "totp/blank", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, _ := tempStore(t)
			ctx := context.Background()
			if err := p.Set(ctx, tt.key, tt.value, Credential{}); err != nil {
				t.Fatalf("Set: %v", err)
			}
			got, err := p.Get(ctx, tt.key, Credential{})
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if got != tt.value {
				t.Fatalf("Get = %q, want %q", got, tt.value)
			}
			if err := p.Delete(ctx, tt.key, Credential{}); err != nil {
				t.Fatalf("Delete: %v", err)
			}
			if _, err := p.Get(ctx, tt.key, Credential{}); !errors.Is(err, ErrNotFound) {
				t.Fatalf("Get after Delete err = %v, want ErrNotFound", err)
			}
		})
	}
}

func TestPlaintextSetIsIdempotentAndReplaces(t *testing.T) {
	p, _ := tempStore(t)
	ctx := context.Background()
	for _, v := range []string{"one", "one", "two"} {
		if err := p.Set(ctx, "totp/a", v, Credential{}); err != nil {
			t.Fatalf("Set %q: %v", v, err)
		}
	}
	got, err := p.Get(ctx, "totp/a", Credential{})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "two" {
		t.Fatalf("Get = %q, want %q", got, "two")
	}
}

func TestPlaintextFileModes(t *testing.T) {
	p, path := tempStore(t)
	if err := p.Set(context.Background(), "totp/a", "SECRET", Credential{}); err != nil {
		t.Fatalf("Set: %v", err)
	}

	tests := []struct {
		name string
		path string
		want os.FileMode
	}{
		{"secrets file is owner-only", path, 0o600},
		{"secrets dir is owner-only", filepath.Dir(path), 0o700},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fi, err := os.Stat(tt.path)
			if err != nil {
				t.Fatalf("Stat: %v", err)
			}
			if got := fi.Mode().Perm(); got != tt.want {
				t.Fatalf("mode = %04o, want %04o", got, tt.want)
			}
		})
	}
}

// TestPlaintextPreservesUnknownFields is the forward-compatibility rule on the
// write path. writable() only refuses a higher schema version, and an additive
// field keeps v: 1 by this repo's convention, so a load-modify-save through
// plaintextFile would delete it without ever tripping that guard.
func TestPlaintextPreservesUnknownFields(t *testing.T) {
	p, path := tempStore(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	const before = `{"v":1,"comment":"hand written","future":{"nested":true},"secrets":{"totp/a":"A"}}`
	if err := os.WriteFile(path, []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	if err := p.Set(ctx, "totp/b", "B", Credential{}); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := p.Delete(ctx, "totp/a", Credential{}); err != nil {
		t.Fatalf("Delete: %v", err)
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
		t.Errorf("\"comment\" = %v, want it preserved", got["comment"])
	}
	if nested, ok := got["future"].(map[string]any); !ok || nested["nested"] != true {
		t.Errorf("\"future\" = %v, want it preserved", got["future"])
	}
	vals, ok := got["secrets"].(map[string]any)
	if !ok {
		t.Fatalf("\"secrets\" = %v, want an object", got["secrets"])
	}
	if vals["totp/b"] != "B" {
		t.Errorf("secrets = %v, want the new value written", vals)
	}
	if _, still := vals["totp/a"]; still {
		t.Errorf("secrets = %v, want the deleted key gone", vals)
	}
}

func TestPlaintextLeavesNoTempFile(t *testing.T) {
	p, path := tempStore(t)
	ctx := context.Background()
	if err := p.Set(ctx, "totp/a", "A", Credential{}); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := p.Set(ctx, "totp/b", "B", Credential{}); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := p.Delete(ctx, "totp/a", Credential{}); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	ents, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range ents {
		if strings.Contains(e.Name(), ".tmp.") {
			t.Fatalf("temp residue left behind: %s", e.Name())
		}
	}
	if len(ents) != 1 {
		t.Fatalf("dir holds %d entries, want just the secrets file", len(ents))
	}
}

func TestPlaintextMissingAndUnknown(t *testing.T) {
	tests := []struct {
		name    string
		seed    string // file contents; empty means no file at all
		op      func(p *Plaintext) error
		wantErr error
	}{
		{
			name:    "missing file reads as empty",
			op:      func(p *Plaintext) error { _, err := p.Get(context.Background(), "totp/x", Credential{}); return err },
			wantErr: ErrNotFound,
		},
		{
			name:    "missing key",
			seed:    `{"v":1,"secrets":{"totp/other":"X"}}`,
			op:      func(p *Plaintext) error { _, err := p.Get(context.Background(), "totp/x", Credential{}); return err },
			wantErr: ErrNotFound,
		},
		{
			name:    "delete of missing key",
			seed:    `{"v":1,"secrets":{}}`,
			op:      func(p *Plaintext) error { return p.Delete(context.Background(), "totp/x", Credential{}) },
			wantErr: ErrNotFound,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, path := tempStore(t)
			if tt.seed != "" {
				seedFile(t, path, tt.seed)
			}
			if err := tt.op(p); !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestPlaintextRefusesNewerSchemaWrite(t *testing.T) {
	tests := []struct {
		name string
		op   func(p *Plaintext) error
	}{
		{"set", func(p *Plaintext) error { return p.Set(context.Background(), "totp/a", "N", Credential{}) }},
		{"delete", func(p *Plaintext) error { return p.Delete(context.Background(), "totp/a", Credential{}) }},
	}
	const seed = `{"v":2,"secrets":{"totp/a":"OLD"},"future":{"k":1}}`
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, path := tempStore(t)
			seedFile(t, path, seed)

			err := tt.op(p)
			if err == nil {
				t.Fatal("op on a v2 file succeeded, want refusal")
			}
			if !strings.Contains(err.Error(), "v2") {
				t.Fatalf("err = %v, want it to name the schema version", err)
			}
			b, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatalf("ReadFile: %v", readErr)
			}
			if string(b) != seed {
				t.Fatalf("file was rewritten:\n got %s\nwant %s", b, seed)
			}
		})
	}
}

func TestPlaintextReadsNewerSchemaAndUnknownFields(t *testing.T) {
	tests := []struct {
		name string
		seed string
		key  string
		want string
	}{
		{"unknown top-level field", `{"v":1,"kdf":"argon2","secrets":{"totp/a":"AAA"}}`, "totp/a", "AAA"},
		{"newer schema still readable", `{"v":2,"secrets":{"totp/a":"BBB"}}`, "totp/a", "BBB"},
		{"no secrets object", `{"v":1}`, "totp/a", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, path := tempStore(t)
			seedFile(t, path, tt.seed)

			got, err := p.Get(context.Background(), tt.key, Credential{})
			if tt.want == "" {
				if !errors.Is(err, ErrNotFound) {
					t.Fatalf("err = %v, want ErrNotFound", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if got != tt.want {
				t.Fatalf("Get = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPlaintextUnknownFieldsSurviveRewrite(t *testing.T) {
	// A v1 file with extra keys is rewritable, and the write must produce a
	// file this build can read back — unknown keys it does not model are
	// dropped, which is why the version gate above blocks v>1 rewrites.
	p, path := tempStore(t)
	seedFile(t, path, `{"v":1,"note":"hi","secrets":{"totp/a":"AAA"}}`)

	if err := p.Set(context.Background(), "totp/b", "BBB", Credential{}); err != nil {
		t.Fatalf("Set: %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var f plaintextFile
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if f.V != plaintextVersion {
		t.Fatalf("v = %d, want %d", f.V, plaintextVersion)
	}
	if f.Secrets["totp/a"] != "AAA" || f.Secrets["totp/b"] != "BBB" {
		t.Fatalf("secrets = %v, want both entries", f.Secrets)
	}
}

func TestPlaintextHonorsContext(t *testing.T) {
	p, _ := tempStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	tests := []struct {
		name string
		op   func() error
	}{
		{"get", func() error { _, err := p.Get(ctx, "totp/a", Credential{}); return err }},
		{"set", func() error { return p.Set(ctx, "totp/a", "A", Credential{}) }},
		{"delete", func() error { return p.Delete(ctx, "totp/a", Credential{}) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.op(); !errors.Is(err, context.Canceled) {
				t.Fatalf("err = %v, want context.Canceled", err)
			}
		})
	}
}

func TestPlaintextMetadata(t *testing.T) {
	p := NewPlaintext("")
	if p.Name() != BackendPlaintext {
		t.Fatalf("Name = %q, want %q", p.Name(), BackendPlaintext)
	}
	if p.AuthPerAccess() {
		t.Fatal("AuthPerAccess = true, want false for a local file backend")
	}
}

// seedFile writes contents at path, creating parent directories.
func seedFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}
