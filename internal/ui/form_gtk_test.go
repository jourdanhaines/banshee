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
	"testing"
	"time"

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
