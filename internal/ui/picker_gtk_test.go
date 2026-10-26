//go:build gtksmoke

// End-to-end picker page check against a real GTK main loop and the real
// theme: open/filter timings, the constant panel size, keyboard navigation,
// the variant popover and the copy path. Runs under `make smoke`.
package ui

import (
	"fmt"
	"runtime"
	"testing"
	"time"

	"github.com/diamondburned/gotk4/pkg/gdk/v4"
	"github.com/diamondburned/gotk4/pkg/gio/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"

	"github.com/jourdanhaines/banshee/internal/config"
	"github.com/jourdanhaines/banshee/internal/fuzzy"
	"github.com/jourdanhaines/banshee/internal/launch"
	"github.com/jourdanhaines/banshee/internal/providers"
)

// pickerLike is an emoji-picker-sized result: 1,900 items, item 5 with six
// variants.
func pickerLike() providers.Result {
	items := make([]providers.PickerItem, 1900)
	for i := range items {
		items[i] = providers.PickerItem{
			Value:    string(rune(0x1F300 + i)),
			Label:    fmt.Sprintf("item %04d", i),
			Keywords: []string{fmt.Sprintf("kw%d", i%7)},
		}
	}
	for k := 0; k < 6; k++ {
		items[5].Variants = append(items[5].Variants, providers.PickerItem{
			Value: string(rune(0x1F300+5)) + string(rune(0x1F3FB+k%5)),
			Label: fmt.Sprintf("item 0005 tone %d", k),
		})
	}
	return providers.Result{Title: "Emoji picker", Picker: &providers.Picker{Title: "Emoji", Items: items}}
}

