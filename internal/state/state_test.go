package state

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	return &Store{Path: filepath.Join(t.TempDir(), "last_action")}
}

func TestRecordAndRead(t *testing.T) {
	tests := []struct {
		name     string
		kind     string
		target   string
		wantFile string
	}{
		{"target", KindTarget, "blacksheep", "target:blacksheep\n"},
		{"group", KindGroup, "work", "group:work\n"},
		{"name with colon", KindTarget, "a:b", "target:a:b\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newStore(t)
			if err := s.Record(tc.kind, tc.target); err != nil {
				t.Fatal(err)
			}
			b, err := os.ReadFile(s.Path)
			if err != nil {
				t.Fatal(err)
			}
			if string(b) != tc.wantFile {
				t.Errorf("file = %q, want %q", b, tc.wantFile)
			}
			got, err := s.Read()
			if err != nil {
				t.Fatal(err)
			}
			if got.Kind != tc.kind || got.Name != tc.target {
				t.Errorf("Read = %+v", got)
			}
		})
	}
}

func TestRecordIsAtomicAndLeavesNoTemp(t *testing.T) {
	s := newStore(t)
	if err := s.Record(KindTarget, "one"); err != nil {
		t.Fatal(err)
	}
	if err := s.Record(KindGroup, "two"); err != nil {
		t.Fatal(err)
	}
	got, err := s.Read()
	if err != nil {
		t.Fatal(err)
	}
	if got.String() != "group:two" {
		t.Errorf("Read = %q", got.String())
	}
	entries, err := os.ReadDir(filepath.Dir(s.Path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("temp files left behind: %v", entries)
	}
}

func TestRecordEmptyIsNoop(t *testing.T) {
	s := newStore(t)
	if err := s.Record("", "x"); err != nil {
		t.Fatal(err)
	}
	if err := s.Record(KindTarget, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.Path); !os.IsNotExist(err) {
		t.Error("no file should have been written")
	}
}

func TestReadErrors(t *testing.T) {
	tests := []struct {
		name    string
		content string
		wantErr error
		errText string
	}{
		{name: "missing file", wantErr: ErrNoAction},
		{name: "empty file", content: "", wantErr: ErrNoAction},
		{name: "blank line", content: "\n", wantErr: ErrNoAction},
		{name: "no colon", content: "blacksheep\n", errText: "malformed"},
		{name: "empty kind", content: ":name\n", errText: "malformed"},
		{name: "empty name", content: "target:\n", errText: "malformed"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newStore(t)
			if tc.name != "missing file" {
				if err := os.WriteFile(s.Path, []byte(tc.content), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			_, err := s.Read()
			if err == nil {
				t.Fatal("expected an error")
			}
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Errorf("err = %v, want %v", err, tc.wantErr)
			}
			if tc.errText != "" && !contains(err.Error(), tc.errText) {
				t.Errorf("err = %v, want it to contain %q", err, tc.errText)
			}
		})
	}
}

func TestReadIgnoresTrailingLinesAndCR(t *testing.T) {
	s := newStore(t)
	if err := os.WriteFile(s.Path, []byte("target:demo\r\nnoise\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := s.Read()
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != KindTarget || got.Name != "demo" {
		t.Errorf("Read = %+v", got)
	}
}

func TestRestore(t *testing.T) {
	tests := []struct {
		name        string
		content     string
		wantTarget  string
		wantGroup   string
		wantErrText string
	}{
		{name: "target", content: "target:demo\n", wantTarget: "demo"},
		{name: "group", content: "group:work\n", wantGroup: "work"},
		{name: "unknown kind", content: "bundle:x\n", wantErrText: "unknown last_action type"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newStore(t)
			if err := os.WriteFile(s.Path, []byte(tc.content), 0o644); err != nil {
				t.Fatal(err)
			}
			var gotTarget, gotGroup string
			err := s.Restore(
				func(n string) error { gotTarget = n; return nil },
				func(n string) error { gotGroup = n; return nil },
			)
			if tc.wantErrText != "" {
				if err == nil || !contains(err.Error(), tc.wantErrText) {
					t.Fatalf("err = %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if gotTarget != tc.wantTarget || gotGroup != tc.wantGroup {
				t.Errorf("target=%q group=%q", gotTarget, gotGroup)
			}
		})
	}
}

func TestRestoreWithoutAction(t *testing.T) {
	s := newStore(t)
	err := s.Restore(func(string) error { return nil }, func(string) error { return nil })
	if !errors.Is(err, ErrNoAction) {
		t.Errorf("err = %v, want ErrNoAction", err)
	}
}

func TestMigrate(t *testing.T) {
	t.Run("0.2.0 last_loaded becomes a target action", func(t *testing.T) {
		dir := t.TempDir()
		s := &Store{Path: filepath.Join(dir, "last_action")}
		for _, n := range []string{"sessions", "session_state"} {
			if err := os.WriteFile(filepath.Join(dir, n), []byte("stale"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(dir, "last_loaded"), []byte("blacksheep\n"), 0o644); err != nil {
			t.Fatal(err)
		}

		s.Migrate(dir)

		got, err := s.Read()
		if err != nil || got.String() != "target:blacksheep" {
			t.Errorf("Read = %v (%v)", got, err)
		}
		for _, n := range []string{"sessions", "session_state", "last_loaded"} {
			if _, err := os.Stat(filepath.Join(dir, n)); !os.IsNotExist(err) {
				t.Errorf("%s should have been removed", n)
			}
		}
	})

	t.Run("existing last_action wins", func(t *testing.T) {
		dir := t.TempDir()
		s := &Store{Path: filepath.Join(dir, "last_action")}
		if err := s.Record(KindGroup, "work"); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "last_loaded"), []byte("old\n"), 0o644); err != nil {
			t.Fatal(err)
		}

		s.Migrate(dir)

		got, _ := s.Read()
		if got.String() != "group:work" {
			t.Errorf("Read = %q", got.String())
		}
	})

	t.Run("nothing to do", func(t *testing.T) {
		dir := t.TempDir()
		s := &Store{Path: filepath.Join(dir, "last_action")}
		s.Migrate(dir)
		if _, err := s.Read(); !errors.Is(err, ErrNoAction) {
			t.Errorf("err = %v", err)
		}
	})
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }
