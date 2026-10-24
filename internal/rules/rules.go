// Package rules evaluates {path, op, value} conditions (domain-model.md
// §7.2, §13.3) against decoded JSON. Route matching (Phase 3) and success
// body rules (Phase 4) share this single implementation. It depends only on
// config for the Rule type; evaluation is pure.
package rules

import (
	"encoding/json"
	"math/big"
	"strconv"
	"strings"

	"messagehub/internal/config"
)

const pathSeparator = "."

// Limits on number literals compared exactly. big.Rat parses exponents up
// to about 1e6 and gets slower with them; payloads and vendor responses are
// untrusted, so larger literals are treated as unparsable (never equal).
const (
	maxNumberLiteralLen = 64
	maxNumberExponent   = 400
)

// Lookup follows a dot-separated path through decoded JSON. Array elements
// are addressed by decimal index. A path is not found when any segment is
// missing, out of range, or crosses a non-container value.
func Lookup(doc any, path string) (any, bool) {
	if path == "" {
		return nil, false
	}
	cur := doc
	for _, seg := range strings.Split(path, pathSeparator) {
		next, ok := child(cur, seg)
		if !ok {
			return nil, false
		}
		cur = next
	}
	return cur, true
}

func child(container any, seg string) (any, bool) {
	switch c := container.(type) {
	case map[string]any:
		v, ok := c[seg]
		return v, ok
	case []any:
		i, err := strconv.Atoi(seg)
		if err != nil || i < 0 || i >= len(c) || seg != strconv.Itoa(i) {
			return nil, false
		}
		return c[i], true
	default:
		return nil, false
	}
}

// Eval reports whether doc satisfies rule. An unknown operator never matches.
func Eval(rule config.Rule, doc any) bool {
	v, found := Lookup(doc, rule.Path)
	switch rule.Op {
	case config.OpEq:
		return found && Equal(v, rule.Value)
	case config.OpNe:
		return !found || !Equal(v, rule.Value)
	case config.OpIn:
		return found && inList(v, rule.Value)
	case config.OpExists:
		return found
	case config.OpNotExists:
		return !found
	default:
		return false
	}
}

// EvalAll reports whether doc satisfies every rule; no rules means true.
func EvalAll(rules []config.Rule, doc any) bool {
	for _, r := range rules {
		if !Eval(r, doc) {
			return false
		}
	}
	return true
}

func inList(v, list any) bool {
	items, ok := list.([]any)
	if !ok {
		return false
	}
	for _, item := range items {
		if Equal(v, item) {
			return true
		}
	}
	return false
}

// Equal compares two decoded JSON values. Numbers compare by exact decimal
// value whatever their Go type (json.Number, float64, int, int64), so 0
// equals 0.0; a float64 compares by its shortest decimal form, so
// float64(0.1) equals json.Number("0.1"). Values of different JSON types are
// never equal ("0" ≠ 0).
func Equal(a, b any) bool {
	if ra, ok := number(a); ok {
		rb, ok := number(b)
		return ok && ra != nil && rb != nil && ra.Cmp(rb) == 0
	}
	switch x := a.(type) {
	case nil:
		return b == nil
	case bool:
		y, ok := b.(bool)
		return ok && x == y
	case string:
		y, ok := b.(string)
		return ok && x == y
	case []any:
		y, ok := b.([]any)
		return ok && equalSlices(x, y)
	case map[string]any:
		y, ok := b.(map[string]any)
		return ok && equalMaps(x, y)
	default:
		return false
	}
}

// number reports whether v is a JSON number and returns its exact value;
// the value is nil when a json.Number does not parse.
func number(v any) (*big.Rat, bool) {
	switch n := v.(type) {
	case json.Number:
		return parseDecimal(n.String()), true
	case float64:
		return parseDecimal(strconv.FormatFloat(n, 'g', -1, 64)), true
	case int:
		return new(big.Rat).SetInt64(int64(n)), true
	case int64:
		return new(big.Rat).SetInt64(n), true
	default:
		return nil, false
	}
}

// parseDecimal returns the exact value of a decimal literal, or nil when it
// does not parse (including NaN and ±Inf) or exceeds the size limits.
func parseDecimal(s string) *big.Rat {
	if len(s) > maxNumberLiteralLen {
		return nil
	}
	if i := strings.IndexAny(s, "eE"); i >= 0 {
		exp, err := strconv.Atoi(s[i+1:])
		if err != nil || exp > maxNumberExponent || exp < -maxNumberExponent {
			return nil
		}
	}
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		return nil
	}
	return r
}

func equalSlices(a, b []any) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !Equal(a[i], b[i]) {
			return false
		}
	}
	return true
}

func equalMaps(a, b map[string]any) bool {
	if len(a) != len(b) {
		return false
	}
	for k, av := range a {
		bv, ok := b[k]
		if !ok || !Equal(av, bv) {
			return false
		}
	}
	return true
}
