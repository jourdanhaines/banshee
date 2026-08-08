package steam

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode"
)

// vdfNode is one object in a Valve KeyValues ("VDF") file: a key/value map
// whose values are either string or a nested vdfNode. Steam's manifests are
// plain text, so a minimal hand-rolled parser (the same call the apps
// provider makes on .desktop files) beats a dependency.
type vdfNode map[string]any

// child returns the nested node under key, or nil when the key is absent or
// holds a string.
func (n vdfNode) child(key string) vdfNode {
	c, _ := n[key].(vdfNode)
	return c
}

// str returns the string under key, or "" when the key is absent or holds a
// nested node.
func (n vdfNode) str(key string) string {
	s, _ := n[key].(string)
	return s
}

// parseVDF reads a KeyValues document into its root node. Supported grammar
// is exactly what Steam writes into appmanifest_*.acf and libraryfolders.vdf:
// quoted tokens with \" and \\ escapes, bare tokens, { } nesting, //
// comments, and CRLF line endings. Unknown keys are kept — callers pick what
// they need — so new manifest fields never break parsing. Platform
// conditionals ([$WIN32]) are not supported; Steam's on-disk manifests do not
// use them.
func parseVDF(r io.Reader) (vdfNode, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	lx := &vdfLexer{src: string(data)}
	root, err := parseVDFBody(lx, false)
	if err != nil {
		return nil, err
	}
	return root, nil
}

// parseVDFBody parses key/value pairs until EOF (nested=false) or a closing
// brace (nested=true).
func parseVDFBody(lx *vdfLexer, nested bool) (vdfNode, error) {
	node := vdfNode{}
	for {
		tok, err := lx.next()
		if err != nil {
			return nil, err
		}
		switch tok.kind {
		case tokEOF:
			if nested {
				return nil, errors.New("vdf: unexpected end of input inside braces")
			}
			return node, nil
		case tokClose:
			if !nested {
				return nil, errors.New("vdf: unbalanced closing brace")
			}
			return node, nil
		case tokOpen:
			return nil, errors.New("vdf: brace where a key was expected")
		}

		val, err := lx.next()
		if err != nil {
			return nil, err
		}
		switch val.kind {
		case tokString:
			node[tok.text] = val.text
		case tokOpen:
			child, err := parseVDFBody(lx, true)
			if err != nil {
				return nil, err
			}
			node[tok.text] = child
		default:
			return nil, fmt.Errorf("vdf: key %q has no value", tok.text)
		}
	}
}

type vdfTokenKind int

const (
	tokEOF vdfTokenKind = iota
	tokString
	tokOpen
	tokClose
)

type vdfToken struct {
	kind vdfTokenKind
	text string
}

type vdfLexer struct {
	src string
	pos int
}

func (l *vdfLexer) next() (vdfToken, error) {
	for l.pos < len(l.src) {
		c := l.src[l.pos]
		switch {
		case unicode.IsSpace(rune(c)):
			l.pos++
		case c == '/' && l.pos+1 < len(l.src) && l.src[l.pos+1] == '/':
			if nl := strings.IndexByte(l.src[l.pos:], '\n'); nl < 0 {
				l.pos = len(l.src)
			} else {
				l.pos += nl + 1
			}
		case c == '{':
			l.pos++
			return vdfToken{kind: tokOpen}, nil
		case c == '}':
			l.pos++
			return vdfToken{kind: tokClose}, nil
		case c == '"':
			return l.quoted()
		default:
			return l.bare(), nil
		}
	}
	return vdfToken{kind: tokEOF}, nil
}

func (l *vdfLexer) quoted() (vdfToken, error) {
	l.pos++ // opening quote
	var b strings.Builder
	for l.pos < len(l.src) {
		c := l.src[l.pos]
		switch c {
		case '"':
			l.pos++
			return vdfToken{kind: tokString, text: b.String()}, nil
		case '\\':
			if l.pos+1 >= len(l.src) {
				return vdfToken{}, errors.New("vdf: dangling escape at end of input")
			}
			next := l.src[l.pos+1]
			switch next {
			case '"', '\\':
				b.WriteByte(next)
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			default:
				// Unknown escape: keep it verbatim, forward compatible.
				b.WriteByte('\\')
				b.WriteByte(next)
			}
			l.pos += 2
		default:
			b.WriteByte(c)
			l.pos++
		}
	}
	return vdfToken{}, errors.New("vdf: unterminated quoted token")
}

func (l *vdfLexer) bare() vdfToken {
	start := l.pos
	for l.pos < len(l.src) {
		c := l.src[l.pos]
		if unicode.IsSpace(rune(c)) || c == '{' || c == '}' || c == '"' {
			break
		}
		l.pos++
	}
	return vdfToken{kind: tokString, text: l.src[start:l.pos]}
}
