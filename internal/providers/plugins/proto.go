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
// "v": ProtoVersion. The host sends EventQuery, EventActivate, EventSubmit
// and EventShutdown (see Event); the plugin answers with EventResults and the
// optional EventActivated (see Message). Unknown events and unknown fields are
// ignored on both sides, so new kinds can be added without breaking either.
//
// A result may declare a "form" (see WireForm): activating it opens an input
// view inside the launcher instead of dispatching, and submission arrives as
// EventSubmit carrying the values keyed by field key. Degradation is
// symmetric and safe: a host too old to know forms drops the "form" field and
// activation falls back to a plain activate event, while a plugin too old to
// know EventSubmit ignores it. When "form" is present the result's "action"
// is ignored — submission always comes back as EventSubmit.
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
	// EventSubmit delivers a form result's submitted values. Like activate it
	// is fire-and-forget: the host does not wait for a reply.
	EventSubmit = "submit"
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
	// ID is the result's id (activate, submit).
	ID string `json:"id,omitempty"`
	// Values are the submitted form values keyed by field key (submit).
	Values map[string]string `json:"values,omitempty"`
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
	// Form, when present, makes activation open an in-launcher input view;
	// the submitted values come back as an EventSubmit. Action is ignored.
	Form *WireForm `json:"form"`
}

// WireAction is the action attached to a plugin result.
type WireAction struct {
	Kind string   `json:"kind"`
	URL  string   `json:"url"`
	Argv []string `json:"argv"`
	Text string   `json:"text"`
}

// WireForm is a declarative input form attached to a plugin result.
type WireForm struct {
	Title  string          `json:"title"`
	Fields []WireFormField `json:"fields"`
}

// WireFormField is one input in a WireForm.
type WireFormField struct {
	// Key keys the submitted value in EventSubmit's values map.
	Key         string `json:"key"`
	Label       string `json:"label"`
	Placeholder string `json:"placeholder"`
	Required    bool   `json:"required"`
	// Secret masks the input in the launcher. Degradation is symmetric: a
	// host too old to know the field ignores it and renders the input
	// unmasked, and a plugin too old to set it leaves it false — either way
	// the submitted value is unchanged.
	Secret bool `json:"secret"`
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
	res := providers.Result{
		ID:       "plugin:" + m.ID + ":" + w.ID,
		Title:    w.Title,
		Subtitle: w.Subtitle,
		Icon:     connectors.ResolveIcon(icon, m.Dir),
		Category: providers.CatPlugin,
		Score:    score,
		Accent:   accent,
		Action:   wireActionTo(w.Action, m.ID, w.ID),
	}
	if w.Form != nil {
		res.Form = wireFormTo(*w.Form, m.ID, w.ID)
	}
	return res
}

// wireFormTo converts a declarative wire form into a providers.Form whose
// Build sends the submitted values back to the plugin: the host-synthesized
// counterpart of a Go provider's Build closure.
func wireFormTo(f WireForm, pluginID, resultID string) *providers.Form {
	fields := make([]providers.FormField, len(f.Fields))
	for i, wf := range f.Fields {
		fields[i] = providers.FormField{
			Key:         wf.Key,
			Label:       wf.Label,
			Placeholder: wf.Placeholder,
			Required:    wf.Required,
			Secret:      wf.Secret,
		}
	}
	return &providers.Form{
		Title:  f.Title,
		Fields: fields,
		Build: func(values map[string]string) (providers.Action, error) {
			return providers.Action{
				Kind:     providers.ActPluginCallback,
				PluginID: pluginID,
				ResultID: resultID,
				Values:   values,
			}, nil
		},
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
