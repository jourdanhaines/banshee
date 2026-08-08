// Package ui is banshee's GTK4 launcher front-end: the wofi/Raycast-style
// overlay window that the daemon shows and hides.
//
// The package is deliberately split in two. Files that touch GTK (launcher.go,
// row.go, keys.go) hold no business logic beyond widget plumbing; every
// decision that can be made without a display — debounce scheduling, selection
// index math, icon-strategy resolution, badge text — lives in plain Go files
// next to them and is unit tested. GTK code is validated by the manual
// checklist in the release plan.
//
// The UI depends on providers.Aggregator, never on a concrete aggregator, so a
// mock (or a future TUI front-end) can be swapped in at the same seam.
package ui

import (
	"context"
	"log"
	"strings"
	"time"

	"github.com/diamondburned/gotk4/pkg/gdk/v4"
	"github.com/diamondburned/gotk4/pkg/gio/v2"
	"github.com/diamondburned/gotk4/pkg/glib/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"

	"github.com/jourdanhaines/banshee/internal/config"
	"github.com/jourdanhaines/banshee/internal/launch"
	"github.com/jourdanhaines/banshee/internal/layershell"
	"github.com/jourdanhaines/banshee/internal/providers"
	"github.com/jourdanhaines/banshee/internal/theme"
)

const (
	// windowName is the CSS node name the stylesheet in internal/theme hangs
	// every rule off. Keep the two in sync.
	windowName = "banshee-window"

	// resultsHeight is the visible height of the result list; roughly eight
	// rows, with the rest reachable by scrolling.
	resultsHeight = 420

	// iconPixelSize is the row icon edge length in logical pixels.
	iconPixelSize = 24

	// fallbackTopMargin positions the window when monitor geometry is
	// unavailable (≈ a quarter of a 1080p screen).
	fallbackTopMargin = 270
)

// Launcher owns the launcher window and its query pipeline. It satisfies the
// UI interface the daemon defines (Show, Hide, Visible, Reload) structurally,
// so neither package needs to import the other's interface.
//
// Every method must be called from the GTK main thread. The daemon already
// funnels socket traffic through glib.IdleAdd, which is exactly that thread.
type Launcher struct {
	app  *gtk.Application
	cfg  config.Config
	agg  providers.Aggregator
	disp *launch.Dispatcher

	win     *gtk.ApplicationWindow
	panel   *gtk.Box
	stack   *gtk.Stack // "main" (query + results) / "form" pages
	mainBox *gtk.Box
	formBox *gtk.Box // persistent form page; contents rebuilt per openForm
	entry   *gtk.Entry
	scroll  *gtk.ScrolledWindow
	list    *gtk.ListBox

	// form is the open form view; nil means the results view is active and
	// is what mode() derives the keymap from.
	form *formView

	debounce *Debouncer
	sel      Selection
	results  []providers.Result

	// gen is bumped per query so a slow provider's late results are dropped
	// instead of overwriting a newer query's rows.
	gen    uint64
	cancel context.CancelFunc

	visible  bool
	layered  bool
	keyboard layershell.KeyboardMode

	// syncing suppresses the row-selected handler while the launcher drives
	// the selection itself, so programmatic moves do not feed back in.
	syncing bool

	// apps caches gio's application list for AppID icon lookups. Nil means
	// "not built yet"; Reload drops it so newly installed apps are picked up.
	apps map[string]*gio.AppInfo

	// builtins caches accent-tinted textures for compiled-in SVG icons. The
	// tint bakes the accent in, so Reload drops the cache alongside the theme.
	builtins map[string]*gdk.Texture

	// shiftClick mirrors the Shift modifier of the most recent list click, so
	// row-activated (which GTK fires without event state) can honor
	// shift-click as the alternate action the way Shift+Enter does.
	shiftClick bool

	// liveRows are the per-row progress bars of the live rows whose period is
	// *not* the standard one; standard rows are represented by sharedBar and
	// register nothing here. Rebuilt from scratch by every setResults, because
	// the bars they point at are destroyed with the old rows.
	liveRows []liveRow

	// sharedBar drains the standard 30-second TOTP window under the query
	// entry. It stands in for every standard-period live row at once — they all
	// rotate on the same unix%30 boundary — and is always allocated, shown and
	// hidden by opacity alone: SetVisible would change the panel's height on
	// every query, which is exactly what the min==max resultsHeight rule above
	// exists to prevent.
	sharedBar *gtk.ProgressBar

	// tickerOn reports whether a live tick is scheduled, and doubles as the
	// stop signal: a GLib timeout source that has already been queued still
	// fires after Hide, so tick self-stops by reading this instead of the
	// launcher removing the source underneath it.
	tickerOn bool

	// tickerGen distinguishes ticker sources across stop/start cycles. A tick
	// from a superseded generation returns false and dies without touching the
	// live one's state, which is what keeps a Hide-then-Show from ending up
	// with two sources updating the same labels.
	tickerGen uint64

	// requeryPending suppresses further boundary requeries until results land.
	// Without it a provider that takes longer than a tick to answer (a locked
	// keyring, say) would be cancelled and restarted once a second and never
	// finish. setResults clears it.
	requeryPending bool
}

