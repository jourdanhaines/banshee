package ui

import (
	"strings"

	"github.com/jourdanhaines/banshee/internal/providers"
)

// TrimValues returns a copy of values with every value TrimSpace'd, so
// providers and validation both see what the user meant rather than stray
// whitespace.
func TrimValues(values map[string]string) map[string]string {
	out := make(map[string]string, len(values))
	for k, v := range values {
		out[k] = strings.TrimSpace(v)
	}
	return out
}

// FirstMissingRequired returns the index of the first required field whose
// value is empty. ok is true when the form is submittable.
func FirstMissingRequired(fields []providers.FormField, values map[string]string) (int, bool) {
	for i, f := range fields {
		if len(f.Options) > 0 {
			// A dropdown always carries one of its own options (the first is
			// preselected), so Required is satisfied by construction. Skipping
			// it also means a caller that never rendered the field — or a
			// values map assembled elsewhere — cannot fail validation on a
			// choice the user was never asked to make.
			continue
		}
		if f.Required && strings.TrimSpace(values[f.Key]) == "" {
			return i, false
		}
	}
	return -1, true
}
