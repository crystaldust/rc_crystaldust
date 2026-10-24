package rules

import (
	"bytes"
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"testing"

	"messagehub/internal/config"
	"messagehub/internal/contracts"
	"messagehub/internal/domain"
)

const successCasesPath = "../../testdata/contracts/success/cases.yaml"

// decode parses JSON the way production does: numbers stay json.Number.
func decode(t *testing.T, s string) any {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		t.Fatalf("decode %q: %v", s, err)
	}
	return v
}

func TestLookup(t *testing.T) {
	doc := map[string]any{
		"code": json.Number("0"),
		"data": map[string]any{
			"result": []any{map[string]any{"status": "done"}},
			"empty":  nil,
		},
		"name": "hub",
	}
	tests := []struct {
		name      string
		path      string
		want      any
		wantFound bool
	}{
		{"top level", "code", json.Number("0"), true},
		{"nested object and array index", "data.result.0.status", "done", true},
		{"null value is found", "data.empty", nil, true},
		{"missing key", "data.missing", nil, false},
		{"index out of range", "data.result.1", nil, false},
		{"negative index", "data.result.-1", nil, false},
		{"non-numeric index", "data.result.first", nil, false},
		{"through a scalar", "name.length", nil, false},
		{"through null", "data.empty.x", nil, false},
		{"empty path", "", nil, false},
		{"empty segment", "data..result", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got, found := Lookup(doc, tt.path)

			// Assert
			if found != tt.wantFound {
				t.Fatalf("Lookup(%q) found = %v, want %v", tt.path, found, tt.wantFound)
			}
			if found && !Equal(got, tt.want) {
				t.Errorf("Lookup(%q) = %#v, want %#v", tt.path, got, tt.want)
			}
		})
	}
}

func TestEval(t *testing.T) {
	doc := map[string]any{
		"code":   json.Number("0"),
		"ratio":  json.Number("1.5"),
		"text":   "0",
		"flag":   true,
		"none":   nil,
		"list":   []any{json.Number("1"), "a"},
		"object": map[string]any{"k": "v"},
	}
	tests := []struct {
		name string
		rule config.Rule
		want bool
	}{
		{"eq number", config.Rule{Path: "code", Op: config.OpEq, Value: json.Number("0")}, true},
		{"eq number by value 0.0", config.Rule{Path: "code", Op: config.OpEq, Value: json.Number("0.0")}, true},
		{"eq float64 rule value", config.Rule{Path: "code", Op: config.OpEq, Value: float64(0)}, true},
		{"eq int rule value", config.Rule{Path: "code", Op: config.OpEq, Value: 0}, true},
		{"eq exponent form", config.Rule{Path: "ratio", Op: config.OpEq, Value: json.Number("15e-1")}, true},
		{"eq different number", config.Rule{Path: "code", Op: config.OpEq, Value: json.Number("1")}, false},
		{"eq string vs number", config.Rule{Path: "text", Op: config.OpEq, Value: json.Number("0")}, false},
		{"eq number vs string", config.Rule{Path: "code", Op: config.OpEq, Value: "0"}, false},
		{"eq bool", config.Rule{Path: "flag", Op: config.OpEq, Value: true}, true},
		{"eq null", config.Rule{Path: "none", Op: config.OpEq, Value: nil}, true},
		{"eq null vs false", config.Rule{Path: "none", Op: config.OpEq, Value: false}, false},
		{"eq array", config.Rule{Path: "list", Op: config.OpEq, Value: []any{float64(1), "a"}}, true},
		{"eq array different length", config.Rule{Path: "list", Op: config.OpEq, Value: []any{float64(1)}}, false},
		{"eq object", config.Rule{Path: "object", Op: config.OpEq, Value: map[string]any{"k": "v"}}, true},
		{"eq object different value", config.Rule{Path: "object", Op: config.OpEq, Value: map[string]any{"k": "w"}}, false},
		{"eq object different keys", config.Rule{Path: "object", Op: config.OpEq, Value: map[string]any{"x": "v"}}, false},
		{"eq missing path", config.Rule{Path: "missing", Op: config.OpEq, Value: nil}, false},
		{"ne different value", config.Rule{Path: "code", Op: config.OpNe, Value: json.Number("1")}, true},
		{"ne same value", config.Rule{Path: "code", Op: config.OpNe, Value: json.Number("0.0")}, false},
		{"ne missing path", config.Rule{Path: "missing", Op: config.OpNe, Value: json.Number("0")}, true},
		{"in matches one element", config.Rule{Path: "code", Op: config.OpIn, Value: []any{json.Number("1"), json.Number("0")}}, true},
		{"in no element", config.Rule{Path: "code", Op: config.OpIn, Value: []any{json.Number("1")}}, false},
		{"in type differs", config.Rule{Path: "code", Op: config.OpIn, Value: []any{"0"}}, false},
		{"in missing path", config.Rule{Path: "missing", Op: config.OpIn, Value: []any{nil}}, false},
		{"in value not an array", config.Rule{Path: "code", Op: config.OpIn, Value: json.Number("0")}, false},
		{"exists", config.Rule{Path: "code", Op: config.OpExists}, true},
		{"exists null", config.Rule{Path: "none", Op: config.OpExists}, true},
		{"exists missing", config.Rule{Path: "missing", Op: config.OpExists}, false},
		{"not_exists missing", config.Rule{Path: "missing", Op: config.OpNotExists}, true},
		{"not_exists present", config.Rule{Path: "none", Op: config.OpNotExists}, false},
		{"unknown operator", config.Rule{Path: "code", Op: "gt", Value: json.Number("0")}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Eval(tt.rule, doc); got != tt.want {
				t.Errorf("Eval(%+v) = %v, want %v", tt.rule, got, tt.want)
			}
		})
	}
}