// liveRow binds one rendered progress bar to the data the ticker needs to
// redraw it: the instant the row's content stops being valid and the length of
// the window ending there. Only non-standard-period rows get one — a standard
// row's remaining share is the same as every other standard row's, which is
// what sharedBar draws.
type liveRow struct {
	bar    *gtk.ProgressBar
	expiry time.Time
	period time.Duration
}

// liveTick is how often the drain bars are redrawn. Each tick recomputes the
// fraction from the wall clock, so the interval only governs smoothness, never
// accuracy: 4 Hz on a single widget the width of the panel looks continuous,
// costs nothing, and caps the latency between a code rotating and the boundary
// requery noticing at one tick.
const liveTick = 250 * time.Millisecond

// NewLauncher builds the launcher window for app and wires the query pipeline
// to agg and the activation path to disp. It must be called on the GTK main
// thread, from the application's activate handler, and before the window is
// realized — layer-shell configuration happens here.
//
// The window starts hidden; the daemon calls Show.
func NewLauncher(app *gtk.Application, cfg config.Config, agg providers.Aggregator, disp *launch.Dispatcher) *Launcher {
	l := &Launcher{
		app:      app,
		cfg:      cfg,
		agg:      agg,
		disp:     disp,
		sel:      NewSelection(),
		keyboard: KeyboardModeFor(cfg.KeyboardMode),
	}
	l.debounce = NewDebouncer(QueryDebounce, GLibTimer)

	l.build()
	return l
}

// formTransition is the slide duration between the results and form views.
const formTransition = 150 * time.Millisecond

