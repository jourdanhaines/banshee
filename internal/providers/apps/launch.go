package apps

import (
	"errors"

	"github.com/jourdanhaines/banshee/internal/launch"
	"github.com/jourdanhaines/banshee/internal/providers"
)

// Launcher starts an application by .desktop ID.
type Launcher interface {
	LaunchApp(id string) error
}

// LauncherFunc adapts a function to the Launcher interface.
type LauncherFunc func(id string) error

// LaunchApp implements Launcher.
func (f LauncherFunc) LaunchApp(id string) error { return f(id) }

// ErrNoAppID is returned when an ActAppLaunch action carries no desktop ID.
var ErrNoAppID = errors.New("apps: app-launch action carries no desktop id")

// RegisterAppLaunchHandler teaches d how to run ActAppLaunch actions, using
// GIO to start the application (which handles Terminal=true entries and Exec=
// field codes correctly).
func RegisterAppLaunchHandler(d *launch.Dispatcher) {
	RegisterAppLaunchHandlerWith(d, GIOLauncher{})
}

// RegisterAppLaunchHandlerWith is RegisterAppLaunchHandler with an injectable
// launcher, for tests and alternate backends.
func RegisterAppLaunchHandlerWith(d *launch.Dispatcher, l Launcher) {
	d.Register(ActAppLaunch, func(a providers.Action) error {
		if len(a.Argv) == 0 || a.Argv[0] == "" {
			return ErrNoAppID
		}
		return l.LaunchApp(a.Argv[0])
	})
}
