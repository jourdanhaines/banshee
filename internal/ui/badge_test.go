package ui

import (
	"testing"

	"github.com/jourdanhaines/banshee/internal/providers"
)

func TestCategoryLabel(t *testing.T) {
	tests := []struct {
		cat  providers.Category
		want string
	}{
		{providers.CatSession, "session"},
		{providers.CatGitHub, "github"},
		{providers.CatConnector, "connector"},
		{providers.CatDirectory, "directory"},
		{providers.CatApp, "app"},
		{providers.CatKill, "kill"},
		{providers.CatPlugin, "plugin"},
		{providers.Category(999), ""}, // a category from a newer banshee
		{providers.Category(-1), ""},
	}
	for _, tt := range tests {
		if got := CategoryLabel(tt.cat); got != tt.want {
			t.Errorf("CategoryLabel(%d) = %q, want %q", tt.cat, got, tt.want)
		}
	}
}

func TestBadgeMarkup(t *testing.T) {
	tests := []struct {
		name   string
		text   string
		accent string
		want   string
	}{
		{
			name:   "valid accent produces a pango span",
			text:   "connector",
			accent: "#a78bfa",
			want:   `<span foreground="#a78bfa">connector</span>`,
		},
		{
			name:   "short hex is normalized",
			text:   "plugin",
			accent: "#f0a",
			want:   `<span foreground="#ff00aa">plugin</span>`,
		},
		{
			name:   "no accent falls back to the stylesheet",
			text:   "session",
			accent: "",
			want:   "",
		},
		{
			name:   "non-hex accent is rejected",
			text:   "plugin",
			accent: "red",
			want:   "",
		},
		{
			name: "markup injection through the accent is rejected",
			text: "plugin",
			// A hostile manifest trying to close the span and inject markup.
			accent: `#fff"><span size="100000">`,
			want:   "",
		},
		{
			name:   "markup in the badge text is escaped",
			text:   `a<b>&"c`,
			accent: "#7aa2f7",
			want:   `<span foreground="#7aa2f7">a&lt;b&gt;&amp;&#34;c</span>`,
		},
		{
			name:   "empty text yields no markup",
			text:   "  ",
			accent: "#7aa2f7",
			want:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := badgeMarkup(tt.text, tt.accent); got != tt.want {
				t.Errorf("badgeMarkup(%q, %q) = %q, want %q", tt.text, tt.accent, got, tt.want)
			}
		})
	}
}