// build assembles the widget tree:
//
//	ApplicationWindow #banshee-window
//	└─ Box .panel (vertical)
//	   └─ Stack (slide transitions)
//	      ├─ "main": Box .main-view
//	      │   ├─ Entry .query
//	      │   ├─ ProgressBar .code-timer   (shared TOTP drain, opacity-toggled)
//	      │   └─ ScrolledWindow .results-scroll
//	      │      └─ ListBox .results (SelectionModeBrowse)
//	      └─ "form": Box (persistent page, contents rebuilt per form)
func (l *Launcher) build() {
	l.applyTheme()

	l.win = gtk.NewApplicationWindow(l.app)
	l.win.SetName(windowName)
	l.win.SetTitle("banshee")
	l.win.SetDecorated(false)
	l.win.SetResizable(false)

	l.panel = gtk.NewBox(gtk.OrientationVertical, 0)
	l.panel.AddCSSClass("panel")
	l.panel.SetSizeRequest(l.launcherWidth(), -1)

	l.mainBox = gtk.NewBox(gtk.OrientationVertical, 0)
	l.mainBox.AddCSSClass("main-view")

	l.entry = gtk.NewEntry()
	l.entry.AddCSSClass("query")
	l.entry.SetPlaceholderText("Search…")
	l.entry.SetHExpand(true)
	l.mainBox.Append(l.entry)

	// Allocated once, for the lifetime of the window: see the sharedBar field
	// comment on why visibility is opacity and not SetVisible.
	l.sharedBar = gtk.NewProgressBar()
	l.sharedBar.AddCSSClass("code-timer")
	l.sharedBar.SetOpacity(0)
	l.mainBox.Append(l.sharedBar)

	l.list = gtk.NewListBox()
	l.list.AddCSSClass("results")
	l.list.SetSelectionMode(gtk.SelectionBrowse)
	l.list.SetActivateOnSingleClick(true)
	l.list.SetShowSeparators(false)

	placeholder := gtk.NewLabel("No results")
	placeholder.AddCSSClass("empty")
	l.list.SetPlaceholder(placeholder)

	l.scroll = gtk.NewScrolledWindow()
	l.scroll.AddCSSClass("results-scroll")
	// Never scroll horizontally: rows ellipsize instead, which is what keeps
	// the panel at its configured width.
	l.scroll.SetPolicy(gtk.PolicyNever, gtk.PolicyAutomatic)
	// Min == max: the panel keeps a constant height instead of growing and
	// shrinking with every keystroke, which on a centred layer surface reads
	// as the whole window jittering.
	l.scroll.SetMinContentHeight(resultsHeight)
	l.scroll.SetMaxContentHeight(resultsHeight)
	l.scroll.SetPropagateNaturalHeight(true)
	l.scroll.SetVExpand(true)
	l.scroll.SetChild(l.list)
	l.mainBox.Append(l.scroll)

	// The form page is a single persistent stack child whose contents are
	// replaced per openForm — adding or removing stack children mid-transition
	// kills the slide animation, so the page itself is never removed. The
	// stack stays vertically homogeneous (the default), so the form page
	// adopts the results page's fixed height and the panel never resizes —
	// the same no-jitter rationale as the min==max scroll height above.
	l.formBox = gtk.NewBox(gtk.OrientationVertical, 0)

	l.stack = gtk.NewStack()
	l.stack.SetTransitionDuration(uint(formTransition.Milliseconds()))
	l.stack.AddNamed(l.mainBox, "main")
	l.stack.AddNamed(l.formBox, "form")
	l.panel.Append(l.stack)

	l.win.SetChild(l.panel)

	l.setupLayerShell()
	l.connectSignals()
}

// setupLayerShell turns the window into a wlr-layer-shell overlay surface.
// Must run before the window is realized. When the compositor does not speak
// layer-shell (an X11 session, a debug run under a nested compositor) the
// window degrades to an ordinary floating window rather than failing.
func (l *Launcher) setupLayerShell() {
	if !layershell.Supported() {
		log.Printf("ui: compositor does not support wlr-layer-shell; falling back to a normal window")
		return
	}
	layershell.Setup(&l.win.Window, l.keyboard, TopMargin())
	l.layered = true
}

func (l *Launcher) connectSignals() {
	l.entry.ConnectChanged(func() {
		q := l.entry.Text()
		l.debounce.Trigger(func() { l.runQuery(q) })
	})

	l.list.ConnectRowSelected(func(row *gtk.ListBoxRow) {
		if l.syncing || row == nil {
			return
		}
		l.sel.Set(row.Index())
	})

	// row-activated carries no event state, so a click gesture (capture
	// phase, before the ListBox handles the press) records whether Shift was
	// held for the activation that follows.
	click := gtk.NewGestureClick()
	click.SetPropagationPhase(gtk.PhaseCapture)
	click.ConnectPressed(func(nPress int, x, y float64) {
		l.shiftClick = click.CurrentEventState()&gdk.ShiftMask != 0
	})
	l.list.AddController(click)

	l.list.ConnectRowActivated(func(row *gtk.ListBoxRow) {
		if row != nil {
			l.sel.Set(row.Index())
		}
		alt := l.shiftClick
		l.shiftClick = false
		l.Activate(alt)
	})

	l.win.AddController(l.newKeyController())
}

// Show makes the launcher visible, seeds the entry with query, re-asserts
// keyboard focus and immediately runs the query (bypassing the debounce, which
// exists for typing, not for showing).
//
// Showing an already-visible launcher is a legal no-op-plus-refocus: Hyprland
// occasionally drops keyboard focus on a layer surface, and re-asserting the
// keyboard mode on every show is the documented workaround.
func (l *Launcher) Show(query string) {
	if l.win == nil {
		return
	}

	// A form left open by a previous show is stale state: showing always
	// starts at the results view.
	l.resetToResults()

	// Setting the text fires ::changed and schedules a debounced query; the
	// explicit Fire below supersedes it so the first paint is immediate.
	l.entry.SetText(query)
	l.entry.SetPosition(-1)

	if l.layered {
		layershell.SetKeyboardMode(&l.win.Window, l.keyboard)
	}
	l.win.SetVisible(true)
	l.visible = true
	l.entry.GrabFocus()
	l.entry.SetPosition(-1)

	l.debounce.Fire(func() { l.runQuery(query) })
}

