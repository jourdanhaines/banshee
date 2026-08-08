// Package icons holds the SVG icons compiled into the banshee binary —
// brand marks (GitHub, Railway) that no freedesktop icon theme ships.
//
// Icons are stored with fill="currentColor" and tinted at load time: SVG
// substitutes the accent color in, so every builtin renders in the theme
// accent like a symbolic icon would. Adding an icon is dropping an SVG into
// data/ — Has and SVG pick it up by basename.
package icons

import (
	"embed"
	"regexp"
	"strconv"
	"strings"
)

//go:embed data/*.svg
var files embed.FS

// RasterSize is the pixel size the SVG root element is rewritten to before
// rasterization. Brand SVGs commonly declare width/height "1em", which
// gdk-pixbuf rasterizes at 16px — upscaling that to a 24px row icon (times
// the display scale) is what fuzzy icons look like. Rendering at 2× the row
// size and letting GTK scale down keeps the icon crisp on 1x and 2x displays.
const RasterSize = 48

// svgSizeAttr matches width/height attributes on the root <svg> element (the
// bundled icons carry them nowhere else).
var svgSizeAttr = regexp.MustCompile(`(width|height)="[^"]*"`)

// withRasterSize rewrites (or injects) the root width/height so the pixbuf
// loader rasterizes at RasterSize instead of whatever unit the source used.
func withRasterSize(svg string) string {
	px := strconv.Itoa(RasterSize)
	if svgSizeAttr.MatchString(svg) {
		return svgSizeAttr.ReplaceAllString(svg, `$1="`+px+`"`)
	}
	return strings.Replace(svg, "<svg", `<svg width="`+px+`" height="`+px+`"`, 1)
}

// Has reports whether a builtin icon with this name is compiled in.
func Has(name string) bool {
	_, err := files.ReadFile("data/" + name + ".svg")
	return err == nil
}

// SVG returns the named icon's SVG bytes, display-ready: every currentColor
// reference replaced by accentHex (a "#rrggbb" color) and the root sized to
// RasterSize. ok is false for unknown names.
func SVG(name, accentHex string) ([]byte, bool) {
	b, err := files.ReadFile("data/" + name + ".svg")
	if err != nil {
		return nil, false
	}
	svg := strings.ReplaceAll(string(b), "currentColor", accentHex)
	return []byte(withRasterSize(svg)), true
}

// Names returns the builtin icon names, for doctor/debug output.
func Names() []string {
	entries, err := files.ReadDir("data")
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, strings.TrimSuffix(e.Name(), ".svg"))
	}
	return out
}
