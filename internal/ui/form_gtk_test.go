//go:build gtksmoke

// Allocation check for generated form fields against a real GTK main loop and
// the real theme. It needs a Wayland/X display, so it runs under `make smoke`:
//
//	go test -tags gtksmoke -run Smoke ./internal/ui
package ui

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/diamondburned/gotk4/pkg/gdk/v4"
	"github.com/diamondburned/gotk4/pkg/gio/v2"
	"github.com/diamondburned/gotk4/pkg/glib/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"

	"github.com/jourdanhaines/banshee/internal/config"
	"github.com/jourdanhaines/banshee/internal/launch"
	"github.com/jourdanhaines/banshee/internal/providers"
)

type noResults struct{}

func (noResults) Query(context.Context, string) []providers.Result { return nil }

// pump runs the default main context for d so layout and frames settle.
func pump(d time.Duration) {
	ctx := glib.MainContextDefault()
	for end := time.Now().Add(d); time.Now().Before(end); {
		for ctx.Pending() {
			ctx.Iteration(false)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// passgenLikeForm mirrors the passgen plugin's text-password layout: a
// dropdown, a length slider, checkboxes and an unlabelled generated field.
func passgenLikeForm() providers.Result {
	text := []providers.Condition{{Key: "generator", Values: []string{"Text password"}}}
	return providers.Result{Form: &providers.Form{Title: "Generate", SubmitLabel: "Generate", Fields: []providers.FormField{
		{Key: "generator", Label: "Generator", Options: []string{"Text password", "UUIDv4"}},
		{Key: "length", Label: "Length", Slider: &providers.SliderSpec{Min: 8, Max: 256, Step: 1, Default: 24}, ShowWhen: text},
		{Key: "symbols", Label: "Symbols", Checkbox: true, Checked: true, ShowWhen: text},
		{Key: "ambiguous", Label: "Exclude ambiguous (0O1lI|)", Checkbox: true, ShowWhen: text},
		{Key: "gen", Generate: &providers.GenerateSpec{
			Charset:    "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789!@#$%^&*()-_=+[]{};:,.<>?/",
			LengthFrom: "length",
		}, ShowWhen: text},
	}}}
}

func TestGeneratedFieldSmoke(t *testing.T) {
	// GTK wants its main loop pinned to one OS thread.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	app := gtk.NewApplication("dev.jourdan.banshee.formsmoke", gio.ApplicationNonUnique)
	app.ConnectActivate(func() {
		defer app.Quit()
		l := NewLauncher(app, config.Default(), noResults{}, launch.NewDispatcher())
		l.Show("")
		pump(200 * time.Millisecond)
		winHeight := l.win.AllocatedHeight()

		l.openForm(passgenLikeForm())
		pump(400 * time.Millisecond)
		g := l.form.generated[0]
		slider := l.form.inputs[1].(*sliderInput).s

		for _, n := range []int{8, 24, 128, 256} {
			for _, revealed := range []bool{false, true} {
				// No t.Run: a subtest runs on another goroutine, off the
				// thread that owns the GTK main context.
				name := fmt.Sprintf("len=%d revealed=%v", n, revealed)
				slider.SetValue(float64(n))
				g.eye.SetActive(revealed)
				pump(100 * time.Millisecond)

				w, h := g.label.AllocatedWidth(), g.label.AllocatedHeight()
				minH, _, _, _ := g.label.Measure(gtk.OrientationVertical, w)
				t.Logf("%s: label %dx%d, min height %d", name, w, h, minH)
				if h < minH {
					t.Errorf("%s: label allocated %dx%d, needs height %d at that width", name, w, h, minH)
				}
				if got := l.win.AllocatedHeight(); got != winHeight {
					t.Errorf("%s: window height = %d, want constant %d", name, got, winHeight)
				}
			}
		}
	})
	app.Run(nil)
}

// totpEditLikeForm mirrors the TOTP edit form: a prefilled name entry and a
// prefilled multi-line notes field. An empty submitLabel keeps Enter-submits.
func totpEditLikeForm(submitLabel string, got *map[string]string) providers.Result {
	return providers.Result{Title: "Edit", Form: &providers.Form{Title: "Edit GitHub", SubmitLabel: submitLabel,
		Fields: []providers.FormField{
			{Key: "name", Label: "Name", Value: "GitHub", Required: true},
			{Key: "notes", Label: "Notes", Value: "code-1\ncode-2", Multiline: true},
		},
		Build: func(v map[string]string) (providers.Action, error) {
			*got = v
			return providers.Action{Kind: "smoke-submit"}, nil
		},
	}}
}

// totpDeleteLikeForm mirrors the TOTP delete confirmation: no fields, only
// the submit and Back buttons.
func totpDeleteLikeForm(got *map[string]string) providers.Result {
	return providers.Result{Title: "Delete", Form: &providers.Form{Title: "Delete GitHub?", SubmitLabel: "Delete",
		Build: func(v map[string]string) (providers.Action, error) {
			*got = v
			return providers.Action{Kind: "smoke-submit"}, nil
		},
	}}
}

func TestMultilineFormSmoke(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	app := gtk.NewApplication("dev.jourdan.banshee.multilinesmoke", gio.ApplicationNonUnique)
	app.ConnectActivate(func() {
		defer app.Quit()
		disp := launch.NewDispatcher()
		disp.Register("smoke-submit", func(providers.Action) error { return nil })
		l := NewLauncher(app, config.Default(), noResults{}, disp)
		l.Show("")
		pump(200 * time.Millisecond)
		winHeight := l.win.AllocatedHeight()
		checkHeight := func(step string) {
			if got := l.win.AllocatedHeight(); got != winHeight {
				t.Errorf("%s: window height = %d, want constant %d", step, got, winHeight)
			}
		}

		// Prefill, and a long note scrolling inside its fixed viewport.
		var got map[string]string
		l.openForm(totpEditLikeForm("Save", &got))
		pump(400 * time.Millisecond)
		vals := l.form.values()
		if vals["name"] != "GitHub" || vals["notes"] != "code-1\ncode-2" {
			t.Errorf("prefilled values = %q", vals)
		}
		checkHeight("edit form open")
		notes := l.form.inputs[1].(*multilineInput)
		notes.tv.Buffer().SetText(strings.Repeat("backup-code\n", 40))
		pump(150 * time.Millisecond)
		checkHeight("long notes")
		if h := notes.box.AllocatedHeight(); h > multilineHeight+40 {
			t.Errorf("notes box grew to %d, want about %d", h, multilineHeight)
		}

		// Ctrl+Enter submits from the focused text view.
		notes.grabFocus()
		pump(50 * time.Millisecond)
		if !l.form.textViewFocused() {
			t.Fatal("text view not focused after grabFocus")
		}
		if !l.handleKey(gdk.KEY_Return, gdk.ControlMask) || l.form != nil || got == nil {
			t.Errorf("ctrl+enter did not submit (form open: %v, values: %q)", l.form != nil, got)
		}

		// Plain Enter on a focused text view passes through even when
		// Enter would otherwise submit.
		got = nil
		l.Show("")
		l.openForm(totpEditLikeForm("", &got))
		pump(300 * time.Millisecond)
		l.form.inputs[1].grabFocus()
		pump(50 * time.Millisecond)
		if l.handleKey(gdk.KEY_Return, 0) || l.form == nil || got != nil {
			t.Error("enter on a focused text view was not passed through")
		}
		l.form.inputs[0].grabFocus()
		pump(50 * time.Millisecond)
		if !l.handleKey(gdk.KEY_Return, 0) || got == nil {
			t.Error("enter on the name entry did not submit")
		}

		// A zero-field confirm form focuses its submit button.
		got = nil
		l.Show("")
		l.openForm(totpDeleteLikeForm(&got))
		pump(300 * time.Millisecond)
		checkHeight("delete form open")
		if !l.form.submit.IsFocus() {
			t.Error("zero-field form did not focus its submit button")
		}

		// A form opened by a still-held Enter swallows it until release.
		l.Show("")
		l.enterHeld = true
		l.openForm(totpDeleteLikeForm(&got))
		pump(300 * time.Millisecond)
		if !l.handleKey(gdk.KEY_Return, 0) || !l.handleKey(gdk.KEY_Return, gdk.ControlMask) || l.form == nil || got != nil {
			t.Error("unarmed enter was not swallowed")
		}
		l.handleKeyRelease(gdk.KEY_Return)
		if !l.handleKey(gdk.KEY_Return, gdk.ControlMask) || got == nil {
			t.Error("enter after release did not submit")
		}
	})
	app.Run(nil)
}
