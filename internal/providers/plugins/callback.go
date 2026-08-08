package plugins

import (
	"github.com/jourdanhaines/banshee/internal/launch"
	"github.com/jourdanhaines/banshee/internal/providers"
)

// RegisterCallbackHandler teaches the launch dispatcher how to run
// providers.ActPluginCallback actions: the activation is forwarded to the
// plugin that produced the result as an "activate" event — or, when the
// action carries submitted form values, as a "submit" event. One kind, two
// payload shapes: a form submission is still the plugin's own result coming
// back to it.
func RegisterCallbackHandler(d *launch.Dispatcher, host *Host) {
	if d == nil || host == nil {
		return
	}
	d.Register(providers.ActPluginCallback, func(a providers.Action) error {
		if a.Values != nil {
			return host.Submit(a.PluginID, a.ResultID, a.Values)
		}
		return host.Activate(a.PluginID, a.ResultID)
	})
}
