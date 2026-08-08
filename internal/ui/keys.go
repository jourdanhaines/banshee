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
	// KeyDeleteWord deletes the word before the cursor in the query entry
	// (Ctrl-W, readline/vim style).
	KeyDeleteWord
	// KeyFormCancel slides an open form away, back to the results view
	// (Esc while a form is open — the window stays up).
	KeyFormCancel
	// KeyFormSubmit validates and submits an open form (Enter).
	KeyFormSubmit
)

// UIMode selects the keymap: the results list or an open form.
type UIMode int

const (
	ModeResults UIMode = iota
	ModeForm
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
	case KeyDeleteWord:
		return "delete-word"
	case KeyFormCancel:
		return "form-cancel"
	case KeyFormSubmit:
		return "form-submit"
	default:
		return "pass"
	}
}

// KeyFor maps a GDK keyval plus modifier state onto a KeyAction for the
// given UI mode.
//
// ModeResults bindings are the ones the plan freezes: Esc hides; Down/Ctrl-J
// and Up/Ctrl-K move the selection while the entry keeps focus; Enter
// activates; Tab and Shift+Enter take the alternate action. Ctrl-J/Ctrl-K are
// matched on the letter keyvals so they work regardless of Shift or Caps
// Lock, and Tab is matched in both its plain and Shift (ISO_Left_Tab) forms.
//
// ModeForm is deliberately minimal: Esc cancels back to the results, Enter
// submits, and everything else — including Tab — passes through so GTK's own
// focus chain moves between the form's fields and text editing keeps working.
func KeyFor(keyval uint, state gdk.ModifierType, mode UIMode) KeyAction {
	if mode == ModeForm {
		switch keyval {
		case gdk.KEY_Escape:
			return KeyFormCancel
		case gdk.KEY_Return, gdk.KEY_KP_Enter, gdk.KEY_ISO_Enter:
			return KeyFormSubmit
		}
		return KeyPass
	}

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
	case gdk.KEY_w, gdk.KEY_W:
		if ctrl {
			return KeyDeleteWord
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
		switch KeyFor(keyval, state, l.mode()) {
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
		case KeyDeleteWord:
			l.deleteWordBeforeCursor()
		case KeyFormCancel:
			l.closeForm(true)
		case KeyFormSubmit:
			// Not always ours: Enter inside a form dropdown belongs to the
			// widget. The decision needs the focused widget, which KeyFor
			// (a pure keyval→action mapping, testable without a display)
			// deliberately knows nothing about, so it lives on the launcher.
			if !l.submitFormOrPass() {
				return false
			}
		default:
			return false // not ours: let the focused widget have it
		}
		return true
	})
	return keys
}
