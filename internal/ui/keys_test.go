package ui

import (
	"testing"

	"github.com/diamondburned/gotk4/pkg/gdk/v4"
)

func TestKeyFor(t *testing.T) {
	tests := []struct {
		name   string
		keyval uint
		state  gdk.ModifierType
		mode   UIMode
		want   KeyAction
	}{
		{"escape hides", gdk.KEY_Escape, 0, ModeResults, KeyHide},
		{"escape with modifiers still hides", gdk.KEY_Escape, gdk.ControlMask | gdk.ShiftMask, ModeResults, KeyHide},

		{"down moves next", gdk.KEY_Down, 0, ModeResults, KeyNext},
		{"up moves prev", gdk.KEY_Up, 0, ModeResults, KeyPrev},

		{"ctrl-j moves next", gdk.KEY_j, gdk.ControlMask, ModeResults, KeyNext},
		{"ctrl-k moves prev", gdk.KEY_k, gdk.ControlMask, ModeResults, KeyPrev},
		{"ctrl-shift-J moves next", gdk.KEY_J, gdk.ControlMask | gdk.ShiftMask, ModeResults, KeyNext},
		{"ctrl-shift-K moves prev", gdk.KEY_K, gdk.ControlMask | gdk.ShiftMask, ModeResults, KeyPrev},
		{"ctrl-n moves next", gdk.KEY_n, gdk.ControlMask, ModeResults, KeyNext},
		{"ctrl-p moves prev", gdk.KEY_p, gdk.ControlMask, ModeResults, KeyPrev},
		{"ctrl-w deletes word", gdk.KEY_w, gdk.ControlMask, ModeResults, KeyDeleteWord},
		{"ctrl-shift-W deletes word", gdk.KEY_W, gdk.ControlMask | gdk.ShiftMask, ModeResults, KeyDeleteWord},
		{"plain w types", gdk.KEY_w, 0, ModeResults, KeyPass},

		{"plain j types", gdk.KEY_j, 0, ModeResults, KeyPass},
		{"plain k types", gdk.KEY_k, 0, ModeResults, KeyPass},
		{"alt-j is not ours", gdk.KEY_j, gdk.AltMask, ModeResults, KeyPass},

		{"enter activates", gdk.KEY_Return, 0, ModeResults, KeyActivate},
		{"keypad enter activates", gdk.KEY_KP_Enter, 0, ModeResults, KeyActivate},
		{"ctrl-enter still activates", gdk.KEY_Return, gdk.ControlMask, ModeResults, KeyActivate},
		{"shift-enter takes the alt action", gdk.KEY_Return, gdk.ShiftMask, ModeResults, KeyActivateAlt},

		{"tab takes the alt action", gdk.KEY_Tab, 0, ModeResults, KeyActivateAlt},
		{"shift-tab takes the alt action", gdk.KEY_ISO_Left_Tab, gdk.ShiftMask, ModeResults, KeyActivateAlt},

		{"ordinary letters type", gdk.KEY_a, 0, ModeResults, KeyPass},
		{"space types", gdk.KEY_space, 0, ModeResults, KeyPass},
		{"backspace edits", gdk.KEY_BackSpace, 0, ModeResults, KeyPass},
		{"ctrl-a selects all in the entry", gdk.KEY_a, gdk.ControlMask, ModeResults, KeyPass},
		{"ctrl-v pastes", gdk.KEY_v, gdk.ControlMask, ModeResults, KeyPass},
		{"home edits", gdk.KEY_Home, 0, ModeResults, KeyPass},
		{"left edits", gdk.KEY_Left, 0, ModeResults, KeyPass},
		{"right edits", gdk.KEY_Right, 0, ModeResults, KeyPass},

		{"form: escape cancels", gdk.KEY_Escape, 0, ModeForm, KeyFormCancel},
		{"form: escape with modifiers cancels", gdk.KEY_Escape, gdk.ShiftMask, ModeForm, KeyFormCancel},
		{"form: enter submits", gdk.KEY_Return, 0, ModeForm, KeyFormSubmit},
		{"form: keypad enter submits", gdk.KEY_KP_Enter, 0, ModeForm, KeyFormSubmit},
		{"form: shift-enter still submits", gdk.KEY_Return, gdk.ShiftMask, ModeForm, KeyFormSubmit},
		{"form: tab passes for focus chain", gdk.KEY_Tab, 0, ModeForm, KeyPass},
		{"form: shift-tab passes for focus chain", gdk.KEY_ISO_Left_Tab, gdk.ShiftMask, ModeForm, KeyPass},
		{"form: down passes", gdk.KEY_Down, 0, ModeForm, KeyPass},
		{"form: up passes", gdk.KEY_Up, 0, ModeForm, KeyPass},
		{"form: ctrl-j passes", gdk.KEY_j, gdk.ControlMask, ModeForm, KeyPass},
		{"form: ctrl-w passes", gdk.KEY_w, gdk.ControlMask, ModeForm, KeyPass},
		{"form: letters type", gdk.KEY_a, 0, ModeForm, KeyPass},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := KeyFor(tt.keyval, tt.state, tt.mode); got != tt.want {
				t.Errorf("KeyFor(%d, %v, %v) = %v, want %v", tt.keyval, tt.state, tt.mode, got, tt.want)
			}
		})
	}
}

func TestKeyActionString(t *testing.T) {
	tests := map[KeyAction]string{
		KeyPass:        "pass",
		KeyHide:        "hide",
		KeyNext:        "next",
		KeyPrev:        "prev",
		KeyActivate:    "activate",
		KeyActivateAlt: "activate-alt",
		KeyFormCancel:  "form-cancel",
		KeyFormSubmit:  "form-submit",
	}
	for a, want := range tests {
		if got := a.String(); got != want {
			t.Errorf("KeyAction(%d).String() = %q, want %q", a, got, want)
		}
	}
}

// TestTypingIsNeverSwallowed guards the property that matters most: with no
// modifiers held, every printable ASCII key must fall through to the entry —
// in both modes.
func TestTypingIsNeverSwallowed(t *testing.T) {
	for _, mode := range []UIMode{ModeResults, ModeForm} {
		for r := ' '; r <= '~'; r++ {
			if got := KeyFor(uint(r), 0, mode); got != KeyPass {
				t.Errorf("KeyFor(%q, none, %v) = %v, want pass", r, mode, got)
			}
		}
	}
}