// Hide hides the window and abandons any in-flight query. The window is kept
// alive (SetVisible false, never destroyed) so the next toggle is instant and
// so the layer-shell configuration survives.
func (l *Launcher) Hide() {
	if l.win == nil || !l.visible {
		return
	}
	// Discard any open form first — a stale l.form would leave the keymap in
	// ModeForm and trap Esc on the next show.
	l.resetToResults()
	l.debounce.Cancel()
	l.cancelQuery()
	// A hidden launcher has nothing to animate; the pending timeout source
	// notices the flag on its next fire and stops itself.
	l.tickerOn = false
	l.requeryPending = false
	// Leave no half-drained bar behind for the next show to flash before its
	// first results land.
	if l.sharedBar != nil {
		l.sharedBar.SetOpacity(0)
	}
	l.win.SetVisible(false)
	l.visible = false
}

// Visible reports whether the launcher window is currently mapped.
func (l *Launcher) Visible() bool { return l.visible }

// Reload drops cached state that a configuration or environment change may
// have invalidated (application list, stylesheet) and refreshes the visible
// results. Call SetConfig first when banshee.conf itself changed.
func (l *Launcher) Reload() {
	l.apps = nil
	l.builtins = nil
	l.applyTheme()
	if l.visible {
		l.debounce.Fire(func() { l.runQuery(l.entry.Text()) })
	}
}

// SetConfig swaps in a freshly parsed configuration. Values that are baked
// into the widget tree (panel width, keyboard mode) are re-applied here;
// stylesheet and result refresh happen in Reload, so callers should do
// SetConfig then Reload.
func (l *Launcher) SetConfig(cfg config.Config) {
	l.cfg = cfg
	l.keyboard = KeyboardModeFor(cfg.KeyboardMode)
	if l.panel != nil {
		l.panel.SetSizeRequest(l.launcherWidth(), -1)
	}
	if l.layered && l.win != nil {
		layershell.SetKeyboardMode(&l.win.Window, l.keyboard)
	}
}

// Activate runs the selected row's action. alt selects Result.AltAction (Tab
// or Shift+Enter) and is ignored when the result has none.
//
// The window is hidden *before* dispatching: spawning a terminal or an app
// while an exclusive-keyboard overlay is still up leaves the new window
// without focus, and a failed dispatch should not leave the launcher covering
// the notification that reports it.
func (l *Launcher) Activate(alt bool) {
	if !l.sel.Valid() || l.sel.Index() >= len(l.results) {
		return
	}
	res := l.results[l.sel.Index()]

	// A form result's primary activation opens the in-launcher form instead
	// of dispatching; the window stays up. The alt action still dispatches
	// directly below, bypassing the form.
	if !alt && res.Form != nil {
		l.openForm(res)
		return
	}

	action := res.Action
	if alt {
		if res.AltAction == nil {
			return
		}
		action = *res.AltAction
	}

	l.Hide()

	if l.disp == nil {
		l.notify("Nothing is wired up to run " + res.Title)
		return
	}
	if err := l.disp.Dispatch(action); err != nil {
		l.notify("Could not run “" + res.Title + "”: " + err.Error())
	}
}

// mode reports which keymap is active: ModeForm while a form is open.
func (l *Launcher) mode() UIMode {
	if l.form != nil {
		return ModeForm
	}
	return ModeResults
}

// openForm rebuilds the form page for res.Form, slides it in and focuses the
// first field. The in-flight query and debounce are left alone: a late
// setResults just repaints the hidden results page, and the form holds its
// own copy of the result.
func (l *Launcher) openForm(res providers.Result) {
	if l.stack == nil || res.Form == nil {
		return
	}
	l.form = newFormView(res)

	for c := l.formBox.FirstChild(); c != nil; c = l.formBox.FirstChild() {
		l.formBox.Remove(c)
	}
	l.formBox.Append(l.form.root)

	l.stack.SetTransitionType(gtk.StackTransitionTypeSlideLeft)
	l.stack.SetVisibleChildName("form")
	// The page becomes visible synchronously, but the grab is deferred to
	// idle so it lands after the transition has mapped the child.
	v := l.form
	glib.IdleAdd(func() {
		if l.form == v {
			v.focusFirst()
		}
	})
}

