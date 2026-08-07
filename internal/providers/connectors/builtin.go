package connectors

import "github.com/jourdanhaines/banshee/internal/providers"

// Built-in connector ids.
const (
	// IDGitHub is the compiled-in GitHub connector.
	IDGitHub = "github"
	// IDRailway is the compiled-in Railway connector.
	IDRailway = "railway"
)

// Builtins returns the compiled-in url connectors, in display order. They are
// ordinary manifests: a user can override either one by shipping a plugin with
// the same id, and a repo binds them exactly like any other connector.
//
// GitHub is special only in that an unbound repo falls back to the URL derived
// from its git origin remote, and its results use providers.CatGitHub.
func Builtins() []Manifest {
	return []Manifest{
		{
			V:      ManifestVersion,
			ID:     IDGitHub,
			Name:   "GitHub",
			Icon:   "github",
			Accent: "#8b949e",
			Type:   TypeURL,
			URL: &URLSpec{
				Template:        "https://github.com/{binding}",
				Title:           "Open {repo} on GitHub",
				RequiresBinding: true,
			},
			Category:     providers.CatGitHub,
			DeriveOrigin: true,
		},
		{
			V:      ManifestVersion,
			ID:     IDRailway,
			Name:   "Railway",
			Icon:   "railway",
			Accent: "#a78bfa",
			Type:   TypeURL,
			URL: &URLSpec{
				Template:        "https://railway.com/project/{binding}",
				Title:           "Open {repo} on Railway",
				RequiresBinding: true,
			},
			Category: providers.CatConnector,
		},
	}
}
