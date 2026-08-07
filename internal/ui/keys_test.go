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
		want   KeyAction
	}{
		{"escape hides", gdk.KEY_Escape, 0, KeyHide},
		{"escape with modifiers still hides", gdk.KEY_Escape, gdk.ControlMask | gdk.ShiftMask, KeyHide},

		{"down moves next", gdk.KEY_Down, 0, KeyNext},
		{"up moves prev", gdk.KEY_Up, 0, KeyPrev},

		{"ctrl-j moves next", gdk.KEY_j, gdk.ControlMask, KeyNext},
		{"ctrl-k moves prev", gdk.KEY_k, gdk.ControlMask, KeyPrev},
		{"ctrl-shift-J moves next", gdk.KEY_J, gdk.ControlMask | gdk.ShiftMask, KeyNext},
		{"ctrl-shift-K moves prev", gdk.KEY_K, gdk.ControlMask | gdk.ShiftMask, KeyPrev},
		{"ctrl-n moves next", gdk.KEY_n, gdk.ControlMask, KeyNext},
		{"ctrl-p moves prev", gdk.KEY_p, gdk.ControlMask, KeyPrev},

		{"plain j types", gdk.KEY_j, 0, KeyPass},
		{"plain k types", gdk.KEY_k, 0, KeyPass},
		{"alt-j is not ours", gdk.KEY_j, gdk.AltMask, KeyPass},

		{"enter activates", gdk.KEY_Return, 0, KeyActivate},
		{"keypad enter activates", gdk.KEY_KP_Enter, 0, KeyActivate},
		{"ctrl-enter still activates", gdk.KEY_Return, gdk.ControlMask, KeyActivate},
		{"shift-enter takes the alt action", gdk.KEY_Return, gdk.ShiftMask, KeyActivateAlt},

		{"tab takes the alt action", gdk.KEY_Tab, 0, KeyActivateAlt},
		{"shift-tab takes the alt action", gdk.KEY_ISO_Left_Tab, gdk.ShiftMask, KeyActivateAlt},

		{"ordinary letters type", gdk.KEY_a, 0, KeyPass},
		{"space types", gdk.KEY_space, 0, KeyPass},
		{"backspace edits", gdk.KEY_BackSpace, 0, KeyPass},
		{"ctrl-a selects all in the entry", gdk.KEY_a, gdk.ControlMask, KeyPass},
		{"ctrl-v pastes", gdk.KEY_v, gdk.ControlMask, KeyPass},
		{"home edits", gdk.KEY_Home, 0, KeyPass},
		{"left edits", gdk.KEY_Left, 0, KeyPass},
		{"right edits", gdk.KEY_Right, 0, KeyPass},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := KeyFor(tt.keyval, tt.state); got != tt.want {
				t.Errorf("KeyFor(%d, %v) = %v, want %v", tt.keyval, tt.state, got, tt.want)
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
	}
	for a, want := range tests {
		if got := a.String(); got != want {
			t.Errorf("KeyAction(%d).String() = %q, want %q", a, got, want)
		}
	}
}

// TestTypingIsNeverSwallowed guards the property that matters most: with no
// modifiers held, every printable ASCII key must fall through to the entry.
func TestTypingIsNeverSwallowed(t *testing.T) {
	for r := ' '; r <= '~'; r++ {
		if got := KeyFor(uint(r), 0); got != KeyPass {
			t.Errorf("KeyFor(%q, none) = %v, want pass", r, got)
		}
	}
}
