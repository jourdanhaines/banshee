package config

// RepoConfig is <repo>/.banshee/config.json (schema v1). Unknown keys are
// ignored for forward compatibility.
//
// Connector values are either a full absolute URL (used verbatim) or an
// opaque binding substituted into the connector's URL template as {binding}.
type RepoConfig struct {
	V int `json:"v"`
	// Connectors maps connector/plugin id → binding or URL.
	Connectors map[string]string `json:"connectors"`
}

// RepoConfigRelPath is the in-repo location of the per-repo config.
const RepoConfigRelPath = ".banshee/config.json"