// closeForm slides back to the results view exactly as the user left it.
// refocus re-grabs the query entry (Esc); it is false when the caller is
// about to hide or refocus itself.
func (l *Launcher) closeForm(refocus bool) {
	if l.form == nil || l.stack == nil {
		return
	}
	l.form = nil
	l.stack.SetTransitionType(gtk.StackTransitionTypeSlideRight)
	l.stack.SetVisibleChildName("main")
	if refocus && l.entry != nil {
		l.entry.GrabFocus()
		l.entry.SetPosition(-1)
	}
}

// submitFormOrPass handles Enter while a form is open: it submits, unless one
// of the form's dropdowns has its option list popped up, in which case it
// reports false so the key controller lets GTK have the press — that Enter
// commits the highlighted option, and stealing it as a submit would make a
// fixed-choice field unusable from the keyboard. A dropdown that merely holds
// focus does not withhold Enter; see formView.dropdownListOpen.
func (l *Launcher) submitFormOrPass() bool {
	if l.form != nil && l.form.dropdownListOpen() {
		return false
	}
	l.submitForm()
	return true
}

// submitForm validates the open form and dispatches the action it builds,
// following the same hide-before-dispatch order as Activate. Validation
// failure keeps the form up with the offending field marked.
func (l *Launcher) submitForm() {
	if l.form == nil {
		return
	}
	values := TrimValues(l.form.values())
	if i, ok := FirstMissingRequired(l.form.form.Fields, values); !ok {
		l.form.markError(i)
		return
	}
	res, form := l.form.res, l.form.form

	l.Hide() // also resets the stack to the results view

	if form.Build == nil {
		return
	}
	action, err := form.Build(values)
	if err != nil {
		l.notify("Could not run “" + res.Title + "”: " + err.Error())
		return
	}
	if l.disp == nil {
		l.notify("Nothing is wired up to run " + res.Title)
		return
	}
	if err := l.disp.Dispatch(action); err != nil {
		l.notify("Could not run “" + res.Title + "”: " + err.Error())
	}
}

// resetToResults is the no-animation teardown used by Hide and Show: whatever
// view is up, the next paint is the results page.
func (l *Launcher) resetToResults() {
	if l.form == nil || l.stack == nil {
		return
	}
	l.form = nil
	l.stack.SetTransitionType(gtk.StackTransitionTypeNone)
	l.stack.SetVisibleChildName("main")
}

// deleteWordBeforeCursor implements Ctrl-W in the query entry: delete from
// the cursor back over one whitespace-delimited word. DeleteText fires
// ::changed, so the debounced requery happens exactly as if the user had
// backspaced.
func (l *Launcher) deleteWordBeforeCursor() {
	if l.entry == nil {
		return
	}
	pos := l.entry.Position()
	if start := DeleteWordStart(l.entry.Text(), pos); start < pos {
		l.entry.DeleteText(start, pos)
	}
}

// runQuery starts a new query generation: the previous one is cancelled, the
// aggregator runs off the main thread, and the results are marshalled back
// through glib.IdleAdd. Late results from a superseded generation are dropped.
func (l *Launcher) runQuery(q string) {
	l.cancelQuery()

	l.gen++
	gen := l.gen

	ctx, cancel := context.WithCancel(context.Background())
	l.cancel = cancel

	agg := l.agg
	go func() {
		defer cancel()
		var res []providers.Result
		if agg != nil {
			res = agg.Query(ctx, q)
		}
		if ctx.Err() != nil {
			return
		}
		glib.IdleAdd(func() {
			// gen is only ever written on the main thread, which is where
			// this closure runs, so the comparison needs no lock.
			if gen != l.gen {
				return
			}
			l.setResults(res)
		})
	}()
}

