package ui

import (
	"strings"

	"github.com/diamondburned/gotk4/pkg/gdk/v4"
	"github.com/diamondburned/gotk4/pkg/glib/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"

	"github.com/jourdanhaines/banshee/internal/fuzzy"
	"github.com/jourdanhaines/banshee/internal/providers"
)

// Sentinels for pickerView.shown.
const (
	cellHidden  = -1
	cellUnknown = -2 // forces the next render to rewrite the cell
)

// pickerView is the persistent picker stack page: a back button and search
// entry over a FlowBox grid, plus a popover for an item's variants. Its cell
// pool outlives a single open so reopening a 1,900-item picker reuses widgets
// instead of rebuilding them; Launcher.Reload drops it.
//
// Invariant: cells are only ever hidden from the tail, so child.Index() == j
// always maps straight to state.Visible[j].
type pickerView struct {
	res   providers.Result
	score fuzzy.Scorer

	root   *gtk.Box
	back   *gtk.Button
	entry  *gtk.Entry
	scroll *gtk.ScrolledWindow
	grid   *gtk.FlowBox

	cells  []*gtk.FlowBoxChild
	labels []*gtk.Label
	shown  []int // item index rendered in cell j, or cellHidden/cellUnknown
	state  PickerState

	pop      *gtk.Popover
	popGrid  *gtk.FlowBox
	popCells []*gtk.FlowBoxChild
	popState PickerState
	popOpen  bool

	onCell    func(j int)
	onVariant func(providers.PickerItem)
	onClosed  func()
}

// newPickerView builds the page's widget tree with an empty cell pool.
func newPickerView(score fuzzy.Scorer) *pickerView {
	v := &pickerView{score: score}

	v.root = gtk.NewBox(gtk.OrientationVertical, 0)
	v.root.AddCSSClass("picker-view")

	header := gtk.NewBox(gtk.OrientationHorizontal, 0)
	v.back = gtk.NewButtonFromIconName("go-previous-symbolic")
	v.back.AddCSSClass("picker-back")
	v.back.SetTooltipText("Back (Esc)")
	v.back.SetCanFocus(false)
	v.back.SetFocusOnClick(false)
	header.Append(v.back)

	v.entry = gtk.NewEntry()
	v.entry.AddCSSClass("picker-query")
	v.entry.SetHExpand(true)
	header.Append(v.entry)
	v.root.Append(header)

	v.grid = newPickerGrid()

	// Not propagating natural height keeps the page's size request small, so
	// the homogeneous stack still takes its height from the results page.
	v.scroll = gtk.NewScrolledWindow()
	v.scroll.AddCSSClass("picker-scroll")
	v.scroll.SetPolicy(gtk.PolicyNever, gtk.PolicyAutomatic)
	v.scroll.SetPropagateNaturalHeight(false)
	v.scroll.SetVExpand(true)
	v.scroll.SetChild(v.grid)
	v.root.Append(v.scroll)

	v.popGrid = newPickerGrid()
	v.pop = gtk.NewPopover()
	v.pop.AddCSSClass("picker-variants")
	v.pop.SetAutohide(true)
	v.pop.SetHasArrow(true)
	v.pop.SetPosition(gtk.PosBottom)
	v.pop.SetChild(v.popGrid)
	v.pop.SetParent(v.root)

	v.grid.ConnectChildActivated(func(c *gtk.FlowBoxChild) {
		j := c.Index()
		v.state.Highlight = j
		v.applyHighlight()
		if v.onCell != nil {
			v.onCell(j)
		}
	})
	v.popGrid.ConnectChildActivated(func(c *gtk.FlowBoxChild) {
		j := c.Index()
		if j < 0 || j >= len(v.popState.Visible) {
			return
		}
		v.popState.Highlight = j
		v.applyPopHighlight()
		if v.onVariant != nil {
			v.onVariant(v.popState.Items[v.popState.Visible[j]])
		}
	})
	v.pop.ConnectClosed(func() {
		v.popOpen = false
		if v.onClosed != nil {
			v.onClosed()
		}
	})
	v.entry.ConnectChanged(v.refilter)
	return v
}

// newPickerGrid returns a non-focusable FlowBox styled as a picker grid; the
// search entry keeps focus and the launcher drives the highlight.
func newPickerGrid() *gtk.FlowBox {
	g := gtk.NewFlowBox()
	g.AddCSSClass("picker-grid")
	g.SetHomogeneous(true)
	g.SetSelectionMode(gtk.SelectionSingle)
	g.SetActivateOnSingleClick(true)
	g.SetColumnSpacing(2)
	g.SetRowSpacing(2)
	g.SetVAlign(gtk.AlignStart)
	g.SetCanFocus(false)
	g.SetFocusOnClick(false)
	return g
}

// connect wires the launcher's callbacks: a grid cell activated, a variant
// chosen, the back button, and the variant popover closing.
func (v *pickerView) connect(onCell func(j int), onVariant func(providers.PickerItem), onBack, onClosed func()) {
	v.onCell = onCell
	v.onVariant = onVariant
	v.onClosed = onClosed
	v.back.ConnectClicked(onBack)
}

// load shows res.Picker on the page, reusing the cell pool. res.Picker must
// be non-nil.
func (v *pickerView) load(res providers.Result) {
	v.closeVariants()
	v.res = res
	p := *res.Picker
	cols := PickerColumns(p)
	v.grid.SetMinChildrenPerLine(uint(cols))
	v.grid.SetMaxChildrenPerLine(uint(cols))
	if p.Title != "" {
		v.entry.SetPlaceholderText("Search " + strings.ToLower(p.Title) + "…")
	} else {
		v.entry.SetPlaceholderText("Search…")
	}

	v.state = NewPickerState(p.Items, cols, true)
	v.ensureCells(len(p.Items))
	for j := range v.shown {
		v.shown[j] = cellUnknown
	}
	// SetText fires ::changed only when the text differs, so refilter runs
	// explicitly too; it is idempotent.
	v.entry.SetText(p.Query)
	v.refilter()
	v.entry.SetPosition(-1)
}

