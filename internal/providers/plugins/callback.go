package plugins

import (
	"github.com/jourdanhaines/banshee/internal/launch"
	"github.com/jourdanhaines/banshee/internal/providers"
)

// RegisterCallbackHandler teaches the launch dispatcher how to run
// providers.ActPluginCallback actions: the activation is forwarded to the
// plugin that produced the result as an "activate" event.
func RegisterCallbackHandler(d *launch.Dispatcher, host *Host) {
	if d == nil || host == nil {
		return
	}
	d.Register(providers.ActPluginCallback, func(a providers.Action) error {
		return host.Activate(a.PluginID, a.ResultID)
	})
}
