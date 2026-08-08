package connectors

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSaveRepoBinding(t *testing.T) {
	tests := []struct {
		name    string
		initial string // existing config.json contents; "" means no file
		id      string
		binding string
		wantErr bool
		check   func(t *testing.T, raw []byte)
	}{
		{
			name:    "creates file and directory with v 1",
			id:      "railway",
			binding: "proj-123",
			check: func(t *testing.T, raw []byte) {
				var got struct {
					V          int               `json:"v"`
					Connectors map[string]string `json:"connectors"`
				}
				if err := json.Unmarshal(raw, &got); err != nil {
					t.Fatalf("unmarshal: %v", err)
				}
				if got.V != 1 {
					t.Errorf("v = %d, want 1", got.V)
				}
				if got.Connectors["railway"] != "proj-123" {
					t.Errorf("binding = %q, want proj-123", got.Connectors["railway"])
				}
			},
		},
		{
			name:    "updates existing binding",
			initial: `{"v":1,"connectors":{"railway":"old"}}`,
			id:      "railway",
			binding: "new",
			check: func(t *testing.T, raw []byte) {
				rc := loadConnectors(t, raw)
				if rc["railway"] != `"new"` {
					t.Errorf("binding = %s, want \"new\"", rc["railway"])
				}
			},
		},
		{
			name:    "preserves unknown top-level and nested keys",
			initial: `{"v":1,"future_key":{"a":[1,2]},"connectors":{"other":{"structured":true}}}`,
			id:      "railway",
			binding: "proj",
			check: func(t *testing.T, raw []byte) {
				var top map[string]json.RawMessage
				if err := json.Unmarshal(raw, &top); err != nil {
					t.Fatalf("unmarshal: %v", err)
				}
				if got := string(compact(t, top["future_key"])); got != `{"a":[1,2]}` {
					t.Errorf("future_key = %s, want preserved", got)
				}
				rc := loadConnectors(t, raw)
				if rc["other"] != `{"structured":true}` {
					t.Errorf("connectors.other = %s, want preserved verbatim", rc["other"])
				}
				if rc["railway"] != `"proj"` {
					t.Errorf("connectors.railway = %s, want \"proj\"", rc["railway"])
				}
			},
		},
		{
			name:    "keeps an existing v verbatim",
			initial: `{"v":2,"connectors":{}}`,
			id:      "railway",
			binding: "proj",
			check: func(t *testing.T, raw []byte) {
				var top map[string]json.RawMessage
				if err := json.Unmarshal(raw, &top); err != nil {
					t.Fatalf("unmarshal: %v", err)
				}
				if string(top["v"]) != "2" {
					t.Errorf("v = %s, want 2", top["v"])
				}
			},
		},
		{
			name:    "non-object file is refused untouched",
			initial: `[1,2,3]`,
			id:      "railway",
			binding: "proj",
			wantErr: true,
			check: func(t *testing.T, raw []byte) {
				if string(raw) != `[1,2,3]` {
					t.Errorf("file was modified: %s", raw)
				}
			},
		},
		{
			name:    "non-object connectors key is refused",
			initial: `{"connectors":"nope"}`,
			id:      "railway",
			binding: "proj",
			wantErr: true,
		},
		{
			name:    "empty binding rejected",
			id:      "railway",
			binding: "   ",
			wantErr: true,
		},
		{
			name:    "empty id rejected",
			id:      "",
			binding: "proj",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := t.TempDir()
			path := RepoConfigPath(repo)
			if tt.initial != "" {
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(tt.initial), 0o644); err != nil {
					t.Fatal(err)
				}
			}

			err := SaveRepoBinding(repo, tt.id, tt.binding)
			if (err != nil) != tt.wantErr {
				t.Fatalf("SaveRepoBinding error = %v, wantErr %v", err, tt.wantErr)
			}

			if tt.check != nil {
				raw, rerr := os.ReadFile(path)
				if rerr != nil {
					t.Fatalf("read back: %v", rerr)
				}
				tt.check(t, raw)
			}

			entries, derr := os.ReadDir(filepath.Dir(path))
			if derr == nil {
				for _, e := range entries {
					if strings.Contains(e.Name(), ".tmp.") {
						t.Errorf("tmp residue left behind: %s", e.Name())
					}
				}
			}
		})
	}
}

// TestSaveRepoBindingRoundTrip asserts the written file loads through the
// normal read path.
func TestSaveRepoBindingRoundTrip(t *testing.T) {
	repo := t.TempDir()
	if err := SaveRepoBinding(repo, "railway", "proj-9"); err != nil {
		t.Fatal(err)
	}
	rc, err := LoadRepoConfig(repo)
	if err != nil {
		t.Fatal(err)
	}
	if rc.V != 1 || rc.Connectors["railway"] != "proj-9" {
		t.Errorf("LoadRepoConfig = %+v, want v1 railway=proj-9", rc)
	}
}

// loadConnectors returns the raw connectors object of a config file, keyed by
// connector id with verbatim JSON values.
func loadConnectors(t *testing.T, raw []byte) map[string]string {
	t.Helper()
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		t.Fatalf("unmarshal top: %v", err)
	}
	var conns map[string]json.RawMessage
	if err := json.Unmarshal(top["connectors"], &conns); err != nil {
		t.Fatalf("unmarshal connectors: %v", err)
	}
	out := map[string]string{}
	for k, v := range conns {
		out[k] = string(compact(t, v))
	}
	return out
}

func compact(t *testing.T, raw json.RawMessage) []byte {
	t.Helper()
	var buf strings.Builder
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		t.Fatalf("decode: %v", err)
	}
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	buf.Write(b)
	return []byte(buf.String())
}
