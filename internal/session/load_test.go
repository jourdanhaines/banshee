package session

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParseSessionValidation(t *testing.T) {
	tests := []struct {
		name    string
		json    string
		wantErr string // substring; empty means the config must validate
	}{
		{
			name: "minimal valid",
			json: `{"v":1,"name":"demo","windows":[{"panes":[{"run":"nvim"}]}]}`,
		},
		{
			name: "unknown keys are ignored",
			json: `{"v":1,"name":"demo","future":true,"windows":[{"panes":[{"run":"a","future":1}]}]}`,
		},
		{
			name:    "invalid JSON",
			json:    `{"v":1,`,
			wantErr: "invalid JSON",
		},
		{
			name:    "0.2.0 sessions wrapper",
			json:    `{"v":1,"sessions":{"demo":{"windows":[]}}}`,
			wantErr: "0.2.0",
		},
		{
			name:    "missing v",
			json:    `{"name":"demo","windows":[{"panes":[{"run":"a"}]}]}`,
			wantErr: `missing or unsupported "v"`,
		},
		{
			name:    "unsupported v",
			json:    `{"v":2,"name":"demo","windows":[{"panes":[{"run":"a"}]}]}`,
			wantErr: `missing or unsupported "v"`,
		},
		{
			name:    "missing name",
			json:    `{"v":1,"windows":[{"panes":[{"run":"a"}]}]}`,
			wantErr: `missing non-empty "name"`,
		},
		{
			name:    "empty name",
			json:    `{"v":1,"name":"","windows":[{"panes":[{"run":"a"}]}]}`,
			wantErr: `missing non-empty "name"`,
		},
		{
			name:    "missing windows",
			json:    `{"v":1,"name":"demo"}`,
			wantErr: `"windows" must be a non-empty array`,
		},
		{
			name:    "empty windows",
			json:    `{"v":1,"name":"demo","windows":[]}`,
			wantErr: `"windows" must be a non-empty array`,
		},
		{
			name:    "windows wrong type",
			json:    `{"v":1,"name":"demo","windows":{"a":1}}`,
			wantErr: `"windows" must be a non-empty array`,
		},
		{
			name:    "window without panes",
			json:    `{"v":1,"name":"demo","windows":[{"name":"w"}]}`,
			wantErr: `non-empty "panes" array`,
		},
		{
			name:    "window with empty panes",
			json:    `{"v":1,"name":"demo","windows":[{"panes":[]}]}`,
			wantErr: `non-empty "panes" array`,
		},
		{
			name:    "second window without panes",
			json:    `{"v":1,"name":"demo","windows":[{"panes":[{"run":"a"}]},{"name":"w"}]}`,
			wantErr: `non-empty "panes" array`,
		},
		{
			name: "nested pane arrays validate",
			json: `{"v":1,"name":"demo","windows":[{"panes":[{"run":"a"},[{"run":"b"},{"run":"c"}]]}]}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseSession([]byte(tc.json), "cfg.json")
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %q, want it to contain %q", err, tc.wantErr)
			}
			if !strings.Contains(err.Error(), "cfg.json") {
				t.Errorf("error %q should name the config path", err)
			}
		})
	}
}

func TestParseSessionMigrationErrorType(t *testing.T) {
	_, err := ParseSession([]byte(`{"v":1,"sessions":{}}`), "old.json")
	var me *MigrationError
	if !errors.As(err, &me) {
		t.Fatalf("expected *MigrationError, got %T (%v)", err, err)
	}
	if me.Path != "old.json" {
		t.Errorf("Path = %q", me.Path)
	}
}

func TestParseGroupValidation(t *testing.T) {
	tests := []struct {
		name    string
		json    string
		wantErr string
	}{
		{name: "valid", json: `{"v":1,"name":"work","targets":["a","b"]}`},
		{name: "unknown keys ignored", json: `{"v":1,"name":"work","targets":["a"],"color":"red"}`},
		{name: "invalid JSON", json: `{`, wantErr: "invalid JSON"},
		{name: "missing v", json: `{"name":"work","targets":["a"]}`, wantErr: `missing or unsupported "v"`},
		{name: "missing name", json: `{"v":1,"targets":["a"]}`, wantErr: `missing non-empty "name"`},
		{name: "missing targets", json: `{"v":1,"name":"work"}`, wantErr: `"targets" must be a non-empty array of strings`},
		{name: "empty targets", json: `{"v":1,"name":"work","targets":[]}`, wantErr: `"targets" must be a non-empty array of strings`},
		{name: "non-string targets", json: `{"v":1,"name":"work","targets":[1,2]}`, wantErr: `"targets" must be a non-empty array of strings`},
		{name: "empty string target", json: `{"v":1,"name":"work","targets":["a",""]}`, wantErr: `"targets" must be a non-empty array of strings`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseGroup([]byte(tc.json), "g.json")
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}

func TestDefaultTemplates(t *testing.T) {
	got := string(DefaultTemplate("blacksheep"))
	want := `{
  "v": 1,
  "name": "blacksheep",
  "windows": [
    {
      "name": "<window_name>",
      "panes": [
        { "run": "<target_command>" }
      ]
    }
  ]
}
`
	if got != want {
		t.Errorf("DefaultTemplate =\n%s\nwant\n%s", got, want)
	}
	// The template must be a config the validator accepts.
	if _, err := ParseSession([]byte(got), "t.json"); err != nil {
		t.Errorf("default template does not validate: %v", err)
	}

	gg := string(DefaultGroupTemplate("work", []string{"a", "b"}))
	wantGroup := `{
  "v": 1,
  "name": "work",
  "targets": ["a","b"]
}
`
	if gg != wantGroup {
		t.Errorf("DefaultGroupTemplate =\n%s\nwant\n%s", gg, wantGroup)
	}
	if _, err := ParseGroup([]byte(gg), "g.json"); err != nil {
		t.Errorf("default group template does not validate: %v", err)
	}
}

func TestPaneLeafAndSplit(t *testing.T) {
	s, err := ParseSession([]byte(`{"v":1,"name":"d","windows":[{"panes":[
		{"run":"nvim","cwd":"~/x"},
		[{"run":"a"},{"run":"b"}]
	]}]}`), "c.json")
	if err != nil {
		t.Fatal(err)
	}
	panes := s.Windows[0].Panes
	if panes[0].IsSplit() {
		t.Error("leaf reported as split")
	}
	leaf, err := panes[0].Leaf()
	if err != nil || leaf.Run != "nvim" || leaf.Cwd != "~/x" {
		t.Errorf("Leaf = %+v, err %v", leaf, err)
	}
	if !panes[1].IsSplit() {
		t.Fatal("nested array not reported as split")
	}
	sub, err := panes[1].Split()
	if err != nil || len(sub) != 2 {
		t.Fatalf("Split = %v, err %v", sub, err)
	}
	// Round-tripping keeps the heterogeneous shape.
	b, err := json.Marshal(s.Windows[0].Panes)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `[{"run":"a"},{"run":"b"}]`) {
		t.Errorf("marshalled panes lost the nested array: %s", b)
	}
}

func TestWriteTemplateAndGroup(t *testing.T) {
	dir := t.TempDir()

	created, err := WriteTemplate(dir, "demo")
	if err != nil || !created {
		t.Fatalf("WriteTemplate = %v, %v", created, err)
	}
	created, err = WriteTemplate(dir, "demo")
	if err != nil || created {
		t.Fatalf("second WriteTemplate must not recreate: %v, %v", created, err)
	}
	if _, err := LoadSession(SessionPath(dir, "demo")); err != nil {
		t.Errorf("written template does not load: %v", err)
	}

	if err := WriteGroup(dir, "work", []string{"a", "b"}); err != nil {
		t.Fatal(err)
	}
	g, err := LoadGroup(GroupPath(dir, "work"))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(g.Targets, []string{"a", "b"}) || g.Name != "work" || g.V != 1 {
		t.Errorf("group = %+v", g)
	}
}

func TestListTargetsAndGroups(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"beta.json", "alpha.json", "notes.txt"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(dir, "sub.json"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := ListTargets(dir); !reflect.DeepEqual(got, []string{"alpha", "beta"}) {
		t.Errorf("ListTargets = %v", got)
	}
	if got := ListGroups(filepath.Join(dir, "missing")); got != nil {
		t.Errorf("missing dir should list nothing, got %v", got)
	}
}

// TestTemplatesEscapeNames covers names that reach the templates straight from
// argv (`banshee -s <target>`, `banshee -ge <name>`) or from a repository
// basename. Interpolating them raw produced JSON that could not parse.
func TestTemplatesEscapeNames(t *testing.T) {
	tests := []struct {
		name   string
		target string
	}{
		{"plain", "blacksheep"},
		{"embedded quote", `foo"bar`},
		{"embedded backslash", `foo\bar`},
		{"newline", "foo\nbar"},
		{"unicode", "café"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, err := ParseSession(DefaultTemplate(tt.target), "t.json")
			if err != nil {
				t.Fatalf("session template does not parse: %v", err)
			}
			if s.Name != tt.target {
				t.Errorf("session name = %q, want %q", s.Name, tt.target)
			}

			g, err := ParseGroup(DefaultGroupTemplate(tt.target, []string{"a", "b"}), "g.json")
			if err != nil {
				t.Fatalf("group template does not parse: %v", err)
			}
			if g.Name != tt.target {
				t.Errorf("group name = %q, want %q", g.Name, tt.target)
			}
		})
	}
}

// TestDefaultTemplateStaysByteIdentical guards the v0.3 heredoc parity that the
// escaping change could have broken for ordinary names.
func TestDefaultTemplateStaysByteIdentical(t *testing.T) {
	want := `{
  "v": 1,
  "name": "blacksheep",
  "windows": [
    {
      "name": "<window_name>",
      "panes": [
        { "run": "<target_command>" }
      ]
    }
  ]
}
`
	if got := string(DefaultTemplate("blacksheep")); got != want {
		t.Fatalf("template =\n%s\nwant\n%s", got, want)
	}
	wantGroup := `{
  "v": 1,
  "name": "work",
  "targets": ["a","b"]
}
`
	if got := string(DefaultGroupTemplate("work", []string{"a", "b"})); got != wantGroup {
		t.Fatalf("group template =\n%s\nwant\n%s", got, wantGroup)
	}
}

// TestWriteGroupValidatesBeforeWriting: an invalid group must leave nothing on
// disk. Writing first meant `banshee -l` reported "(invalid group config)" for
// that name forever.
func TestWriteGroupValidatesBeforeWriting(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "groups")
	if err := WriteGroup(dir, "empty", nil); err == nil {
		t.Fatal("expected an error for a group with no targets")
	}
	if _, err := os.Stat(GroupPath(dir, "empty")); err == nil {
		t.Fatal("an invalid group config was persisted")
	}

	if err := WriteGroup(dir, "work", []string{"a", "b"}); err != nil {
		t.Fatalf("WriteGroup: %v", err)
	}
	g, err := LoadGroup(GroupPath(dir, "work"))
	if err != nil {
		t.Fatalf("LoadGroup: %v", err)
	}
	if len(g.Targets) != 2 {
		t.Fatalf("targets = %v", g.Targets)
	}
}
