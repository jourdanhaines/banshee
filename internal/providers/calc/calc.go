package calc

import (
	"context"
	"math"
	"regexp"
	"strings"

	"github.com/jourdanhaines/banshee/internal/providers"
)

// Score given to every calc row. The aggregator sorts by (-Score, Category,
// Title); a query that is a calculation should answer at the top.
const Score = 1000

// datePattern rejects date-shaped queries like 2025-01-01 that would
// otherwise auto-evaluate as subtraction.
var datePattern = regexp.MustCompile(`^\d{4}-\d{1,2}-\d{1,2}$`)

// leadingZero rejects zero-padded operands like 007 or 01+1 — those are IDs
// and dates, not arithmetic. 0.5 and 10 stay legal.
var leadingZero = regexp.MustCompile(`(^|[^0-9.])0[0-9]`)

// Provider turns math-shaped queries into a single answer row. It is
// stateless; activation copies the result (Enter) or the whole equation
// (Tab / Shift+Enter) to the clipboard.
type Provider struct{}

// New returns the calculator provider.
func New() *Provider { return &Provider{} }

// Name implements providers.Provider.
func (p *Provider) Name() string { return "calc" }

// Query evaluates q when it is forced with a "=" or "calc " prefix, or when
// it auto-detects as arithmetic: it must evaluate to a finite value AND
// contain an actual operation (a bare number like "42" is part of someone
// else's query, not a calculation).
func (p *Provider) Query(ctx context.Context, q string) ([]providers.Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	expr, forced := stripPrefix(strings.TrimSpace(q))
	if expr == "" {
		return nil, nil
	}
	if !forced && (datePattern.MatchString(expr) || leadingZero.MatchString(expr)) {
		return nil, nil
	}
	v, err := Eval(expr)
	if err != nil || math.IsInf(v, 0) || math.IsNaN(v) {
		return nil, nil
	}
	if !forced && !hasOperation(expr) {
		return nil, nil
	}
	res := Format(v)
	return []providers.Result{{
		ID:       "calc:" + expr,
		Title:    res,
		Subtitle: expr + " = " + res + " · Enter copies result, Tab copies equation",
		Icon:     providers.Icon{ThemeName: "accessories-calculator-symbolic"},
		Category: providers.CatCalc,
		Score:    Score,
		Action:   providers.Action{Kind: providers.ActClipboardCopy, Text: res},
		AltAction: &providers.Action{
			Kind: providers.ActClipboardCopy,
			Text: expr + " = " + res,
		},
	}}, nil
}

// stripPrefix removes a forcing prefix ("=" or "calc ") and reports whether
// one was present.
func stripPrefix(q string) (expr string, forced bool) {
	if rest, ok := strings.CutPrefix(q, "="); ok {
		return strings.TrimSpace(rest), true
	}
	if rest, ok := strings.CutPrefix(q, "calc "); ok {
		return strings.TrimSpace(rest), true
	}
	return q, false
}

// hasOperation reports whether expr contains a binary operator or a function
// call — the auto-detect gate that keeps bare numbers ("42", "-5", "3.14")
// and lone constants ("e") from producing a calc row.
func hasOperation(expr string) bool {
	toks, err := lex(expr)
	if err != nil {
		return false
	}
	for i, t := range toks {
		if t.kind == tokIdent && functions[t.text] != nil {
			return true
		}
		if t.kind != tokOp {
			continue
		}
		switch t.text {
		case "*", "/", "%", "^":
			return true
		case "+", "-":
			// Binary only when it follows a value; a leading or
			// post-operator sign is unary.
			if i > 0 {
				prev := toks[i-1]
				if prev.kind == tokNumber || prev.kind == tokIdent || prev.kind == tokOp && prev.text == ")" {
					return true
				}
			}
		}
	}
	return false
}
