// Package connectors turns a matched repository into "open this repo
// somewhere" results: GitHub (derived from the repo's git origin remote) plus
// any number of declarative URL connectors — either compiled in (Railway) or
// contributed by a user plugin whose manifest.json has type "url".
//
// Connectors are pure data: a Manifest describes an id, a display name, an
// icon and a URL template. A repo opts into a connector by binding it in
// <repo>/.banshee/config.json:
//
//	{"v":1,"connectors":{"railway":"6f1a...-project-id"}}
//
// The bound value is either an absolute URL (used verbatim) or an opaque
// binding substituted into the connector's template as {binding}.
package connectors

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/jourdanhaines/banshee/internal/config"
	"github.com/jourdanhaines/banshee/internal/icons"
	"github.com/jourdanhaines/banshee/internal/providers"
)

// ManifestVersion is the manifest.json schema version understood by this
// build. Manifests declaring another version are rejected.
const ManifestVersion = 1

// MaxExecTimeoutMS caps exec.timeout_ms. The aggregator joins every provider
// before it returns, so a plugin's soft timeout is an upper bound on how long
// the whole launcher takes to paint: the last keystroke's context is only
// cancelled when the window hides, and until then the user stares at an empty
// list. The plan's soft timeout is 150 ms; this leaves generous headroom for a
// plugin that really does need to shell out, and clamps anything beyond it.
const MaxExecTimeoutMS = 2000

// Manifest types.
const (
	// TypeURL is a declarative connector: a repo-bound URL template.
	TypeURL = "url"
	// TypeExec is a long-running child process speaking the JSON-lines
	// plugin protocol (see internal/providers/plugins).
	TypeExec = "exec"
)

// Manifest is a parsed plugins/<id>/manifest.json (schema v1). It is shared
// by both plugin types so a plugin directory can be read once and routed by
// Type: url-type manifests are handed to the connectors Provider, exec-type
// manifests to the plugin host.
//
// Unknown JSON keys are ignored for forward compatibility.
type Manifest struct {
	// V is the schema version; must equal ManifestVersion.
	V int `json:"v"`
	// ID is the connector/plugin id. It is the key used in a repo's
	// .banshee/config.json "connectors" map and must be filename-safe.
	ID string `json:"id"`
	// Name is the human-readable name shown in result titles.
	Name string `json:"name"`
	// Icon is either an icon-theme name ("network-wireless-symbolic") or a
	// file path relative to the plugin directory ("railway.svg"). A value
	// containing '/' or '.' is treated as a path.
	Icon string `json:"icon"`
	// Accent is an optional CSS color for the result's badge.
	Accent string `json:"accent"`
	// Type is TypeURL or TypeExec.
	Type string `json:"type"`
	// URL is required when Type is TypeURL.
	URL *URLSpec `json:"url"`
	// Exec is required when Type is TypeExec.
	Exec *ExecSpec `json:"exec"`

	// Dir is the directory the manifest was loaded from; icon paths and
	// relative exec binaries resolve against it. Empty for compiled-in
	// connectors. Not part of the JSON schema.
	Dir string `json:"-"`
	// Category overrides the result category for url-type connectors.
	// Defaults to providers.CatConnector; the built-in GitHub connector uses
	// providers.CatGitHub. Not part of the JSON schema.
	Category providers.Category `json:"-"`
	// DeriveOrigin makes an unbound repo fall back to the URL derived from
	// its git origin remote. Only the built-in GitHub connector sets it.
	// Not part of the JSON schema.
	DeriveOrigin bool `json:"-"`
}

// URLSpec is the declarative half of a url-type manifest.
type URLSpec struct {
	// Template is the URL built for a repo. Placeholders {binding}, {repo}
	// and {path} are substituted.
	Template string `json:"template"`
	// Title is the result title template; defaults to "Open {repo} on
	// {name}". Placeholders {repo}, {path}, {binding} and {name} are
	// substituted.
	Title string `json:"title"`
	// RequiresBinding hides the connector for repos that have no binding
	// (and, for GitHub, no derivable origin). Defaults to false, meaning the
	// connector shows for every matched repo.
	RequiresBinding bool `json:"requires_binding"`
}

