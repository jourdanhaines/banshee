package connectors

import (
	"errors"
	"fmt"

	"github.com/jourdanhaines/banshee/internal/launch"
	"github.com/jourdanhaines/banshee/internal/providers"
)

// ActConnectorLink writes a connector binding into a repo's
// .banshee/config.json. Payload: Argv = [connectorID, repoPath, binding].
const ActConnectorLink = "connector-link"

// errBindingRequired is returned by a link form's Build when the submitted
// binding is empty after trimming.
var errBindingRequired = errors.New("a project URL or ID is required")

// RegisterLinkHandler binds ActConnectorLink on d. The handler is a single
// small local file write, so it is safe to run synchronously on the GTK main
// loop — and staying synchronous guarantees the next query's config-cache
// read sees the new binding.
func RegisterLinkHandler(d *launch.Dispatcher) {
	d.Register(ActConnectorLink, func(a providers.Action) error {
		if len(a.Argv) != 3 {
			return fmt.Errorf("connector-link: want argv [connectorID repoPath binding], got %d values", len(a.Argv))
		}
		return SaveRepoBinding(a.Argv[1], a.Argv[0], a.Argv[2])
	})
}
