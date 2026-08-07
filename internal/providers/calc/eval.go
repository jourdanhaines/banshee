// Package calc provides the inline calculator: a hand-written arithmetic
// expression evaluator and a Provider that turns math-shaped queries into a
// single answer row whose activation copies the result to the clipboard.
package calc

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Eval parses and evaluates an arithmetic expression.
//
// Grammar (low → high precedence):
//
//	expr    := term  (('+'|'-') term)*          left-associative
//	term    := unary (('*'|'/'|'%') unary)*     left-associative, % is math.Mod
//	unary   := ('-'|'+') unary | power
//	power   := primary ('^' unary)?             right-associative: 2^3^2 = 512
//	primary := NUMBER | IDENT | IDENT '(' expr ')' | '(' expr ')'
//
// Unary binding above power gives the conventional -2^2 = -4 while the
// ('^' unary) right side allows 2^-3. Constants pi and e and one-argument
// functions (sqrt, abs, floor, ceil, round) are the only identifiers.
// Division and mod by zero follow IEEE semantics (Inf/NaN) — callers filter
// non-finite results.
func Eval(expr string) (float64, error) {
	toks, err := lex(expr)
	if err != nil {
		return 0, err
	}
	p := &parser{toks: toks}
	v, err := p.expr()
	if err != nil {
		return 0, err
	}
	if p.peek().kind != tokEOF {
		return 0, fmt.Errorf("calc: unexpected %q at position %d", p.peek().text, p.peek().pos)
	}
	return v, nil
}

// Format renders v the way the launcher shows it (and copies it): integral
// values without a decimal point, everything else with 12 significant digits
// so float noise like 0.1+0.2 → 0.30000000000000004 never surfaces.
func Format(v float64) string {
	if v == math.Trunc(v) && math.Abs(v) < 1e15 {
		return strconv.FormatFloat(v, 'f', -1, 64)
	}
	return strconv.FormatFloat(v, 'g', 12, 64)
}

// constants and functions available as identifiers in expressions.
var (
	constants = map[string]float64{
		"pi": math.Pi,
		"e":  math.E,
	}
	functions = map[string]func(float64) float64{
		"sqrt":  math.Sqrt,
		"abs":   math.Abs,
		"floor": math.Floor,
		"ceil":  math.Ceil,
		"round": math.Round,
	}
)

type tokenKind int

const (
	tokEOF tokenKind = iota
	tokNumber
	tokIdent
	tokOp // single-rune operator or parenthesis
)

type token struct {
	kind tokenKind
	text string
	val  float64 // tokNumber only
	pos  int     // byte offset in the input, for error messages
}

// lex tokenizes expr. Numbers are [0-9]+(.[0-9]+)?([eE][+-]?[0-9]+)? — a
// second dot in "1.2.3" is a syntax error, which is load-bearing for the
// provider's auto-detect (version strings and IPs must not evaluate).
func lex(expr string) ([]token, error) {
	var toks []token
	i := 0
	for i < len(expr) {
		c := expr[i]
		switch {
		case c == ' ' || c == '\t':
			i++
		case c >= '0' && c <= '9' || c == '.':
			start := i
			for i < len(expr) && expr[i] >= '0' && expr[i] <= '9' {
				i++
			}
			if i < len(expr) && expr[i] == '.' {
				i++
				for i < len(expr) && expr[i] >= '0' && expr[i] <= '9' {
					i++
				}
			}
			if i < len(expr) && (expr[i] == 'e' || expr[i] == 'E') {
				j := i + 1
				if j < len(expr) && (expr[j] == '+' || expr[j] == '-') {
					j++
				}
				if j < len(expr) && expr[j] >= '0' && expr[j] <= '9' {
					for j < len(expr) && expr[j] >= '0' && expr[j] <= '9' {
						j++
					}
					i = j
				}
			}
			text := expr[start:i]
			v, err := strconv.ParseFloat(text, 64)
			if err != nil {
				return nil, fmt.Errorf("calc: bad number %q at position %d", text, start)
			}
			toks = append(toks, token{kind: tokNumber, text: text, val: v, pos: start})
		case c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z':
			start := i
			for i < len(expr) && (expr[i] >= 'a' && expr[i] <= 'z' || expr[i] >= 'A' && expr[i] <= 'Z') {
				i++
			}
			toks = append(toks, token{kind: tokIdent, text: strings.ToLower(expr[start:i]), pos: start})
		case strings.ContainsRune("+-*/%^()", rune(c)):
			toks = append(toks, token{kind: tokOp, text: string(c), pos: i})
			i++
		default:
			return nil, fmt.Errorf("calc: unexpected character %q at position %d", c, i)
		}
	}
	toks = append(toks, token{kind: tokEOF, text: "end of expression", pos: len(expr)})
	return toks, nil
}

