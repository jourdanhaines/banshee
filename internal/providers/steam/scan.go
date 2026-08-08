package steam

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Game is one installed Steam game, as read from its appmanifest.
type Game struct {
	// AppID is Steam's numeric application id, as a string (it is only ever
	// interpolated into URLs and paths).
	AppID string
	// Name is the store title from the manifest.
	Name string
	// IconPath is the absolute path to the game's client icon in Steam's
	// librarycache, or "" when no cached art was found (the provider then
	// falls back to the builtin Steam mark).
	IconPath string
}

// stateFullyInstalled is the StateFlags bit Steam sets once every depot is on
// disk; manifests without it are queued, downloading or partially removed.
const stateFullyInstalled = 4

// steamworksRedistAppID is "Steamworks Common Redistributables" — tooling
// every library carries, never something to launch.
const steamworksRedistAppID = "228980"

// discoverRoot returns the first Steam root that exists on this machine, or
// "" when Steam is not installed. A root is a directory holding steamapps/;
// ~/.steam/steam is usually a symlink to the XDG location, so only the first
// hit is used.
func discoverRoot() string {
	var candidates []string
	if x := os.Getenv("XDG_DATA_HOME"); x != "" {
		candidates = append(candidates, filepath.Join(x, "Steam"))
	}
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates,
			filepath.Join(home, ".local", "share", "Steam"),
			filepath.Join(home, ".steam", "steam"),
			filepath.Join(home, ".steam", "root"),
			filepath.Join(home, ".var", "app", "com.valvesoftware.Steam", ".local", "share", "Steam"),
		)
	}
	for _, c := range candidates {
		if fi, err := os.Stat(filepath.Join(c, "steamapps")); err == nil && fi.IsDir() {
			return c
		}
	}
	return ""
}

// libraryPaths returns every Steam library root: the ones listed in
// steamapps/libraryfolders.vdf plus root itself. A missing or malformed
// libraryfolders.vdf degrades to just root — the primary library still works.
func libraryPaths(root string) []string {
	paths := []string{root}
	seen := map[string]bool{root: true}

	f, err := os.Open(filepath.Join(root, "steamapps", "libraryfolders.vdf"))
	if err != nil {
		return paths
	}
	defer f.Close()
	doc, err := parseVDF(f)
	if err != nil {
		return paths
	}
	folders := doc.child("libraryfolders")
	if folders == nil {
		folders = doc.child("LibraryFolders") // pre-2021 capitalization
	}
	if folders == nil {
		return paths
	}
	for key, v := range folders {
		if _, err := strconv.Atoi(key); err != nil {
			continue // "contentstatsid" and friends
		}
		var p string
		switch val := v.(type) {
		case vdfNode:
			p = val.str("path")
		case string:
			p = val // ancient format: "1" "/path/to/library"
		}
		if p != "" && !seen[p] {
			seen[p] = true
			paths = append(paths, p)
		}
	}
	return paths
}

// scanGames reads every appmanifest under every library of root and returns
// the installed, launchable games sorted by name. Icons always resolve
// against root — Steam keeps librarycache art in the primary install, not
// per-library.
func scanGames(root string) ([]Game, error) {
	var games []Game
	seen := map[string]bool{}
	for _, lib := range libraryPaths(root) {
		manifests, err := filepath.Glob(filepath.Join(lib, "steamapps", "appmanifest_*.acf"))
		if err != nil {
			continue // only on bad pattern, which this is not
		}
		for _, m := range manifests {
			g, ok := readManifest(m)
			if !ok || seen[g.AppID] {
				continue
			}
			seen[g.AppID] = true
			g.IconPath = iconPath(root, g.AppID)
			games = append(games, g)
		}
	}
	sort.Slice(games, func(i, j int) bool {
		a, b := strings.ToLower(games[i].Name), strings.ToLower(games[j].Name)
		if a != b {
			return a < b
		}
		return games[i].AppID < games[j].AppID
	})
	return games, nil
}

// readManifest parses one appmanifest_*.acf, reporting ok=false for anything
// that should not become a row: unreadable or malformed files, apps still
// downloading, and non-game apps.
func readManifest(path string) (Game, bool) {
	f, err := os.Open(path)
	if err != nil {
		return Game{}, false
	}
	defer f.Close()
	doc, err := parseVDF(f)
	if err != nil {
		return Game{}, false
	}
	app := doc.child("AppState")
	if app == nil {
		return Game{}, false
	}
	g := Game{AppID: app.str("appid"), Name: app.str("name")}
	if g.AppID == "" || g.Name == "" {
		return Game{}, false
	}
	flags, err := strconv.Atoi(app.str("StateFlags"))
	if err != nil || flags&stateFullyInstalled == 0 {
		return Game{}, false
	}
	if !isGame(g.AppID, g.Name) {
		return Game{}, false
	}
	return g, true
}

// ostWord matches "OST" as a standalone word ("Celeste OST"), without
// swallowing names that merely contain the letters ("Lost Planet").
var ostWord = regexp.MustCompile(`\bOST\b`)

// isGame filters out the non-game apps Steam installs alongside games. The
// manifest carries no app-type field, so the appid and name are the only
// local signals: Proton and the Steam Linux Runtime are compatibility
// tooling, and soundtracks and dedicated servers are launchable only in the
// loosest sense.
func isGame(appID, name string) bool {
	if appID == steamworksRedistAppID {
		return false
	}
	for _, prefix := range []string{"Proton", "Steam Linux Runtime"} {
		if strings.HasPrefix(name, prefix) {
			return false
		}
	}
	if strings.Contains(name, "Soundtrack") || strings.Contains(name, "Dedicated Server") {
		return false
	}
	return !ostWord.MatchString(name)
}

// iconHex matches the 40-hex-digit basename Steam gives the client icon in
// the current librarycache layout.
var iconHex = regexp.MustCompile(`^[0-9a-f]{40}\.jpg$`)

// iconPath returns the absolute path of the game's client icon, probing the
// current nested layout (librarycache/<appid>/<sha>.jpg) first and the old
// flat layout (librarycache/<appid>_icon.jpg) second. "" when neither exists.
func iconPath(root, appID string) string {
	cache := filepath.Join(root, "appcache", "librarycache")
	if entries, err := os.ReadDir(filepath.Join(cache, appID)); err == nil {
		for _, e := range entries {
			if iconHex.MatchString(e.Name()) {
				return filepath.Join(cache, appID, e.Name())
			}
		}
	}
	flat := filepath.Join(cache, appID+"_icon.jpg")
	if _, err := os.Stat(flat); err == nil {
		return flat
	}
	return ""
}
