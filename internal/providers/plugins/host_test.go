package plugins

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jourdanhaines/banshee/internal/launch"
	"github.com/jourdanhaines/banshee/internal/providers"
)

// writePlugin creates <root>/<name> containing manifest.json and, when script
// is non-empty, an executable plugin.sh.
func writePlugin(t *testing.T, root, name, manifest, script string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if manifest != "" {
		if err := os.WriteFile(filepath.Join(dir, ManifestName), []byte(manifest), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if script != "" {
		if err := os.WriteFile(filepath.Join(dir, "plugin.sh"), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestHostLoad(t *testing.T) {
	root := t.TempDir()
	writePlugin(t, root, "aaa-railwayish", `{"v":1,"id":"sentry","name":"Sentry","type":"url",
		"url":{"template":"https://sentry.io/{binding}","requires_binding":true}}`, "")
	writePlugin(t, root, "bbb-demo", `{"v":1,"id":"demo","name":"Demo","type":"exec",
		"exec":{"bin":"./plugin.sh","prefix":"demo","timeout_ms":500}}`, echoScript)
	writePlugin(t, root, "ccc-broken", `{"v":1,"id":"broken","type":"url"}`, "")
	writePlugin(t, root, "ddd-nomanifest", "", "")
	if err := os.WriteFile(filepath.Join(root, "stray.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	h := NewHost(root, Options{})
	t.Cleanup(h.Shutdown)
	err := h.Load()
	if err == nil {
		t.Fatal("expected the broken manifest to be reported")
	}

	urls := h.URLManifests()
	if len(urls) != 1 || urls[0].ID != "sentry" {
		t.Fatalf("URLManifests = %+v", urls)
	}
	if urls[0].Dir != filepath.Join(root, "aaa-railwayish") {
		t.Fatalf("manifest Dir = %q", urls[0].Dir)
	}

	provs := h.Providers()
	if len(provs) != 1 || provs[0].Name() != "plugin:demo" {
		t.Fatalf("Providers = %+v", provs)
	}

	got, err := provs[0].Query(context.Background(), "demo hi")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Title != "echo hi" {
		t.Fatalf("query through host provider = %+v", got)
	}
}

func TestHostLoadMissingDir(t *testing.T) {
	h := NewHost(filepath.Join(t.TempDir(), "does-not-exist"), Options{})
	if err := h.Load(); err != nil {
		t.Fatalf("missing plugin dir must not be an error: %v", err)
	}
	if len(h.Providers()) != 0 || len(h.URLManifests()) != 0 {
		t.Fatal("expected an empty host")
	}
}

func TestHostDuplicateID(t *testing.T) {
	root := t.TempDir()
	writePlugin(t, root, "a-first", `{"v":1,"id":"dup","type":"url","url":{"template":"https://a/{binding}"}}`, "")
	writePlugin(t, root, "b-second", `{"v":1,"id":"dup","type":"url","url":{"template":"https://b/{binding}"}}`, "")
	h := NewHost(root, Options{})
	if err := h.Load(); err == nil {
		t.Fatal("expected a duplicate-id error")
	}
	if urls := h.URLManifests(); len(urls) != 1 || urls[0].URL.Template != "https://a/{binding}" {
		t.Fatalf("first manifest should win: %+v", urls)
	}
}

func TestHostReloadRestartsPlugins(t *testing.T) {
	root := t.TempDir()
	writePlugin(t, root, "demo", `{"v":1,"id":"demo","type":"exec","exec":{"bin":"./plugin.sh"}}`, echoScript)
	h := NewHost(root, Options{Timeout: 2 * time.Second})
	t.Cleanup(h.Shutdown)
	if err := h.Load(); err != nil {
		t.Fatal(err)
	}
	first := h.ExecPlugins()[0]
	if _, err := first.Query(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}
	if err := h.Load(); err != nil {
		t.Fatal(err)
	}
	second := h.ExecPlugins()[0]
	if first == second {
		t.Fatal("reload should replace the plugin instance")
	}
	got, err := second.Query(context.Background(), "again")
	if err != nil || len(got) != 1 {
		t.Fatalf("reloaded plugin query = (%+v, %v)", got, err)
	}
}

func TestHostActivateUnknownPlugin(t *testing.T) {
	h := NewHost(t.TempDir(), Options{})
	if err := h.Load(); err != nil {
		t.Fatal(err)
	}
	if err := h.Activate("nope", "r1"); err == nil {
		t.Fatal("expected an unknown-plugin error")
	}
}

func TestRegisterCallbackHandler(t *testing.T) {
	root := t.TempDir()
	dir := writePlugin(t, root, "demo", `{"v":1,"id":"demo","type":"exec","exec":{"bin":"./plugin.sh"}}`, echoScript)
	h := NewHost(root, Options{Timeout: 2 * time.Second})
	t.Cleanup(h.Shutdown)
	if err := h.Load(); err != nil {
		t.Fatal(err)
	}

	d := launch.NewDispatcher()
	RegisterCallbackHandler(d, h)

	// The plugin must be running before it can receive an activation.
	if _, err := h.ExecPlugins()[0].Query(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}
	err := d.Dispatch(providers.Action{
		Kind: providers.ActPluginCallback, PluginID: "demo", ResultID: "r1",
	})
	if err != nil {
		t.Fatal(err)
	}

	stamp := filepath.Join(dir, "activated.txt")
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(stamp); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("dispatcher never delivered the callback")
}

func TestRegisterCallbackHandlerNilSafe(t *testing.T) {
	RegisterCallbackHandler(nil, nil)
	RegisterCallbackHandler(launch.NewDispatcher(), nil)
}