func (l *Launcher) cancelQuery() {
	if l.cancel != nil {
		l.cancel()
		l.cancel = nil
	}
}

// setResults stores and renders a query generation's results, then selects
// the top hit. max_results is an opt-in cap; the default (0) shows everything
// the aggregator returned.
func (l *Launcher) setResults(res []providers.Result) {
	if max := l.cfg.MaxResults; max > 0 && len(res) > max {
		res = res[:max]
	}
	l.results = res
	l.requeryPending = false

	// The previous generation's bars are about to be destroyed with their
	// rows; newRow re-registers the live ones as it rebuilds.
	l.liveRows = nil

	l.syncing = true
	l.list.RemoveAll()
	for i := range res {
		l.list.Append(l.newRow(res[i]))
	}
	l.syncing = false

	// Seed the shared bar here as well as in tick, so a query that lands
	// mid-window paints at the right level instead of jumping on the first tick.
	if l.sharedBar != nil {
		if AnyStandardLive(res) {
			l.sharedBar.SetFraction(StandardFraction(time.Now()))
			l.sharedBar.SetOpacity(1)
		} else {
			l.sharedBar.SetOpacity(0)
		}
	}

	if AnyLive(res) {
		l.startTicker()
	} else {
		l.tickerOn = false
	}

	l.sel.Reset(len(res))
	l.applySelection()
}

// startTicker schedules the live-row refresh, unless one is already running.
// Idempotent: setResults calls it on every generation that contains a live row,
// and the ticker must not multiply.
func (l *Launcher) startTicker() {
	if l.tickerOn {
		return
	}
	l.tickerOn = true
	l.tickerGen++
	gen := l.tickerGen
	glib.TimeoutAdd(uint(liveTick.Milliseconds()), func() bool { return l.tick(gen) })
}

// tick drains the visible progress bars and, when a row's content has actually
// expired, re-runs the current query once so the provider can hand back fresh
// content. It runs on the GTK main loop (GLib timeout) and returns false to
// unschedule itself.
//
// The split is deliberate. Re-running the whole query on every tick to move a
// bar would rescan /proc and poke every exec plugin a hundred-plus times per
// TOTP rotation; redrawing bars alone cannot work either, because the code
// behind the bar is only obtainable from the provider, which holds the secret.
// So: cheap fraction updates on every tick, one real query at the expiry
// boundary.
//
// Time comes from time.Now on every tick rather than a decrementing counter,
// so a suspended machine or a delayed source shows the true remaining time
// instead of accumulated drift.
func (l *Launcher) tick(gen uint64) bool {
	// A superseded source must die quietly: startTicker has already handed
	// ownership of tickerOn to a newer generation.
	if gen != l.tickerGen {
		return false
	}
	if !l.tickerOn || !l.visible {
		l.tickerOn = false
		return false
	}

	now := time.Now()
	if l.sharedBar != nil && AnyStandardLive(l.results) {
		l.sharedBar.SetFraction(StandardFraction(now))
	}
	for _, lr := range l.liveRows {
		if lr.bar == nil {
			continue
		}
		lr.bar.SetFraction(Fraction(lr.expiry, lr.period, now))
	}

	if !l.requeryPending && AnyExpired(l.results, now) && l.entry != nil {
		l.requeryPending = true
		l.runQuery(l.entry.Text())
	}
	return true
}

// MoveSelection shifts the highlighted row by delta and keeps it on screen.
// Exported so the daemon (or a test harness) can drive navigation the same way
// the key controller does.
func (l *Launcher) MoveSelection(delta int) {
	if l.sel.Move(delta) {
		l.applySelection()
	}
}

// applySelection pushes the Selection state into the ListBox. The entry keeps
// keyboard focus throughout, so GTK's own list navigation never runs and this
// is the only thing that moves the highlight.
func (l *Launcher) applySelection() {
	if l.list == nil {
		return
	}
	i := l.sel.Index()
	if i < 0 {
		l.syncing = true
		l.list.UnselectAll()
		l.syncing = false
		return
	}
	row := l.list.RowAtIndex(i)
	if row == nil {
		return
	}
	l.syncing = true
	l.list.SelectRow(row)
	l.syncing = false
	l.scrollToRow(row)
}

