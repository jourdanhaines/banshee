package plugins

import (
	"path/filepath"
	"testing"

	"github.com/jourdanhaines/banshee/internal/providers"
	"github.com/jourdanhaines/banshee/internal/providers/connectors"
)

func TestWireResultToResult(t *testing.T) {
	m := connectors.Manifest{ID: "wifi", Dir: "/plugins/wifi", Icon: "network-wireless-symbolic", Accent: "#aaa"}
	tests := []struct {
		name string
		in   WireResult
		want providers.Result
	}{
		{
			name: "url action",
			in:   WireResult{ID: "a", Title: "A", Subtitle: "s", Score: 90, Action: &WireAction{Kind: KindURL, URL: "https://x"}},
			want: providers.Result{
				ID: "plugin:wifi:a", Title: "A", Subtitle: "s", Score: 90, Accent: "#aaa",
				Icon: providers.Icon{ThemeName: "network-wireless-symbolic"}, Category: providers.CatPlugin,
				Action: providers.Action{Kind: providers.ActURL, URL: "https://x"},
			},
		},
		{
			name: "exec-detach action",
			in:   WireResult{ID: "b", Title: "B", Action: &WireAction{Kind: KindExecDetach, Argv: []string{"nmcli", "up"}}},
			want: providers.Result{
				ID: "plugin:wifi:b", Title: "B", Score: DefaultScore, Accent: "#aaa",
				Icon: providers.Icon{ThemeName: "network-wireless-symbolic"}, Category: providers.CatPlugin,
				Action: providers.Action{Kind: providers.ActExecDetach, Argv: []string{"nmcli", "up"}},
			},
		},
		{
			name: "explicit callback",
			in:   WireResult{ID: "c", Title: "C", Action: &WireAction{Kind: KindCallback}},
			want: providers.Result{
				ID: "plugin:wifi:c", Title: "C", Score: DefaultScore, Accent: "#aaa",
				Icon: providers.Icon{ThemeName: "network-wireless-symbolic"}, Category: providers.CatPlugin,
				Action: providers.Action{Kind: providers.ActPluginCallback, PluginID: "wifi", ResultID: "c"},
			},
		},
		{
			name: "missing action defaults to callback",
			in:   WireResult{ID: "d", Title: "D"},
			want: providers.Result{
				ID: "plugin:wifi:d", Title: "D", Score: DefaultScore, Accent: "#aaa",
				Icon: providers.Icon{ThemeName: "network-wireless-symbolic"}, Category: providers.CatPlugin,
				Action: providers.Action{Kind: providers.ActPluginCallback, PluginID: "wifi", ResultID: "d"},
			},
		},
		{
			name: "url action without url falls back to callback",
			in:   WireResult{ID: "e", Title: "E", Action: &WireAction{Kind: KindURL}},
			want: providers.Result{
				ID: "plugin:wifi:e", Title: "E", Score: DefaultScore, Accent: "#aaa",
				Icon: providers.Icon{ThemeName: "network-wireless-symbolic"}, Category: providers.CatPlugin,
				Action: providers.Action{Kind: providers.ActPluginCallback, PluginID: "wifi", ResultID: "e"},
			},
		},
		{
			name: "per-result icon path and accent override",
			in:   WireResult{ID: "f", Title: "F", Icon: "icons/f.png", Accent: "#fff"},
			want: providers.Result{
				ID: "plugin:wifi:f", Title: "F", Score: DefaultScore, Accent: "#fff",
				Icon: providers.Icon{Path: filepath.Join("/plugins/wifi", "icons/f.png")}, Category: providers.CatPlugin,
				Action: providers.Action{Kind: providers.ActPluginCallback, PluginID: "wifi", ResultID: "f"},
			},
		},
		{
			name: "clipboard action",
			in:   WireResult{ID: "h", Title: "H", Action: &WireAction{Kind: KindClipboard, Text: "42"}},
			want: providers.Result{
				ID: "plugin:wifi:h", Title: "H", Score: DefaultScore, Accent: "#aaa",
				Icon: providers.Icon{ThemeName: "network-wireless-symbolic"}, Category: providers.CatPlugin,
				Action: providers.Action{Kind: providers.ActClipboardCopy, Text: "42"},
			},
		},
		{
			name: "clipboard action without text falls back to callback",
			in:   WireResult{ID: "i", Title: "I", Action: &WireAction{Kind: KindClipboard}},
			want: providers.Result{
				ID: "plugin:wifi:i", Title: "I", Score: DefaultScore, Accent: "#aaa",
				Icon: providers.Icon{ThemeName: "network-wireless-symbolic"}, Category: providers.CatPlugin,
				Action: providers.Action{Kind: providers.ActPluginCallback, PluginID: "wifi", ResultID: "i"},
			},
		},
		{
			name: "unknown action kind falls back to callback",
			in:   WireResult{ID: "g", Title: "G", Action: &WireAction{Kind: "teleport"}},
			want: providers.Result{
				ID: "plugin:wifi:g", Title: "G", Score: DefaultScore, Accent: "#aaa",
				Icon: providers.Icon{ThemeName: "network-wireless-symbolic"}, Category: providers.CatPlugin,
				Action: providers.Action{Kind: providers.ActPluginCallback, PluginID: "wifi", ResultID: "g"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.in.toResult(m)
			if got.ID != tt.want.ID || got.Title != tt.want.Title || got.Subtitle != tt.want.Subtitle ||
				got.Score != tt.want.Score || got.Accent != tt.want.Accent || got.Icon != tt.want.Icon ||
				got.Category != tt.want.Category || got.Action.Kind != tt.want.Action.Kind ||
				got.Action.URL != tt.want.Action.URL || got.Action.PluginID != tt.want.Action.PluginID ||
				got.Action.ResultID != tt.want.Action.ResultID || got.Action.Text != tt.want.Action.Text {
				t.Fatalf("toResult = %+v, want %+v", got, tt.want)
			}
			if len(got.Action.Argv) != len(tt.want.Action.Argv) {
				t.Fatalf("argv = %v, want %v", got.Action.Argv, tt.want.Action.Argv)
			}
		})
	}
}

func TestMatchQuery(t *testing.T) {
	tests := []struct {
		name   string
		prefix string
		min    int
		query  string
		want   string
		ok     bool
	}{
		{"no prefix passes through", "", 0, "blacksh", "blacksh", true},
		{"no prefix rejects empty", "", 0, "   ", "", false},
		{"prefix alone", "wifi", 0, "wifi", "", true},
		{"prefix with argument", "wifi", 0, "wifi home", "home", true},
		{"prefix case insensitive", "wifi", 0, "WiFi home", "home", true},
		{"prefix must be followed by space", "wifi", 0, "wifikill", "", false},
		{"non-matching query", "wifi", 0, "blacksh", "", false},
		{"shorter than prefix", "wifi", 0, "wi", "", false},
		{"surrounding whitespace trimmed", "wifi", 0, "  wifi   home  ", "home", true},
		{"min zero keeps exact only", "record", 0, "rec", "", false},
		{"min prefix alone", "record", 3, "rec", "", true},
		{"min prefix case insensitive", "record", 3, "REC", "", true},
		{"min partial with argument", "record", 3, "reco x", "x", true},
		{"min one short of full", "record", 3, "recor", "", true},
		{"min full prefix", "record", 3, "record", "", true},
		{"min full prefix with arguments", "record", 3, "record foo bar", "foo bar", true},
		{"min below minimum", "record", 3, "re", "", false},
		{"min token longer than prefix", "record", 3, "recording", "", false},
		{"min plural not a prefix", "record", 3, "records", "", false},
		{"min leading junk", "record", 3, "xrec", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &ExecPlugin{prefix: tt.prefix, prefixMin: tt.min}
			got, ok := p.MatchQuery(tt.query)
			if got != tt.want || ok != tt.ok {
				t.Fatalf("MatchQuery(%q) = (%q, %v), want (%q, %v)", tt.query, got, ok, tt.want, tt.ok)
			}
		})
	}
}
