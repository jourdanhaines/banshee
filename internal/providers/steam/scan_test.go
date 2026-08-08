package steam

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// fakeApp declares one appmanifest in a fixture library.
type fakeApp struct {
	appID string
	name  string
	flags string // StateFlags value; "" writes no StateFlags key at all
}

// writeManifest writes an appmanifest_*.acf into lib/steamapps.
func writeManifest(t *testing.T, lib string, a fakeApp) {
	t.Helper()
	dir := filepath.Join(lib, "steamapps")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf("\"AppState\"\n{\n\t\"appid\"\t\t%q\n\t\"name\"\t\t%q\n", a.appID, a.name)
	if a.flags != "" {
		body += fmt.Sprintf("\t\"StateFlags\"\t\t%q\n", a.flags)
	}
	body += "}\n"
	path := filepath.Join(dir, "appmanifest_"+a.appID+".acf")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeLibraryFolders writes root's libraryfolders.vdf listing the given
// library paths (root itself is folder 0).
func writeLibraryFolders(t *testing.T, root string, extras ...string) {
	t.Helper()
	dir := filepath.Join(root, "steamapps")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "\"libraryfolders\"\n{\n"
	for i, p := range append([]string{root}, extras...) {
		body += fmt.Sprintf("\t%q\n\t{\n\t\t\"path\"\t\t%q\n\t}\n", fmt.Sprint(i), p)
	}
	body += "\t\"contentstatsid\"\t\t\"-1\"\n}\n"
	if err := os.WriteFile(filepath.Join(dir, "libraryfolders.vdf"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeIconNested drops a 40-hex client icon in the current librarycache
// layout and returns its path.
func writeIconNested(t *testing.T, root, appID string) string {
	t.Helper()
	dir := filepath.Join(root, "appcache", "librarycache", appID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "858961e95fbf869f136e1770d586e0caefd4cfac.jpg")
	if err := os.WriteFile(path, []byte("jpg"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Named siblings the picker must skip.
	for _, sib := range []string{"header.jpg", "logo.png", "library_600x900.jpg"} {
		if err := os.WriteFile(filepath.Join(dir, sib), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

// writeIconFlat drops an old-layout icon and returns its path.
func writeIconFlat(t *testing.T, root, appID string) string {
	t.Helper()
	dir := filepath.Join(root, "appcache", "librarycache")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, appID+"_icon.jpg")
	if err := os.WriteFile(path, []byte("jpg"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestScanGames(t *testing.T) {
	root, second := t.TempDir(), t.TempDir()
	writeLibraryFolders(t, root, second)

	writeManifest(t, root, fakeApp{"105600", "Terraria", "4"})
	writeManifest(t, second, fakeApp{"1245620", "ELDEN RING", "4"})
	// Every reason to be skipped, one manifest each.
	writeManifest(t, root, fakeApp{"111", "Downloading Game", "2"})
	writeManifest(t, root, fakeApp{"112", "No Flags Game", ""})
	writeManifest(t, root, fakeApp{steamworksRedistAppID, "Steamworks Common Redistributables", "4"})
	writeManifest(t, root, fakeApp{"113", "Proton 9.0 (Beta)", "4"})
	writeManifest(t, root, fakeApp{"114", "Steam Linux Runtime 3.0 (sniper)", "4"})
	writeManifest(t, root, fakeApp{"115", "Celeste Original Soundtrack", "4"})
	writeManifest(t, root, fakeApp{"116", "Celeste OST", "4"})
	writeManifest(t, root, fakeApp{"117", "Valheim Dedicated Server", "4"})
	// Duplicate appid in the second library must not double the game.
	writeManifest(t, second, fakeApp{"105600", "Terraria", "4"})
	// "OST" only as a word: this one stays.
	writeManifest(t, root, fakeApp{"118", "Lost Planet", "4"})

	games, err := scanGames(root)
	if err != nil {
		t.Fatalf("scanGames: %v", err)
	}
	var names []string
	for _, g := range games {
		names = append(names, g.Name)
	}
	want := []string{"ELDEN RING", "Lost Planet", "Terraria"} // sorted by name
	if len(names) != len(want) {
		t.Fatalf("games = %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("games = %v, want %v", names, want)
		}
	}
}

func TestLibraryPathsWithoutVDF(t *testing.T) {
	root := t.TempDir()
	got := libraryPaths(root)
	if len(got) != 1 || got[0] != root {
		t.Fatalf("libraryPaths = %v, want just root", got)
	}
}

func TestIconPath(t *testing.T) {
	t.Run("nested layout wins over flat", func(t *testing.T) {
		root := t.TempDir()
		nested := writeIconNested(t, root, "105600")
		writeIconFlat(t, root, "105600")
		if got := iconPath(root, "105600"); got != nested {
			t.Errorf("iconPath = %q, want %q", got, nested)
		}
	})
	t.Run("flat fallback", func(t *testing.T) {
		root := t.TempDir()
		flat := writeIconFlat(t, root, "105600")
		if got := iconPath(root, "105600"); got != flat {
			t.Errorf("iconPath = %q, want %q", got, flat)
		}
	})
	t.Run("named siblings are not icons", func(t *testing.T) {
		root := t.TempDir()
		dir := filepath.Join(root, "appcache", "librarycache", "105600")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "header.jpg"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := iconPath(root, "105600"); got != "" {
			t.Errorf("iconPath = %q, want empty", got)
		}
	})
	t.Run("missing entirely", func(t *testing.T) {
		if got := iconPath(t.TempDir(), "105600"); got != "" {
			t.Errorf("iconPath = %q, want empty", got)
		}
	})
}
