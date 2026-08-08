package theme

import (
	"errors"
	"sync"

	"github.com/diamondburned/gotk4/pkg/gdk/v4"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"

	"github.com/jourdanhaines/banshee/internal/config"
)

// errNoDisplay is returned when Load is called before GTK has a display —
// almost always a call ordered before gtk.Application's activate handler.
var errNoDisplay = errors.New("theme: no GDK display")

var (
	mu        sync.Mutex
	installed *gtk.CSSProvider
	onDisplay *gdk.Display
)

// Load renders the stylesheet for cfg and installs it on display at
// GTK_STYLE_PROVIDER_PRIORITY_APPLICATION, so it beats the system theme but
// still loses to a user's own ~/.config/gtk-4.0/gtk.css overrides.
//
// Load is idempotent: a second call (banshee reload with an edited
// banshee.conf) removes the provider installed by the previous call before
// adding the new one. It must run on the GTK main thread and after GTK is
// initialized, i.e. from the application's activate/startup handler.
//
// CSS parse errors are not fatal: GTK drops the offending declaration and logs
// it through GLib (which gotk4 forwards to log/slog), and the launcher stays
// usable. Note that banshee deliberately does *not* connect to the provider's
// parsing-error signal — that handler double-frees the GError in gotk4
// pkg/v0.4.0 and aborts the process. TestGeneratedCSSParses catches bad
// declarations at build time instead. Load therefore always returns nil today;
// the error return is kept so a future stylesheet source that can fail (a user
// override file under ~/.config/banshee/theme.css, say) does not change every
// call site.
func Load(display *gdk.Display, cfg config.Config) error {
	if display == nil {
		return errNoDisplay
	}

	mu.Lock()
	defer mu.Unlock()

	p := gtk.NewCSSProvider()
	p.LoadFromString(Render(cfg))

	if installed != nil && onDisplay != nil {
		gtk.StyleContextRemoveProviderForDisplay(onDisplay, installed)
	}
	gtk.StyleContextAddProviderForDisplay(display, p, gtk.STYLE_PROVIDER_PRIORITY_APPLICATION)

	installed, onDisplay = p, display
	return nil
}