// scrollToRow keeps the selected row inside the viewport. Rows appended in
// this same main-loop iteration have no allocation yet, so the clamp is
// deferred to idle when the row has not been sized.
func (l *Launcher) scrollToRow(row *gtk.ListBoxRow) {
	if l.scroll == nil || row == nil {
		return
	}
	clamp := func() {
		adj := l.scroll.VAdjustment()
		if adj == nil {
			return
		}
		a := row.Allocation()
		if a == nil {
			return
		}
		adj.ClampPage(float64(a.Y()), float64(a.Y()+a.Height()))
	}
	if a := row.Allocation(); a != nil && a.Height() > 0 {
		clamp()
		return
	}
	glib.IdleAdd(clamp)
}

// Notify surfaces msg the same way a failed activation does. It exists for
// action handlers that finish *after* Dispatch returned — a TOTP copy that
// waits on a secret store, say — and therefore have no other way to tell the
// user they failed; boot hands one of these to such handlers as a callback.
//
// Like every other Launcher method it must be called on the GTK main thread,
// so a handler running off it wraps the call in glib.IdleAdd. Safe on a nil
// receiver, so boot can build the callback before the window exists.
func (l *Launcher) Notify(msg string) {
	if l == nil {
		return
	}
	l.notify(msg)
}

// notify logs an error and surfaces it as a desktop notification, because by
// the time an action fails the launcher has already hidden itself.
func (l *Launcher) notify(msg string) {
	log.Printf("ui: %s", msg)
	if l.app == nil {
		return
	}
	n := gio.NewNotification("banshee")
	n.SetBody(msg)
	n.SetPriority(gio.NotificationPriorityNormal)
	l.app.SendNotification("banshee-error", n)
}

// applyTheme (re-)installs the stylesheet for the current config on the
// default display. Called once at construction and again on every Reload, so
// an edited accent/opacity/width takes effect without restarting the daemon.
func (l *Launcher) applyTheme() {
	d := gdk.DisplayGetDefault()
	if d == nil {
		log.Printf("ui: no default display; launcher stylesheet not installed")
		return
	}
	if err := theme.Load(d, l.cfg); err != nil {
		log.Printf("ui: theme load failed: %v", err)
	}
}

func (l *Launcher) launcherWidth() int {
	if l.cfg.LauncherWidth > 0 {
		return l.cfg.LauncherWidth
	}
	return config.Default().LauncherWidth
}

// KeyboardModeFor maps the keyboard_mode config value onto a layer-shell mode.
// Anything other than "on-demand" (the Hyprland focus-quirk escape hatch)
// means exclusive, including an empty or misspelled value.
func KeyboardModeFor(s string) layershell.KeyboardMode {
	if strings.EqualFold(strings.TrimSpace(s), "on-demand") {
		return layershell.KeyboardOnDemand
	}
	return layershell.KeyboardExclusive
}

// TopMargin returns the launcher's offset from the top of the screen: a
// quarter of the primary monitor's height, so the panel sits in the upper
// third the way Raycast and wofi do. Falls back to fallbackTopMargin when no
// display or monitor geometry is available (headless, or called before GTK is
// initialized).
func TopMargin() int {
	d := gdk.DisplayGetDefault()
	if d == nil {
		return fallbackTopMargin
	}
	mons := d.Monitors()
	if mons == nil || mons.NItems() == 0 {
		return fallbackTopMargin
	}
	obj := mons.Item(0)
	if obj == nil {
		return fallbackTopMargin
	}
	mon, ok := obj.Cast().(*gdk.Monitor)
	if !ok {
		return fallbackTopMargin
	}
	geom := mon.Geometry()
	if geom == nil || geom.Height() <= 0 {
		return fallbackTopMargin
	}
	return geom.Height() / 4
}

// GLibTimer is the production Timer: it schedules through the GLib main loop,
// so debounced callbacks run on the GTK thread and are safe to touch widgets.
func GLibTimer(d time.Duration, fn func()) (cancel func()) {
	ms := uint(d.Milliseconds())
	if ms == 0 {
		ms = 1
	}
	var fired bool
	h := glib.TimeoutAdd(ms, func() bool {
		fired = true
		fn()
		return false // one-shot
	})
	return func() {
		if !fired {
			fired = true
			glib.SourceRemove(h)
		}
	}
}
