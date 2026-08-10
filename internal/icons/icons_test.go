package icons

import (
	"bytes"
	"testing"
)

func TestHas(t *testing.T) {
	for _, name := range []string{"github", "railway", "steam"} {
		if !Has(name) {
			t.Errorf("Has(%q) = false", name)
		}
	}
	if Has("nope") {
		t.Error("Has(nope) = true")
	}
}

func TestSVG(t *testing.T) {
	b, ok := SVG("github", "#7aa2f7")
	if !ok || len(b) == 0 {
		t.Fatalf("SVG(github) = %d bytes, ok=%v", len(b), ok)
	}
	if bytes.Contains(b, []byte("currentColor")) {
		t.Error("currentColor not substituted")
	}
	if !bytes.Contains(b, []byte("#7aa2f7")) {
		t.Error("accent color missing from recolored SVG")
	}
	if bytes.Contains(b, []byte("1em")) || !bytes.Contains(b, []byte(`width="48"`)) || !bytes.Contains(b, []byte(`height="48"`)) {
		t.Errorf("root not resized to %d px: %s", RasterSize, b)
	}
	if _, ok := SVG("nope", "#000000"); ok {
		t.Error("SVG(nope) ok = true")
	}
}

func TestWithRasterSizeInjectsWhenAbsent(t *testing.T) {
	got := withRasterSize(`<svg viewBox="0 0 24 24"><path d="M0 0"/></svg>`)
	if got != `<svg width="48" height="48" viewBox="0 0 24 24"><path d="M0 0"/></svg>` {
		t.Errorf("withRasterSize = %s", got)
	}
}

func TestNames(t *testing.T) {
	names := Names()
	if len(names) < 2 {
		t.Fatalf("Names() = %v", names)
	}
	seen := map[string]bool{}
	for _, n := range names {
		seen[n] = true
	}
	if !seen["github"] || !seen["railway"] {
		t.Errorf("Names() = %v, want github + railway", names)
	}
}
