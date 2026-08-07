package ui

import (
	"github.com/diamondburned/gotk4/pkg/gdk/v4"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
)

// KeyAction is the launcher's interpretation of a key press. Keeping the
// decision in a plain function makes the whole keymap unit testable without a
// display; newKeyController is then just plumbing.
type KeyAction int

const (
	// KeyPass means banshee does not handle the key; it falls through to the
	// entry (ordinary typing, text editing, clipboard).
	KeyPass KeyAction = iota
	// KeyHide closes the launcher (Esc).
	KeyHide
	// KeyNext moves the selection down (Down, Ctrl-J).
	KeyNext
	// KeyPrev moves the selection up (Up, Ctrl-K).
	KeyPrev
	// KeyActivate runs the selected result's primary action (Enter).
	KeyActivate
	// KeyActivateAlt runs the selected result's AltAction (Tab, Shift+Enter).
	KeyActivateAlt
)

// String implements fmt.Stringer for readable test failures.
func (a KeyAction) String() string {
	switch a {
	case KeyHide:
		return "hide"
	case KeyNext:
		return "next"
	case KeyPrev:
		return "prev"
	case KeyActivate:
		return "activate"
	case KeyActivateAlt:
		return "activate-alt"
	default:
		return "pass"
	}
}

// KeyFor maps a GDK keyval plus modifier state onto a KeyAction.
//
// The bindings are the ones the plan freezes: Esc hides; Down/Ctrl-J and
// Up/Ctrl-K move the selection while the entry keeps focus; Enter activates;
// Tab and Shift+Enter take the alternate action. Ctrl-J/Ctrl-K are matched on
// the letter keyvals so they work regardless of Shift or Caps Lock, and Tab is
// matched in both its plain and Shift (ISO_Left_Tab) forms.
func KeyFor(keyval uint, state gdk.ModifierType) KeyAction {
	ctrl := state&gdk.ControlMask != 0
	shift := state&gdk.ShiftMask != 0

	switch keyval {
	case gdk.KEY_Escape:
		return KeyHide
	case gdk.KEY_Down:
		return KeyNext
	case gdk.KEY_Up:
		return KeyPrev
	case gdk.KEY_Tab, gdk.KEY_ISO_Left_Tab, gdk.KEY_KP_Tab:
		return KeyActivateAlt
	case gdk.KEY_Return, gdk.KEY_KP_Enter, gdk.KEY_ISO_Enter:
		if shift {
			return KeyActivateAlt
		}
		return KeyActivate
	case gdk.KEY_j, gdk.KEY_J:
		if ctrl {
			return KeyNext
		}
	case gdk.KEY_k, gdk.KEY_K:
		if ctrl {
			return KeyPrev
		}
	case gdk.KEY_n, gdk.KEY_N:
		if ctrl {
			return KeyNext
		}
	case gdk.KEY_p, gdk.KEY_P:
		if ctrl {
			return KeyPrev
		}
	}
	return KeyPass
}

// newKeyController returns the window-level key controller. It runs in the
// capture phase so navigation keys are intercepted before the focused entry
// consumes them — the entry must keep focus (so typing keeps working) while
// the arrow keys drive the list.
func (l *Launcher) newKeyController() *gtk.EventControllerKey {
	keys := gtk.NewEventControllerKey()
	keys.SetPropagationPhase(gtk.PhaseCapture)
	keys.ConnectKeyPressed(func(keyval, _ uint, state gdk.ModifierType) bool {
		switch KeyFor(keyval, state) {
		case KeyHide:
			l.Hide()
		case KeyNext:
			l.MoveSelection(1)
		case KeyPrev:
			l.MoveSelection(-1)
		case KeyActivate:
			l.Activate(false)
		case KeyActivateAlt:
			l.Activate(true)
		default:
			return false // not ours: let the entry have it
		}
		return true
	})
	return keys
}
