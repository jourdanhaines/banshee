// Package layershell wraps gtk4-layer-shell behind a small API so the rest of
// banshee never imports the binding directly. If the upstream Go binding ever
// breaks against a newer gotk4, this package is reimplemented as a ~100-line
// cgo shim (pkg-config: gtk4-layer-shell-0) with no callers changing.
package layershell

import (
	layershell "github.com/diamondburned/gotk4-layer-shell/pkg/gtk4layershell"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
)

// KeyboardMode mirrors the layer-shell keyboard interactivity modes banshee
// supports (config key keyboard_mode).
type KeyboardMode int

const (
	KeyboardExclusive KeyboardMode = iota
	KeyboardOnDemand
)

// Supported reports whether the compositor speaks wlr-layer-shell.
func Supported() bool { return layershell.IsSupported() }

// Setup configures win as banshee's launcher surface: overlay layer,
// namespace "banshee" (targeted by Hyprland layerrules), anchored to the top
// edge only (horizontally centered by the compositor), offset topMargin px
// from the top. Must be called before the window is realized.
func Setup(win *gtk.Window, mode KeyboardMode, topMargin int) {
	layershell.InitForWindow(win)
	layershell.SetNamespace(win, "banshee")
	layershell.SetLayer(win, layershell.LayerShellLayerOverlay)
	layershell.SetAnchor(win, layershell.LayerShellEdgeTop, true)
	layershell.SetMargin(win, layershell.LayerShellEdgeTop, topMargin)
	SetKeyboardMode(win, mode)
}

// SetKeyboardMode (re-)asserts keyboard interactivity; called on every show
// to work around Hyprland focus quirks.
func SetKeyboardMode(win *gtk.Window, mode KeyboardMode) {
	switch mode {
	case KeyboardOnDemand:
		layershell.SetKeyboardMode(win, layershell.LayerShellKeyboardModeOnDemand)
	default:
		layershell.SetKeyboardMode(win, layershell.LayerShellKeyboardModeExclusive)
	}
}