func TestEqualNumberEdgeCases(t *testing.T) {
	tests := []struct {
		name string
		a, b any
		want bool
	}{
		{"large integers compared exactly", json.Number("9007199254740993"), json.Number("9007199254740992"), false},
		{"int64 and json.Number", int64(7), json.Number("7.0"), true},
		{"invalid json.Number never equal", json.Number("x"), json.Number("x"), false},
		{"float64 by shortest decimal form", float64(0.1), json.Number("0.1"), true},
		{"NaN never equal", math.NaN(), math.NaN(), false},
		{"huge exponent never equal", json.Number("1e999999"), json.Number("1e999999"), false},
		{"exponent at the limit compares", json.Number("1e400"), json.Number("10e399"), true},
		{"over-long literal never equal", json.Number(strings.Repeat("1", 65)), json.Number(strings.Repeat("1", 65)), false},
		{"malformed exponent never equal", json.Number("1ex"), json.Number("1ex"), false},
		{"arrays with a different element", []any{"a", "b"}, []any{"a", "c"}, false},
		{"objects with different sizes", map[string]any{"k": "v"}, map[string]any{"k": "v", "x": "y"}, false},
		{"unsupported type never equal", struct{}{}, struct{}{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Equal(tt.a, tt.b); got != tt.want {
				t.Errorf("Equal(%#v, %#v) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestEvalAll(t *testing.T) {
	doc := decode(t, `{"code":0,"msg":"ok"}`)
	pass := config.Rule{Path: "code", Op: config.OpEq, Value: json.Number("0")}
	fail := config.Rule{Path: "msg", Op: config.OpEq, Value: "error"}

	if !EvalAll(nil, doc) {
		t.Error("EvalAll(no rules) = false, want true")
	}
	if !EvalAll([]config.Rule{pass}, doc) {
		t.Error("EvalAll(pass) = false, want true")
	}
	if EvalAll([]config.Rule{pass, fail}, doc) {
		t.Error("EvalAll(pass, fail) = true, want false")
	}
}

func TestRouteMatchRoots(t *testing.T) {
	// Route.match paths start at payload. or metadata. (§4, §7.2).
	doc := map[string]any{
		"payload":  decode(t, `{"order":{"amount":100}}`),
		"metadata": decode(t, `{"region":"cn"}`),
	}
	rules := []config.Rule{
		{Path: "payload.order.amount", Op: config.OpEq, Value: json.Number("100.0")},
		{Path: "metadata.region", Op: config.OpIn, Value: []any{"cn", "sg"}},
	}
	if !EvalAll(rules, doc) {
		t.Error("EvalAll on payload/metadata roots = false, want true")
	}
}

// TestSuccessGoldenBodyRules cross-checks body rule evaluation against the
// success golden cases: a case expected to succeed must satisfy its body
// rules, and a case whose status satisfies success.status but is not a
// success must fail them.
func TestSuccessGoldenBodyRules(t *testing.T) {
	cases, err := contracts.LoadSuccessCases(successCasesPath)
	if err != nil {
		t.Fatalf("LoadSuccessCases: %v", err)
	}
	checked := 0
	for _, c := range cases {
		if c.Response == nil || len(c.Success.Body) == 0 {
			continue
		}
		t.Run(c.Name, func(t *testing.T) {
			bodyOK := bodySatisfied(c.Success.Body, c.Response.Body)
			switch {
			case c.WantOutcome == domain.OutcomeSuccess && !bodyOK:
				t.Errorf("want success but body rules are not satisfied")
			case c.WantOutcome != domain.OutcomeSuccess && statusMatches(c.Success.Status, c.Response.Status) && bodyOK:
				t.Errorf("want %s with a matching status, but body rules are satisfied", c.WantOutcome)
			}
		})
		checked++
	}
	if checked == 0 {
		t.Fatal("no golden case has body rules")
	}
}

func bodySatisfied(raw []contracts.BodyRule, body string) bool {
	dec := json.NewDecoder(bytes.NewReader([]byte(body)))
	dec.UseNumber()
	var doc any
	if err := dec.Decode(&doc); err != nil || dec.More() {
		return false
	}
	rules := make([]config.Rule, len(raw))
	for i, r := range raw {
		rules[i] = config.Rule{Path: r.Path, Op: config.RuleOp(r.Op), Value: r.Value}
	}
	return EvalAll(rules, doc)
}

func statusMatches(patterns []contracts.StatusPattern, status int) bool {
	code := strconv.Itoa(status)
	for _, p := range patterns {
		s := string(p)
		if s == code || (strings.HasSuffix(s, "xx") && s[0] == code[0]) {
			return true
		}
	}
	return false
}