func TestPickerSmoke(t *testing.T) {
	// GTK wants its main loop pinned to one OS thread.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	app := gtk.NewApplication("dev.jourdan.banshee.pickersmoke", gio.ApplicationNonUnique)
	app.ConnectActivate(func() {
		defer app.Quit()

		var copied []string
		d := launch.NewDispatcher()
		d.Register(providers.ActClipboardCopy, func(a providers.Action) error {
			copied = append(copied, a.Text)
			return nil
		})
		l := NewLauncher(app, config.Default(), noResults{}, d)
		res := pickerLike()
		v := l.pickerPage
		lastCopied := func() string {
			if len(copied) == 0 {
				return ""
			}
			return copied[len(copied)-1]
		}
		checkSize := func(step string, h0, w0 int) {
			if h := l.win.AllocatedHeight(); h != h0 {
				t.Errorf("%s: window height = %d, want constant %d", step, h, h0)
			}
			if w := l.win.AllocatedWidth(); w != w0 {
				t.Errorf("%s: window width = %d, want constant %d", step, w, w0)
			}
		}

		// (1) Baseline size from the results page.
		l.Show("")
		pump(200 * time.Millisecond)
		h0, w0 := l.win.AllocatedHeight(), l.win.AllocatedWidth()
		t.Logf("window %dx%d", w0, h0)

		// (2) Open: every item rendered, panel unchanged.
		t0 := time.Now()
		l.openPicker(res)
		t.Logf("open (sync): %v", time.Since(t0))
		pump(400 * time.Millisecond)
		if l.mode() != ModePicker {
			t.Errorf("open: mode = %v, want ModePicker", l.mode())
		}
		if n := len(v.state.Visible); n != 1900 {
			t.Errorf("open: %d visible, want 1900", n)
		}
		if n := len(v.cells); n != 1900 {
			t.Errorf("open: %d cells, want 1900", n)
		}
		checkSize("open", h0, w0)
		// Items U+1F850/U+1F852 are 75 px arrows: this guards that one wide
		// glyph never widens the homogeneous grid, and with it the panel.
		if gw := v.grid.AllocatedWidth(); gw > w0 {
			t.Errorf("open: grid width %d exceeds window width %d", gw, w0)
		}

		// (3) Filter: hidden cells trail the visible ones.
		t0 = time.Now()
		v.entry.SetText("item 001")
		t.Logf("filter (sync): %v", time.Since(t0))
		pump(200 * time.Millisecond)
		// fuzzy.Score is a subsequence matcher, so "001" also hits e.g. 0101.
		want := len(FilterPicker(res.Picker.Items, "item 001", fuzzy.Score))
		t.Logf("filter: %d visible", want)
		if n := len(v.state.Visible); n != want || want == 0 || want >= 1900 {
			t.Errorf("filter: %d visible, want %d (0 < want < 1900)", n, want)
		}
		if want < len(v.cells) && v.cells[want].Visible() {
			t.Errorf("filter: cell %d visible, want hidden", want)
		}
		if want > 0 && !v.cells[want-1].Visible() {
			t.Errorf("filter: cell %d hidden, want visible", want-1)
		}
		checkSize("filter", h0, w0)

		// (4) Left/Right from the entry pass through; Down enters the grid.
		if l.handleKey(gdk.KEY_Left, 0) {
			t.Errorf("nav: Left in entry was consumed, want pass-through")
		}
		l.handleKey(gdk.KEY_Down, 0)
		if v.state.Highlight != 0 {
			t.Errorf("nav: Down highlight = %d, want 0", v.state.Highlight)
		}
		l.handleKey(gdk.KEY_Right, 0)
		if v.state.Highlight != 1 {
			t.Errorf("nav: Right highlight = %d, want 1", v.state.Highlight)
		}
		if sel := v.grid.SelectedChildren(); len(sel) != 1 || sel[0].Index() != 1 {
			t.Errorf("nav: grid selection = %v, want child 1", sel)
		}

		// (5) Enter on an item with variants opens the popover.
		v.entry.SetText("")
		pump(100 * time.Millisecond)
		l.handleKey(gdk.KEY_Down, 0)
		for i := 0; i < 5; i++ {
			l.handleKey(gdk.KEY_Right, 0)
		}
		if v.state.Highlight != 5 {
			t.Errorf("variants: highlight = %d, want 5", v.state.Highlight)
		}
		l.handleKey(gdk.KEY_Return, 0)
		pump(200 * time.Millisecond)
		if !v.variantsOpen() || !v.pop.Visible() {
			t.Errorf("variants: open=%v popover visible=%v, want both", v.variantsOpen(), v.pop.Visible())
		}
		if v.popState.Highlight != 0 {
			t.Errorf("variants: popover highlight = %d, want 0", v.popState.Highlight)
		}
		checkSize("variants", h0, w0)

		// (6) Arrows move inside the popover; Esc closes only the popover.
		l.handleKey(gdk.KEY_Right, 0)
		if v.popState.Highlight != 1 {
			t.Errorf("variants: popover Right highlight = %d, want 1", v.popState.Highlight)
		}
		l.handleKey(gdk.KEY_Escape, 0)
		pump(200 * time.Millisecond)
		if v.variantsOpen() || v.pop.Visible() {
			t.Errorf("variants: popover still open after Esc")
		}
		if l.mode() != ModePicker {
			t.Errorf("variants: mode after Esc = %v, want ModePicker", l.mode())
		}

		// (7) Enter Enter copies the default tone (the item itself) and hides.
		l.handleKey(gdk.KEY_Return, 0)
		pump(100 * time.Millisecond)
		l.handleKey(gdk.KEY_Return, 0)
		pump(100 * time.Millisecond)
		if got, want := lastCopied(), res.Picker.Items[5].Value; got != want {
			t.Errorf("copy variant: copied %q, want %q", got, want)
		}
		if l.Visible() {
			t.Errorf("copy variant: launcher still visible")
		}
		if l.mode() != ModeResults {
			t.Errorf("copy variant: mode = %v, want ModeResults", l.mode())
		}

		// (8) Enter with no highlight copies the first visible match.
		l.Show("")
		l.openPicker(res)
		pump(200 * time.Millisecond)
		v.entry.SetText("item 0003")
		pump(100 * time.Millisecond)
		l.handleKey(gdk.KEY_Return, 0)
		pump(100 * time.Millisecond)
		if got := lastCopied(); got != "\U0001F303" {
			t.Errorf("copy first: copied %q, want %q", got, "\U0001F303")
		}
		if l.Visible() {
			t.Errorf("copy first: launcher still visible")
		}

		// (9) Esc returns to the results page.
		l.Show("")
		l.openPicker(res)
		pump(200 * time.Millisecond)
		l.handleKey(gdk.KEY_Escape, 0)
		pump(300 * time.Millisecond)
		if l.mode() != ModeResults {
			t.Errorf("esc: mode = %v, want ModeResults", l.mode())
		}
		if name := l.stack.VisibleChildName(); name != "main" {
			t.Errorf("esc: stack page = %q, want main", name)
		}

		// (10) Reload closes the picker and drops the pool; (11) reopen rebuilds.
		l.openPicker(res)
		pump(200 * time.Millisecond)
		l.Reload()
		if l.mode() != ModeResults {
			t.Errorf("reload: mode = %v, want ModeResults", l.mode())
		}
		if n := len(v.cells); n != 0 {
			t.Errorf("reload: %d cells, want 0", n)
		}
		t0 = time.Now()
		l.openPicker(res)
		t.Logf("reopen after reload (sync): %v", time.Since(t0))
		pump(200 * time.Millisecond)
		if n := len(v.cells); n != 1900 {
			t.Errorf("reopen: %d cells, want 1900", n)
		}
	})
	app.Run(nil)
}
