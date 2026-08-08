package steam

import (
	"strings"
	"testing"
)

func TestParseVDF(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		wantErr bool
		check   func(t *testing.T, n vdfNode)
	}{
		{
			name: "flat key values",
			in:   `"a" "1" "b" "two"`,
			check: func(t *testing.T, n vdfNode) {
				if n.str("a") != "1" || n.str("b") != "two" {
					t.Errorf("node = %v", n)
				}
			},
		},
		{
			name: "nesting",
			in: `"root"
{
	"inner"
	{
		"k" "v"
	}
	"s" "x"
}`,
			check: func(t *testing.T, n vdfNode) {
				root := n.child("root")
				if root == nil {
					t.Fatalf("no root child: %v", n)
				}
				if root.str("s") != "x" {
					t.Errorf("root.s = %q", root.str("s"))
				}
				if got := root.child("inner").str("k"); got != "v" {
					t.Errorf("inner.k = %q", got)
				}
			},
		},
		{
			name: "escapes",
			in:   `"k" "a \"quoted\" back\\slash"`,
			check: func(t *testing.T, n vdfNode) {
				if got := n.str("k"); got != `a "quoted" back\slash` {
					t.Errorf("k = %q", got)
				}
			},
		},
		{
			name: "comments and CRLF",
			in:   "// header comment\r\n\"k\" \"v\" // trailing\r\n",
			check: func(t *testing.T, n vdfNode) {
				if n.str("k") != "v" {
					t.Errorf("k = %q", n.str("k"))
				}
			},
		},
		{
			name: "bare tokens",
			in:   `key value`,
			check: func(t *testing.T, n vdfNode) {
				if n.str("key") != "value" {
					t.Errorf("key = %q", n.str("key"))
				}
			},
		},
		{name: "unbalanced open", in: `"a" { "k" "v"`, wantErr: true},
		{name: "unbalanced close", in: `"a" "1" }`, wantErr: true},
		{name: "brace as key", in: `{ "k" "v" }`, wantErr: true},
		{name: "key without value", in: `"lonely"`, wantErr: true},
		{name: "unterminated quote", in: `"k" "never ends`, wantErr: true},
		{
			name: "real appmanifest shape",
			in: `"AppState"
{
	"appid"		"105600"
	"name"		"Terraria"
	"StateFlags"		"4"
	"installdir"		"Terraria"
	"InstalledDepots"
	{
		"105602"
		{
			"manifest"		"3908368967129848993"
		}
	}
}`,
			check: func(t *testing.T, n vdfNode) {
				app := n.child("AppState")
				if app.str("appid") != "105600" || app.str("name") != "Terraria" || app.str("StateFlags") != "4" {
					t.Errorf("AppState = %v", app)
				}
			},
		},
		{
			name: "real libraryfolders shape",
			in: `"libraryfolders"
{
	"0"
	{
		"path"		"/home/u/.local/share/Steam"
		"apps"
		{
			"105600"		"828396557"
		}
	}
	"1"
	{
		"path"		"/mnt/games/SteamLibrary"
	}
}`,
			check: func(t *testing.T, n vdfNode) {
				folders := n.child("libraryfolders")
				if got := folders.child("1").str("path"); got != "/mnt/games/SteamLibrary" {
					t.Errorf("folder 1 path = %q", got)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			n, err := parseVDF(strings.NewReader(tc.in))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseVDF = %v, want error", n)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseVDF: %v", err)
			}
			tc.check(t, n)
		})
	}
}

func TestVDFNodeAccessorsTolerateWrongShapes(t *testing.T) {
	n := vdfNode{"s": "str", "n": vdfNode{"k": "v"}}
	if n.child("s") != nil {
		t.Error("child(string value) != nil")
	}
	if n.child("missing") != nil {
		t.Error("child(missing) != nil")
	}
	if n.str("n") != "" {
		t.Errorf("str(node value) = %q", n.str("n"))
	}
	if n.str("missing") != "" {
		t.Errorf("str(missing) = %q", n.str("missing"))
	}
}
