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
		name      string
		prefix    string
		prefixes  []string
		min       int
		query     string
		want      string
		wantMatch string // canonical prefix reported on a match
		ok        bool
	}{
		{"no prefix passes through", "", nil, 0, "blacksh", "blacksh", "", true},
		{"no prefix rejects empty", "", nil, 0, "   ", "", "", false},
		{"prefix alone", "wifi", nil, 0, "wifi", "", "wifi", true},
		{"prefix with argument", "wifi", nil, 0, "wifi home", "home", "wifi", true},
		{"prefix case insensitive", "wifi", nil, 0, "WiFi home", "home", "wifi", true},
		{"prefix must be followed by space", "wifi", nil, 0, "wifikill", "", "", false},
		{"non-matching query", "wifi", nil, 0, "blacksh", "", "", false},
		{"shorter than prefix", "wifi", nil, 0, "wi", "", "", false},
		{"surrounding whitespace trimmed", "wifi", nil, 0, "  wifi   home  ", "home", "wifi", true},
		{"min zero keeps exact only", "record", nil, 0, "rec", "", "", false},
		{"min prefix alone", "record", nil, 3, "rec", "", "record", true},
		{"min prefix case insensitive", "record", nil, 3, "REC", "", "record", true},
		{"min partial with argument", "record", nil, 3, "reco x", "x", "record", true},
		{"min one short of full", "record", nil, 3, "recor", "", "record", true},
		{"min full prefix", "record", nil, 3, "record", "", "record", true},
		{"min full prefix with arguments", "record", nil, 3, "record foo bar", "foo bar", "record", true},
		{"min below minimum", "record", nil, 3, "re", "", "", false},
		{"min token longer than prefix", "record", nil, 3, "recording", "", "", false},
		{"min plural not a prefix", "record", nil, 3, "records", "", "", false},
		{"min leading junk", "record", nil, 3, "xrec", "", "", false},
		{"alt prefix shortened", "record", []string{"stop"}, 3, "sto", "", "stop", true},
		{"alt prefix case insensitive", "record", []string{"stop"}, 3, "STOP", "", "stop", true},
		{"alt prefix with argument", "record", []string{"stop"}, 3, "stop now", "now", "stop", true},
		{"primary still matches beside alt", "record", []string{"stop"}, 3, "rec", "", "record", true},
		{"alt prefix below minimum", "record", []string{"stop"}, 3, "st", "", "", false},
		{"alt prefix token too long", "record", []string{"stop"}, 3, "stopping", "", "", false},
		{"alt prefix min zero rejects short", "record", []string{"stop"}, 0, "sto", "", "", false},
		{"alt prefix min zero exact", "record", []string{"stop"}, 0, "stop", "", "stop", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &ExecPlugin{prefixMin: tt.min}
			if tt.prefix != "" {
				p.prefixes = append([]string{tt.prefix}, tt.prefixes...)
			}
			got, pre, ok := p.MatchQuery(tt.query)
			if got != tt.want || pre != tt.wantMatch || ok != tt.ok {
				t.Fatalf("MatchQuery(%q) = (%q, %q, %v), want (%q, %q, %v)", tt.query, got, pre, ok, tt.want, tt.wantMatch, tt.ok)
			}
		})
	}
}
