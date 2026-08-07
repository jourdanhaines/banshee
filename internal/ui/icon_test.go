package ui

import (
	"testing"

	"github.com/jourdanhaines/banshee/internal/providers"
)

func TestResolveIcon(t *testing.T) {
	tests := []struct {
		name      string
		icon      providers.Icon
		wantKind  IconKind
		wantValue string
	}{
		{
			name:     "zero value has no icon",
			icon:     providers.Icon{},
			wantKind: IconNone,
		},
		{
			name:      "app id",
			icon:      providers.Icon{AppID: "org.mozilla.firefox.desktop"},
			wantKind:  IconApp,
			wantValue: "org.mozilla.firefox.desktop",
		},
		{
			name:      "app id without the desktop suffix is kept verbatim",
			icon:      providers.Icon{AppID: "org.mozilla.firefox"},
			wantKind:  IconApp,
			wantValue: "org.mozilla.firefox",
		},
		{
			name:      "theme name",
			icon:      providers.Icon{ThemeName: "network-wireless-symbolic"},
			wantKind:  IconTheme,
			wantValue: "network-wireless-symbolic",
		},
		{
			name:      "absolute path",
			icon:      providers.Icon{Path: "/home/u/.config/banshee/plugins/railway/railway.svg"},
			wantKind:  IconFile,
			wantValue: "/home/u/.config/banshee/plugins/railway/railway.svg",
		},
		{
			name:     "relative path is rejected",
			icon:     providers.Icon{Path: "railway.svg"},
			wantKind: IconNone,
		},
		{
			name:     "path traversal without a leading slash is rejected",
			icon:     providers.Icon{Path: "../../etc/passwd"},
			wantKind: IconNone,
		},
		{
			name:      "app id wins over theme name and path",
			icon:      providers.Icon{AppID: "a.desktop", ThemeName: "n", Path: "/p.svg"},
			wantKind:  IconApp,
			wantValue: "a.desktop",
		},
		{
			name:      "theme name wins over path",
			icon:      providers.Icon{ThemeName: "n", Path: "/p.svg"},
			wantKind:  IconTheme,
			wantValue: "n",
		},
		{
			name:      "whitespace is trimmed",
			icon:      providers.Icon{ThemeName: "  folder-symbolic  "},
			wantKind:  IconTheme,
			wantValue: "folder-symbolic",
		},
		{
			name:      "whitespace-only fields count as unset",
			icon:      providers.Icon{AppID: "   ", ThemeName: "\t", Path: "/p.svg"},
			wantKind:  IconFile,
			wantValue: "/p.svg",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kind, value := ResolveIcon(tt.icon)
			if kind != tt.wantKind {
				t.Errorf("kind = %v, want %v", kind, tt.wantKind)
			}
			if value != tt.wantValue {
				t.Errorf("value = %q, want %q", value, tt.wantValue)
			}
		})
	}
}

func TestIconKindString(t *testing.T) {
	tests := map[IconKind]string{
		IconNone:  "none",
		IconApp:   "app",
		IconTheme: "theme",
		IconFile:  "file",
	}
	for k, want := range tests {
		if got := k.String(); got != want {
			t.Errorf("IconKind(%d).String() = %q, want %q", k, got, want)
		}
	}
}

func TestDesktopID(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"org.mozilla.firefox", "org.mozilla.firefox.desktop"},
		{"org.mozilla.firefox.desktop", "org.mozilla.firefox.desktop"},
		{"  ghostty  ", "ghostty.desktop"},
		{"", ""},
		{"   ", ""},
	}
	for _, tt := range tests {
		if got := desktopID(tt.in); got != tt.want {
			t.Errorf("desktopID(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
