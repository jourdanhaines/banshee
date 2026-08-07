package apps

import (
	"fmt"

	"github.com/diamondburned/gotk4/pkg/gio/v2"
)

// GIOSource lists applications through GIO's AppInfo API. GIO applies the
// freedesktop visibility rules (NoDisplay, Hidden, OnlyShowIn/NotShowIn,
// TryExec) in ShouldShow, so banshee does not reimplement them.
type GIOSource struct {
	// DesktopDirs overrides the directories searched for the backing .desktop
	// files, which supply GenericName and Keywords (not exposed by the GAppInfo
	// interface). Empty means DesktopDirs().
	DesktopDirs []string
}

var _ Source = GIOSource{}

// Apps implements Source.
func (s GIOSource) Apps() ([]App, error) {
	infos := gio.AppInfoGetAll()
	dirs := s.DesktopDirs
	if len(dirs) == 0 {
		dirs = DesktopDirs()
	}
	enr := newEnricher(dirs)

	out := make([]App, 0, len(infos))
	for _, info := range infos {
		if info == nil || !info.ShouldShow() {
			continue
		}
		id, name := info.ID(), info.DisplayName()
		if id == "" || name == "" {
			continue
		}
		a := App{
			ID:          id,
			Name:        name,
			Description: info.Description(),
			Executable:  info.Executable(),
			Commandline: info.Commandline(),
		}
		a.GenericName, a.Keywords = enr.lookup(id)
		out = append(out, a)
	}
	return out, nil
}

// GIOLauncher launches applications through GIO.
type GIOLauncher struct{}

var _ Launcher = GIOLauncher{}

// LaunchApp implements Launcher. It resolves the desktop ID against the live
// application list and launches it with no files and the default launch
// context, which honors Terminal=true and Exec= field codes.
func (GIOLauncher) LaunchApp(id string) error {
	for _, info := range gio.AppInfoGetAll() {
		if info != nil && info.ID() == id {
			return info.Launch(nil, nil)
		}
	}
	return fmt.Errorf("apps: no application with desktop id %q", id)
}