// parser is a recursive-descent parser that evaluates during the parse — the
// grammar is small enough that an AST would be pure overhead.
type parser struct {
	toks []token
	i    int
}

func (p *parser) peek() token { return p.toks[p.i] }

func (p *parser) next() token {
	t := p.toks[p.i]
	if t.kind != tokEOF {
		p.i++
	}
	return t
}

// acceptOp consumes the next token if it is one of the given operators.
func (p *parser) acceptOp(ops string) (string, bool) {
	t := p.peek()
	if t.kind == tokOp && strings.Contains(ops, t.text) {
		p.next()
		return t.text, true
	}
	return "", false
}

func (p *parser) expr() (float64, error) {
	v, err := p.term()
	if err != nil {
		return 0, err
	}
	for {
		op, ok := p.acceptOp("+-")
		if !ok {
			return v, nil
		}
		rhs, err := p.term()
		if err != nil {
			return 0, err
		}
		if op == "+" {
			v += rhs
		} else {
			v -= rhs
		}
	}
}

func (p *parser) term() (float64, error) {
	v, err := p.unary()
	if err != nil {
		return 0, err
	}
	for {
		op, ok := p.acceptOp("*/%")
		if !ok {
			return v, nil
		}
		rhs, err := p.unary()
		if err != nil {
			return 0, err
		}
		switch op {
		case "*":
			v *= rhs
		case "/":
			v /= rhs
		case "%":
			v = math.Mod(v, rhs)
		}
	}
}

func (p *parser) unary() (float64, error) {
	if op, ok := p.acceptOp("+-"); ok {
		v, err := p.unary()
		if err != nil {
			return 0, err
		}
		if op == "-" {
			return -v, nil
		}
		return v, nil
	}
	return p.power()
}

func (p *parser) power() (float64, error) {
	v, err := p.primary()
	if err != nil {
		return 0, err
	}
	if _, ok := p.acceptOp("^"); ok {
		rhs, err := p.unary()
		if err != nil {
			return 0, err
		}
		return math.Pow(v, rhs), nil
	}
	return v, nil
}

func (p *parser) primary() (float64, error) {
	t := p.next()
	switch t.kind {
	case tokNumber:
		return t.val, nil
	case tokIdent:
		if c, ok := constants[t.text]; ok {
			return c, nil
		}
		fn, ok := functions[t.text]
		if !ok {
			return 0, fmt.Errorf("calc: unknown identifier %q at position %d", t.text, t.pos)
		}
		if _, ok := p.acceptOp("("); !ok {
			return 0, fmt.Errorf("calc: %s requires an argument at position %d", t.text, t.pos)
		}
		arg, err := p.expr()
		if err != nil {
			return 0, err
		}
		if _, ok := p.acceptOp(")"); !ok {
			return 0, fmt.Errorf("calc: missing ) at position %d", p.peek().pos)
		}
		return fn(arg), nil
	case tokOp:
		if t.text == "(" {
			v, err := p.expr()
			if err != nil {
				return 0, err
			}
			if _, ok := p.acceptOp(")"); !ok {
				return 0, fmt.Errorf("calc: missing ) at position %d", p.peek().pos)
			}
			return v, nil
		}
	}
	return 0, fmt.Errorf("calc: unexpected %q at position %d", t.text, t.pos)
}
