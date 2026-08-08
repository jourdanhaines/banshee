package plugins

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/jourdanhaines/banshee/internal/providers"
	"github.com/jourdanhaines/banshee/internal/providers/connectors"
)

// ManifestName is the file every plugin directory must contain.
const ManifestName = "manifest.json"

// Host owns the user plugin directory: it scans <dir>/<id>/manifest.json,
// splits the manifests by type and manages the lifetime of exec plugins.
//
// Typical wiring in the daemon:
//
//	host := plugins.NewHost(config.PluginsDir(), plugins.Options{})
//	err := host.Load()
//	conn := connectors.New(idx, fuzzy.Score)
//	conn.AddManifests(host.URLManifests()...)
//	reg.Register(conn)
//	for _, p := range host.Providers() { reg.Register(p) }
//	plugins.RegisterCallbackHandler(dispatcher, host)
type Host struct {
	dir  string
	opts Options

	mu    sync.RWMutex
	urls  []connectors.Manifest
	execs []*ExecPlugin
	byID  map[string]*ExecPlugin
}

// NewHost returns a host for the plugin directory dir. Nothing is read until
// Load is called.
func NewHost(dir string, opts Options) *Host {
	return &Host{dir: dir, opts: opts.withDefaults(), byID: map[string]*ExecPlugin{}}
}

// Dir returns the plugin directory being watched.
func (h *Host) Dir() string { return h.dir }

// Load (re)scans the plugin directory. Previously started exec plugins are
// shut down first, so Load doubles as the "reload" that re-enables plugins
// disabled by repeated crashes.
//
// A malformed plugin never blocks the others: its error is collected and
// returned joined, while every valid plugin is still loaded. A missing plugin
// directory is not an error.
func (h *Host) Load() error {
	h.Shutdown()

	entries, err := os.ReadDir(h.dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			h.mu.Lock()
			h.urls, h.execs, h.byID = nil, nil, map[string]*ExecPlugin{}
			h.mu.Unlock()
			return nil
		}
		return err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

	var (
		urls  []connectors.Manifest
		execs []*ExecPlugin
		byID  = map[string]*ExecPlugin{}
		seen  = map[string]bool{}
		errs  []error
	)
	for _, e := range entries {
		dir := filepath.Join(h.dir, e.Name())
		// Stat rather than DirEntry.IsDir so symlinked plugin directories
		// (a common way to develop a plugin in place) are followed.
		if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, ManifestName))
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				errs = append(errs, err)
			}
			continue
		}
		m, err := connectors.ParseManifest(data, dir)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", dir, err))
			continue
		}
		if seen[m.ID] {
			errs = append(errs, fmt.Errorf("%s: duplicate plugin id %q", dir, m.ID))
			continue
		}
		seen[m.ID] = true

		switch m.Type {
		case connectors.TypeURL:
			urls = append(urls, m)
		case connectors.TypeExec:
			p, err := NewExecPlugin(m, h.opts)
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", dir, err))
				continue
			}
			execs = append(execs, p)
			byID[m.ID] = p
		}
	}

	h.mu.Lock()
	h.urls, h.execs, h.byID = urls, execs, byID
	h.mu.Unlock()
	return errors.Join(errs...)
}

// URLManifests returns the url-type plugin manifests, in directory order.
// Feed them to connectors.Provider.AddManifests.
func (h *Host) URLManifests() []connectors.Manifest {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return append([]connectors.Manifest(nil), h.urls...)
}

// Providers returns one providers.Provider per exec plugin.
func (h *Host) Providers() []providers.Provider {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]providers.Provider, 0, len(h.execs))
	for _, p := range h.execs {
		out = append(out, p)
	}
	return out
}

// ExecPlugins returns the loaded exec plugins.
func (h *Host) ExecPlugins() []*ExecPlugin {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return append([]*ExecPlugin(nil), h.execs...)
}

// Activate forwards a plugin-callback activation to the owning plugin.
func (h *Host) Activate(pluginID, resultID string) error {
	h.mu.RLock()
	p := h.byID[pluginID]
	h.mu.RUnlock()
	if p == nil {
		return fmt.Errorf("plugins: unknown plugin %q", pluginID)
	}
	return p.Activate(resultID)
}

// Submit forwards a form result's submitted values to the owning plugin.
func (h *Host) Submit(pluginID, resultID string, values map[string]string) error {
	h.mu.RLock()
	p := h.byID[pluginID]
	h.mu.RUnlock()
	if p == nil {
		return fmt.Errorf("plugins: unknown plugin %q", pluginID)
	}
	return p.Submit(resultID, values)
}

// Shutdown stops every running exec plugin.
func (h *Host) Shutdown() {
	h.mu.RLock()
	execs := append([]*ExecPlugin(nil), h.execs...)
	h.mu.RUnlock()
	var wg sync.WaitGroup
	for _, p := range execs {
		wg.Add(1)
		go func(p *ExecPlugin) { defer wg.Done(); p.Shutdown() }(p)
	}
	wg.Wait()
}
