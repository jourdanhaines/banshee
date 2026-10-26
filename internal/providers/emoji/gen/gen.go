package main

import (
	"bufio"
	"encoding/xml"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"
)

// row is one fully-qualified line of emoji-test.txt.
type row struct {
	runes []rune
	value string
	name  string
}

// entry is a base emoji with its keywords and skin-tone variants.
type entry struct {
	row
	keywords []string
	variants []row
}

// parseEmojiTest reads emoji-test.txt and returns its fully-qualified rows in
// file order, plus the version from the "# Version:" header.
func parseEmojiTest(r io.Reader) ([]row, string, error) {
	var (
		rows    []row
		version string
	)
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	n := 0
	for sc.Scan() {
		n++
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") {
			if v, ok := strings.CutPrefix(line, "# Version:"); ok {
				version = strings.TrimSpace(v)
			}
			continue
		}
		data, comment, ok := strings.Cut(line, "#")
		if !ok {
			return nil, "", fmt.Errorf("line %d: missing comment", n)
		}
		cps, status, ok := strings.Cut(data, ";")
		if !ok {
			return nil, "", fmt.Errorf("line %d: missing ';'", n)
		}
		if strings.TrimSpace(status) != "fully-qualified" {
			continue
		}
		var runes []rune
		for _, f := range strings.Fields(cps) {
			v, err := strconv.ParseUint(f, 16, 32)
			if err != nil {
				return nil, "", fmt.Errorf("line %d: bad code point %q: %w", n, f, err)
			}
			if !utf8.ValidRune(rune(v)) {
				return nil, "", fmt.Errorf("line %d: invalid rune %q", n, f)
			}
			runes = append(runes, rune(v))
		}
		if len(runes) == 0 {
			return nil, "", fmt.Errorf("line %d: no code points", n)
		}
		parts := strings.SplitN(strings.TrimSpace(comment), " ", 3)
		if len(parts) != 3 || !strings.HasPrefix(parts[1], "E") {
			return nil, "", fmt.Errorf("line %d: malformed comment %q", n, comment)
		}
		value := string(runes)
		if parts[0] != value {
			return nil, "", fmt.Errorf("line %d: glyph %q does not match code points %q", n, parts[0], value)
		}
		name := parts[2]
		if strings.ContainsAny(name, "\t\n") {
			return nil, "", fmt.Errorf("line %d: name contains TAB or LF", n)
		}
		rows = append(rows, row{runes: runes, value: value, name: name})
	}
	if err := sc.Err(); err != nil {
		return nil, "", fmt.Errorf("line %d: %w", n+1, err)
	}
	return rows, version, nil
}

// isModifier reports whether r is a Fitzpatrick skin-tone modifier.
func isModifier(r rune) bool { return r >= 0x1F3FB && r <= 0x1F3FF }

// stripModifiers returns rs without its skin-tone modifiers.
func stripModifiers(rs []rune) []rune {
	out := make([]rune, 0, len(rs))
	for _, r := range rs {
		if !isModifier(r) {
			out = append(out, r)
		}
	}
	return out
}

