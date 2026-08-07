// Package plugins hosts user-supplied plugins found under
// config.PluginsDir(). Each plugin is a directory containing a manifest.json
// (see connectors.Manifest, schema v1):
//
//   - type "url" plugins are declarative connectors; the host hands their
//     manifests to the connectors provider via Provider.AddManifests.
//   - type "exec" plugins are long-running child processes that speak a
//     newline-delimited JSON protocol on stdin/stdout. Each becomes its own
//     providers.Provider.
//
// # The exec protocol
//
// One JSON object per line, in both directions, every message stamped with
// "v": ProtoVersion. The host sends EventQuery, EventActivate and
// EventShutdown (see Event); the plugin answers with EventResults and the
// optional EventActivated (see Message). Unknown events and unknown fields are
// ignored on both sides, so new kinds can be added without breaking either.
//
// Every query carries a Seq, and a plugin must echo the seq it is answering:
// the host drops any message whose seq is not the query it is still waiting
// on. Results for one seq may be split across several messages; the host
// merges them until one arrives with "done": true or the soft per-query
// timeout elapses, whichever comes first. Partial results are kept, late ones
// are discarded.
//
// # Process contract
//
// The child is started lazily, on the first query that passes the plugin's
// prefix gate (see ExecPlugin.MatchQuery), and lives until the host shuts it
// down. It runs in its own process group, with its working directory set to
// the plugin directory and these variables added to its environment:
//
//   - BANSHEE_PLUGIN_ID — the manifest id.
//   - BANSHEE_PLUGIN_DIR — absolute path of the plugin directory.
//   - BANSHEE_PLUGIN_PROTO — ProtoVersion, as a decimal string.
//
// Stdout carries protocol messages only: the host skips any line that does not
// start with '{', so one stray log line costs a plugin its results. Logs belong
// on stderr, which the daemon forwards to daemon.log. Output must be flushed
// after every message — runtimes that line-buffer only when stdout is a TTY
// (Python, Node) otherwise appear to answer nothing at all.
//
// Lifecycle and timing — the soft query timeout, crash backoff, the crash
// count that disables a plugin until the next reload, the line-length cap and
// the shutdown grace period — are defined and documented as constants in
// exec.go. The manifest schema for both plugin types is in
// connectors/manifest.go. plugins/example/ is a complete, commented exec
// plugin.
package plugins

import (
	"github.com/jourdanhaines/banshee/internal/providers"
	"github.com/jourdanhaines/banshee/internal/providers/connectors"
)

// ProtoVersion is the plugin protocol version stamped on every event the host
// sends. Plugins should ignore events carrying a version they do not know.
const ProtoVersion = 1

// Events sent by the host to a plugin's stdin.
const (
	// EventQuery asks the plugin for results for a query.
	EventQuery = "query"
	// EventActivate tells the plugin a result of its was activated.
	EventActivate = "activate"
	// EventShutdown asks the plugin to exit; the host closes stdin after it.
	EventShutdown = "shutdown"
)

// Events a plugin writes to stdout.
const (
	// EventResults carries results for a query seq.
	EventResults = "results"
	// EventActivated acknowledges an activate event. Purely informational —
	// the host does not wait for it.
	EventActivated = "activated"
)

// Action kinds a plugin may put on a result. They map onto the frozen
// providers action kinds.
const (
	// KindURL opens Action.URL with the system handler.
	KindURL = "url"
	// KindExecDetach runs Action.Argv detached from the daemon.
	KindExecDetach = "exec-detach"
	// KindCallback (the default) sends an activate event back to the plugin.
	KindCallback = "callback"
	// KindClipboard copies Action.Text to the system clipboard.
	KindClipboard = "clipboard"
)

// DefaultScore is used for plugin results that omit "score".
const DefaultScore = 50

// Event is one host → plugin message, written as a single JSON line.
type Event struct {
	V     int    `json:"v"`
	Event string `json:"event"`
	// Seq identifies a query; results must echo it back. Zero for shutdown.
	Seq uint64 `json:"seq,omitempty"`
	// Query is the user's query with the plugin's prefix stripped (query).
	Query string `json:"query,omitempty"`
	// ID is the activated result's id (activate).
	ID string `json:"id,omitempty"`
}

// Message is one plugin → host message, read as a single JSON line. Unknown
// fields are ignored.
type Message struct {
	V     int    `json:"v"`
	Seq   uint64 `json:"seq"`
	Event string `json:"event"`
	// Results may be sent across several messages for the same seq; the host
	// merges them until Done is true or the soft timeout elapses.
	Results []WireResult `json:"results"`
	Done    bool         `json:"done"`
	Error   string       `json:"error"`
}

// WireResult is a result as serialized by a plugin.
type WireResult struct {
	// ID must be stable enough to be echoed back in an activate event.
	ID       string `json:"id"`
	Title    string `json:"title"`
	Subtitle string `json:"subtitle"`
	// Icon is an icon-theme name, or a path relative to the plugin dir.
	Icon string `json:"icon"`
	// Accent overrides the manifest accent for this row.
	Accent string `json:"accent"`
	// Score ranks the row; omitted or zero means DefaultScore.
	Score  int         `json:"score"`
	Action *WireAction `json:"action"`
}

// WireAction is the action attached to a plugin result.
type WireAction struct {
	Kind string   `json:"kind"`
	URL  string   `json:"url"`
	Argv []string `json:"argv"`
	Text string   `json:"text"`
}

// toResult converts a wire result into a launcher result, filling icon and
// accent from the plugin's manifest when the result omits them.
func (w WireResult) toResult(m connectors.Manifest) providers.Result {
	score := w.Score
	if score == 0 {
		score = DefaultScore
	}
	accent := m.Accent
	if w.Accent != "" {
		accent = w.Accent
	}
	icon := m.Icon
	if w.Icon != "" {
		icon = w.Icon
	}
	return providers.Result{
		ID:       "plugin:" + m.ID + ":" + w.ID,
		Title:    w.Title,
		Subtitle: w.Subtitle,
		Icon:     connectors.ResolveIcon(icon, m.Dir),
		Category: providers.CatPlugin,
		Score:    score,
		Accent:   accent,
		Action:   wireActionTo(w.Action, m.ID, w.ID),
	}
}

func wireActionTo(a *WireAction, pluginID, resultID string) providers.Action {
	callback := providers.Action{
		Kind:     providers.ActPluginCallback,
		PluginID: pluginID,
		ResultID: resultID,
	}
	if a == nil {
		return callback
	}
	switch a.Kind {
	case KindURL:
		if a.URL == "" {
			return callback
		}
		return providers.Action{Kind: providers.ActURL, URL: a.URL}
	case KindExecDetach:
		if len(a.Argv) == 0 {
			return callback
		}
		return providers.Action{Kind: providers.ActExecDetach, Argv: a.Argv}
	case KindClipboard:
		if a.Text == "" {
			return callback
		}
		return providers.Action{Kind: providers.ActClipboardCopy, Text: a.Text}
	default:
		return callback
	}
}
