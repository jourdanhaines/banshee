package boot

import (
	"context"
	"log"
	"sync"

	"github.com/jourdanhaines/banshee/internal/daemon"
	"github.com/jourdanhaines/banshee/internal/index"
	"github.com/jourdanhaines/banshee/internal/providers"
	"github.com/jourdanhaines/banshee/internal/providers/plugins"
)

// pluginSet is one providers.Provider standing in for every exec plugin.
//
// The frozen providers.Registry can only append, but `banshee reload` replaces
// the whole plugin set (plugins.Host.Load tears the children down and starts
// them again). Registering this indirection once means reload needs no
// registry surgery: it reads through to the host on every query.
type pluginSet struct {
	host *plugins.Host
}

// Name implements providers.Provider.
func (p *pluginSet) Name() string { return "plugins" }

// Query fans out to every loaded exec plugin concurrently. Individual plugin
// failures (a crash, a disabled plugin, a soft timeout) are dropped rather
// than failing the whole query — the aggregator would otherwise lose the
// entire plugin category because one plugin misbehaved.
func (p *pluginSet) Query(ctx context.Context, q string) ([]providers.Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	set := p.host.Providers()
	if len(set) == 0 {
		return nil, nil
	}

	var (
		mu  sync.Mutex
		out []providers.Result
		wg  sync.WaitGroup
	)
	for _, pl := range set {
		wg.Add(1)
		go func(pl providers.Provider) {
			defer wg.Done()
			res, err := pl.Query(ctx, q)
			if err != nil || len(res) == 0 {
				return
			}
			mu.Lock()
			out = append(out, res...)
			mu.Unlock()
		}(pl)
	}
	wg.Wait()

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// reindexOnShow decorates the launcher window with the plan's "async reindex
// on show" behavior: every time the launcher appears with a stale repo cache,
// a rescan runs in the background so the next keystroke sees repos cloned
// since the daemon started. The scan never blocks the show.
type reindexOnShow struct {
	daemon.UI
	idx *index.Scanner
	log *log.Logger

	mu       sync.Mutex
	scanning bool
}

// Show reveals the launcher and kicks off a background rescan when the repo
// cache has aged past its TTL.
func (r *reindexOnShow) Show(query string) {
	r.UI.Show(query)
	r.maybeRescan()
}

// Reload refreshes the UI. The daemon-level reload hook has already forced a
// rescan, so nothing extra happens here.
func (r *reindexOnShow) Reload() { r.UI.Reload() }

func (r *reindexOnShow) maybeRescan() {
	if r.idx == nil || !r.idx.Stale() {
		return
	}
	r.mu.Lock()
	if r.scanning {
		r.mu.Unlock()
		return
	}
	r.scanning = true
	r.mu.Unlock()

	go func() {
		defer func() {
			r.mu.Lock()
			r.scanning = false
			r.mu.Unlock()
		}()
		if err := r.idx.Rescan(); err != nil && r.log != nil {
			r.log.Printf("index: background rescan: %v", err)
		}
	}()
}

// compile-time checks.
var (
	_ providers.Provider = (*pluginSet)(nil)
	_ daemon.UI          = (*reindexOnShow)(nil)
)