// norm drops every U+FE0F so emoji-test and CLDR spellings compare equal.
func norm(rs []rune) string {
	var b strings.Builder
	for _, r := range rs {
		if r != 0xFE0F {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func hasModifier(rs []rune) bool {
	for _, r := range rs {
		if isModifier(r) {
			return true
		}
	}
	return false
}

// baseName drops every "… skin tone" attribute after ": ", so
// "man: light skin tone, red hair" becomes "man: red hair".
func baseName(name string) string {
	head, tail, ok := strings.Cut(name, ": ")
	if !ok {
		return name
	}
	var keep []string
	for _, p := range strings.Split(tail, ", ") {
		if !strings.HasSuffix(p, " skin tone") {
			keep = append(keep, p)
		}
	}
	if len(keep) == 0 {
		return head
	}
	return head + ": " + strings.Join(keep, ", ")
}

// group folds skin-tone rows under their base, preserving file order. A
// toned row with no resolvable base is an error rather than a silent drop.
func group(rows []row) ([]entry, error) {
	var es []entry
	byKey := map[string]int{}
	byName := map[string]int{}
	for _, r := range rows {
		if hasModifier(r.runes) {
			continue
		}
		k := norm(r.runes)
		if _, dup := byKey[k]; dup {
			return nil, fmt.Errorf("duplicate base sequence %q (%s)", r.value, r.name)
		}
		if _, dup := byName[r.name]; dup {
			return nil, fmt.Errorf("duplicate base name %q", r.name)
		}
		byKey[k] = len(es)
		byName[r.name] = len(es)
		es = append(es, entry{row: r})
	}
	for _, r := range rows {
		if !hasModifier(r.runes) {
			continue
		}
		i, ok := byKey[norm(stripModifiers(r.runes))]
		if !ok {
			i, ok = byName[baseName(r.name)]
		}
		if !ok {
			if head, _, cut := strings.Cut(r.name, ": "); cut {
				i, ok = byName[head]
			}
		}
		if !ok {
			return nil, fmt.Errorf("no base for %q (%s)", r.value, r.name)
		}
		if !containsValue(es[i].variants, r.value) {
			es[i].variants = append(es[i].variants, r)
		}
	}
	return es, nil
}

func containsValue(rs []row, v string) bool {
	for _, r := range rs {
		if r.value == v {
			return true
		}
	}
	return false
}

type ldml struct {
	Annotations []struct {
		CP   string `xml:"cp,attr"`
		Type string `xml:"type,attr"`
		Text string `xml:",chardata"`
	} `xml:"annotations>annotation"`
}

// parseAnnotations reads a CLDR annotations file into keywords keyed by the
// FE0F-free sequence; text-to-speech names are skipped.
func parseAnnotations(r io.Reader) (map[string][]string, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	var doc ldml
	if err := xml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse annotations: %w", err)
	}
	out := map[string][]string{}
	for _, a := range doc.Annotations {
		if a.Type == "tts" {
			continue
		}
		k := norm([]rune(a.CP))
		for _, kw := range strings.Split(a.Text, "|") {
			if kw = strings.TrimSpace(kw); kw != "" {
				out[k] = append(out[k], kw)
			}
		}
	}
	return out, nil
}

// attachKeywords gives each base the keywords of the first source that has
// it, minus its own label and duplicates.
func attachKeywords(es []entry, sources ...map[string][]string) error {
	for i := range es {
		e := &es[i]
		k := norm(e.runes)
		var kws []string
		for _, src := range sources {
			if hit := src[k]; len(hit) > 0 {
				kws = hit
				break
			}
		}
		seen := map[string]bool{}
		e.keywords = nil
		for _, kw := range kws {
			if strings.ContainsAny(kw, "\t\n|") {
				return fmt.Errorf("keyword %q for %q contains TAB, LF or '|'", kw, e.value)
			}
			if strings.EqualFold(kw, e.name) || seen[kw] {
				continue
			}
			seen[kw] = true
			e.keywords = append(e.keywords, kw)
		}
	}
	return nil
}

// write emits the TSV dataset: "# " header lines, then each base line
// followed by its TAB-prefixed variant lines.
func write(w io.Writer, es []entry, header []string) error {
	bw := bufio.NewWriter(w)
	for _, h := range header {
		if strings.Contains(h, "\n") {
			return fmt.Errorf("header line contains LF: %q", h)
		}
		fmt.Fprintf(bw, "# %s\n", h)
	}
	for _, e := range es {
		if err := checkFields(e.value, e.name); err != nil {
			return err
		}
		for _, kw := range e.keywords {
			if strings.ContainsAny(kw, "\t\n|") {
				return fmt.Errorf("keyword %q for %q contains TAB, LF or '|'", kw, e.value)
			}
		}
		fmt.Fprintf(bw, "%s\t%s\t%s\n", e.value, e.name, strings.Join(e.keywords, "|"))
		for _, v := range e.variants {
			if err := checkFields(v.value, v.name); err != nil {
				return err
			}
			fmt.Fprintf(bw, "\t%s\t%s\n", v.value, v.name)
		}
	}
	return bw.Flush()
}

func checkFields(fields ...string) error {
	for _, f := range fields {
		if f == "" || strings.ContainsAny(f, "\t\n") {
			return fmt.Errorf("field %q is empty or contains TAB or LF", f)
		}
	}
	return nil
}