// ExecSpec is the process half of an exec-type manifest.
type ExecSpec struct {
	// Bin is the plugin executable. A value containing '/' resolves relative
	// to the plugin directory; otherwise it is looked up on $PATH.
	Bin string `json:"bin"`
	// Args are extra arguments passed before the plugin starts reading
	// events.
	Args []string `json:"args"`
	// Prefix gates the plugin: when set, it only receives queries starting
	// with the prefix, and the prefix is stripped before sending.
	Prefix string `json:"prefix"`
	// TimeoutMS is the soft per-query timeout in milliseconds. Zero uses the
	// host default (150ms); anything above MaxExecTimeoutMS is clamped to it.
	TimeoutMS int `json:"timeout_ms"`
}

var idRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*$`)

// ValidIdentifier reports whether id is a well-formed connector/plugin id.
// Callers without a loaded manifest set (the CLI link verb) use it to accept
// any plausible id rather than only the builtins.
func ValidIdentifier(id string) bool { return idRe.MatchString(id) }

// ParseManifest decodes a manifest.json body loaded from dir and validates it.
// dir may be empty for manifests that are not backed by a directory.
func ParseManifest(data []byte, dir string) (Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return Manifest{}, fmt.Errorf("manifest: %w", err)
	}
	m.Dir = dir
	if m.Name == "" {
		m.Name = m.ID
	}
	if m.Category == 0 {
		m.Category = providers.CatConnector
	}
	if err := m.Validate(); err != nil {
		return Manifest{}, err
	}
	// Clamp rather than reject: an over-eager timeout is a tuning mistake, not
	// a broken manifest, and forward compatibility says a plugin written for a
	// future banshee must still load here.
	if m.Exec != nil && m.Exec.TimeoutMS > MaxExecTimeoutMS {
		m.Exec.TimeoutMS = MaxExecTimeoutMS
	}
	return m, nil
}

// Validate reports whether the manifest is well-formed and supported.
func (m Manifest) Validate() error {
	if m.V != ManifestVersion {
		return fmt.Errorf("manifest %q: unsupported version %d (want %d)", m.ID, m.V, ManifestVersion)
	}
	if !idRe.MatchString(m.ID) {
		return fmt.Errorf("manifest: invalid id %q", m.ID)
	}
	switch m.Type {
	case TypeURL:
		if m.URL == nil || strings.TrimSpace(m.URL.Template) == "" {
			return fmt.Errorf("manifest %q: url.template is required for type %q", m.ID, TypeURL)
		}
	case TypeExec:
		if m.Exec == nil || strings.TrimSpace(m.Exec.Bin) == "" {
			return fmt.Errorf("manifest %q: exec.bin is required for type %q", m.ID, TypeExec)
		}
		if m.Exec.TimeoutMS < 0 {
			return fmt.Errorf("manifest %q: exec.timeout_ms must not be negative", m.ID)
		}
	case "":
		return fmt.Errorf("manifest %q: type is required (%q or %q)", m.ID, TypeURL, TypeExec)
	default:
		return fmt.Errorf("manifest %q: unknown type %q", m.ID, m.Type)
	}
	return nil
}

// ResolveIcon maps a manifest or plugin-result icon string to a providers.Icon.
// A name matching an icon compiled into the binary (internal/icons) wins; a
// value containing '/' or '.' is a file path (relative values resolve against
// dir); anything else is an icon-theme name.
func ResolveIcon(icon, dir string) providers.Icon {
	icon = strings.TrimSpace(icon)
	if icon == "" {
		return providers.Icon{}
	}
	if !strings.ContainsAny(icon, "/.") {
		if icons.Has(icon) {
			return providers.Icon{Builtin: icon}
		}
		return providers.Icon{ThemeName: icon}
	}
	p := config.ExpandPath(icon)
	if !filepath.IsAbs(p) && dir != "" {
		p = filepath.Join(dir, p)
	}
	return providers.Icon{Path: p}
}

// expand substitutes {key} placeholders in tmpl.
func expand(tmpl string, vars map[string]string) string {
	if tmpl == "" {
		return ""
	}
	out := tmpl
	for k, v := range vars {
		out = strings.ReplaceAll(out, "{"+k+"}", v)
	}
	return out
}