// refilter re-runs the filter over the entry text and re-renders the grid
// from the top.
func (v *pickerView) refilter() {
	v.state.Filter(v.entry.Text(), v.score)
	v.render()
	if adj := v.scroll.VAdjustment(); adj != nil {
		adj.SetValue(0)
	}
}

// ensureCells grows the pool to at least n hidden cells.
func (v *pickerView) ensureCells(n int) {
	for len(v.cells) < n {
		label := gtk.NewLabel("")
		label.AddCSSClass("picker-cell")
		c := gtk.NewFlowBoxChild()
		c.SetChild(label)
		c.SetCanFocus(false)
		c.SetFocusOnClick(false)
		c.SetVisible(false)
		v.grid.Append(c)
		v.cells = append(v.cells, c)
		v.labels = append(v.labels, label)
		v.shown = append(v.shown, cellHidden)
	}
}

// render writes state.Visible into the cell pool in order, touching only
// cells whose item changed. Filtering is done here rather than through
// FlowBox.SetFilterFunc/SetSortFunc: those call back into Go once per child
// (and n·log n times for the sort) on every keystroke.
func (v *pickerView) render() {
	for j, c := range v.cells {
		want := cellHidden
		if j < len(v.state.Visible) {
			want = v.state.Visible[j]
		}
		if v.shown[j] == want {
			continue
		}
		if want < 0 {
			c.SetVisible(false)
		} else {
			if v.shown[j] < 0 {
				c.SetVisible(true)
			}
			it := v.state.Items[want]
			v.labels[j].SetText(it.Value)
			// The tooltip is the only place the item's name is shown.
			c.SetTooltipText(it.Label)
		}
		v.shown[j] = want
	}
	v.applyHighlight()
}

// applyHighlight mirrors state.Highlight into the grid selection and keeps
// the highlighted cell scrolled into view.
func (v *pickerView) applyHighlight() {
	h := v.state.Highlight
	if h < 0 || h >= len(v.cells) {
		v.grid.UnselectAll()
		return
	}
	v.grid.SelectChild(v.cells[h])
	clampIntoView(v.scroll, &v.cells[h].Widget)
}

// openVariants pops up a chooser of it followed by its Variants, pointing at
// anchor. The item itself comes first and is pre-highlighted, so Enter Enter
// copies the default.
func (v *pickerView) openVariants(it providers.PickerItem, anchor *gtk.FlowBoxChild) {
	v.popGrid.RemoveAll()
	v.popCells = v.popCells[:0]
	list := append([]providers.PickerItem{it}, it.Variants...)
	for _, e := range list {
		label := gtk.NewLabel(e.Value)
		label.AddCSSClass("picker-cell")
		c := gtk.NewFlowBoxChild()
		c.SetChild(label)
		c.SetCanFocus(false)
		c.SetFocusOnClick(false)
		c.SetTooltipText(e.Label)
		v.popGrid.Append(c)
		v.popCells = append(v.popCells, c)
	}
	cols := max(min(v.state.Cols, len(list)), 1)
	v.popGrid.SetMinChildrenPerLine(uint(cols))
	v.popGrid.SetMaxChildrenPerLine(uint(cols))
	v.popState = NewPickerState(list, cols, false)
	v.applyPopHighlight()

	if b, ok := anchor.ComputeBounds(v.root); ok {
		r := gdk.NewRectangle(int(b.X()), int(b.Y()), int(b.Width()), int(b.Height()))
		v.pop.SetPointingTo(&r)
	}
	v.popOpen = true
	v.pop.Popup()
}

// variantsOpen reports whether the variant popover is up.
func (v *pickerView) variantsOpen() bool { return v.popOpen }

// closeVariants pops the variant popover down; its closed handler resets
// popOpen.
func (v *pickerView) closeVariants() {
	if v.popOpen {
		v.pop.Popdown()
	}
}

// movePop moves the popover's highlight.
func (v *pickerView) movePop(dir GridDir) {
	if v.popState.Move(dir, false) {
		v.applyPopHighlight()
	}
}

// applyPopHighlight mirrors popState.Highlight into the popover's selection.
func (v *pickerView) applyPopHighlight() {
	h := v.popState.Highlight
	if h < 0 || h >= len(v.popCells) {
		v.popGrid.UnselectAll()
		return
	}
	v.popGrid.SelectChild(v.popCells[h])
}

// drop releases the cell pool; the next load rebuilds it.
func (v *pickerView) drop() {
	v.closeVariants()
	v.grid.RemoveAll()
	v.cells, v.labels, v.shown = nil, nil, nil
	v.state = PickerState{}
}

// clampIntoView scrolls sw so w is inside its viewport. w's allocation must
// be relative to sw's child (a ListBox row or FlowBox child). Widgets added in
// this same main-loop iteration have no allocation yet, so the clamp is
// deferred to idle when w has not been sized.
func clampIntoView(sw *gtk.ScrolledWindow, w *gtk.Widget) {
	if sw == nil || w == nil {
		return
	}
	clamp := func() {
		adj := sw.VAdjustment()
		if adj == nil {
			return
		}
		a := w.Allocation()
		if a == nil {
			return
		}
		adj.ClampPage(float64(a.Y()), float64(a.Y()+a.Height()))
	}
	if a := w.Allocation(); a != nil && a.Height() > 0 {
		clamp()
		return
	}
	glib.IdleAdd(clamp)
}
