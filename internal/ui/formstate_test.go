package ui

import (
	"testing"

	"github.com/jourdanhaines/banshee/internal/providers"
)

func TestTrimValues(t *testing.T) {
	tests := []struct {
		name string
		in   map[string]string
		want map[string]string
	}{
		{"trims whitespace", map[string]string{"a": "  x  ", "b": "\ty\n"}, map[string]string{"a": "x", "b": "y"}},
		{"empty map", map[string]string{}, map[string]string{}},
		{"blank becomes empty", map[string]string{"a": "   "}, map[string]string{"a": ""}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := TrimValues(tt.in)
			if len(got) != len(tt.want) {
				t.Fatalf("len = %d, want %d", len(got), len(tt.want))
			}
			for k, v := range tt.want {
				if got[k] != v {
					t.Errorf("[%q] = %q, want %q", k, got[k], v)
				}
			}
		})
	}
}

func TestFirstMissingRequired(t *testing.T) {
	fields := []providers.FormField{
		{Key: "a", Required: true},
		{Key: "b", Required: false},
		{Key: "c", Required: true},
	}
	tests := []struct {
		name   string
		fields []providers.FormField
		values map[string]string
		wantI  int
		wantOK bool
	}{
		{"all present", fields, map[string]string{"a": "1", "c": "2"}, -1, true},
		{"first required missing", fields, map[string]string{"c": "2"}, 0, false},
		{"later required missing", fields, map[string]string{"a": "1"}, 2, false},
		{"whitespace counts as missing", fields, map[string]string{"a": "  ", "c": "2"}, 0, false},
		{"optional missing is fine", fields, map[string]string{"a": "1", "c": "2"}, -1, true},
		{"no fields", nil, nil, -1, true},
		{
			"a required dropdown never blocks submission",
			[]providers.FormField{{Key: "backend", Required: true, Options: []string{"keyring", "plaintext"}}},
			map[string]string{"backend": ""},
			-1, true,
		},
		{
			"a dropdown is skipped in favour of a genuinely empty entry",
			[]providers.FormField{
				{Key: "backend", Required: true, Options: []string{"keyring", "plaintext"}},
				{Key: "name", Required: true},
			},
			map[string]string{},
			1, false,
		},
		{
			"an empty options slice is an ordinary entry",
			[]providers.FormField{{Key: "name", Required: true, Options: []string{}}},
			map[string]string{},
			0, false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			i, ok := FirstMissingRequired(tt.fields, tt.values)
			if i != tt.wantI || ok != tt.wantOK {
				t.Errorf("FirstMissingRequired = (%d, %v), want (%d, %v)", i, ok, tt.wantI, tt.wantOK)
			}
		})
	}
}
