package template

import (
	"slices"
	"strings"
)

// Limits that keep a pathological configuration from exhausting the worker.
const (
	maxTemplateBytes = 64 * 1024
	maxPipelineCalls = 16
	maxBodyDepth     = 32
)

// Path roots (§7.1).
const (
	rootEvent    = "event"
	rootSecret   = "secret"
	rootDelivery = "delivery"
	rootNow      = "now"
	rootRequest  = "request"
)

var (
	eventFields = []string{
		"id", "source", "source_event_id", "event_type", "occurred_at",
		"received_at", "idempotency_key", "ordering_key",
	}
	eventContainers = []string{"payload", "metadata"}
	deliveryFields  = []string{"id", "attempt"}
)

// part is either a literal run of text or one expression.
type part struct {
	literal string
	expr    *expr
	inQuery bool // URL only: the expression follows a literal '?'
}

// expr is one parsed {{ path | func arg … }}.
type expr struct {
	path  []string
	calls []call
}

// carriesSecret reports whether the expression may output a secret (§10):
// a secret.* path root (a secret used as an hmac key does not count), or
// request.body copied without a one-way digest, since the body may hold
// secrets. request.body | hmac_* or | sha256 outputs a digest and is kept.
func (e *expr) carriesSecret() bool {
	switch e.path[0] {
	case rootSecret:
		return true
	case rootRequest:
		return len(e.calls) == 0 || !digestFunctions[e.calls[len(e.calls)-1].name]
	default:
		return false
	}
}

// digestFunctions output a one-way hash of their input.
var digestFunctions = map[string]bool{"sha256": true, "hmac_sha256_hex": true, "hmac_sha256_base64": true}

type call struct {
	name string
	fn   function
	arg  any // decoded argument, see argKind
}

// parsed is a statically checked template string.
type parsed struct {
	parts []part
}

func (p parsed) carriesSecret() bool {
	for _, pt := range p.parts {
		if pt.expr != nil && pt.expr.carriesSecret() {
			return true
		}
	}
	return false
}

// single returns the expression when the whole string is exactly one
// expression, ignoring surrounding whitespace.
func (p parsed) single() (*expr, bool) {
	var found *expr
	for _, pt := range p.parts {
		switch {
		case pt.expr != nil && found != nil:
			return nil, false
		case pt.expr != nil:
			found = pt.expr
		case strings.TrimSpace(pt.literal) != "":
			return nil, false
		}
	}
	return found, found != nil
}

func (p parsed) hasExpr() bool {
	return slices.ContainsFunc(p.parts, func(pt part) bool { return pt.expr != nil })
}

// parse splits a template string into parts and checks every expression
// for syntax, path roots allowed at loc, function names and arguments.
func parse(s string, loc Location) (parsed, error) {
	if len(s) > maxTemplateBytes {
		return parsed{}, syntaxErrorf("template longer than %d bytes", maxTemplateBytes)
	}
	var parts []part
	inQuery := false
	for {
		i := strings.Index(s, openDelim)
		if i < 0 {
			return parsed{parts: appendLiteral(parts, s)}, nil
		}
		parts = appendLiteral(parts, s[:i])
		if loc == LocationURL && strings.Contains(s[:i], "?") {
			inQuery = true
		}
		rest := s[i+len(openDelim):]
		end := findClose(rest)
		if end < 0 {
			return parsed{}, syntaxErrorf("unterminated expression")
		}
		e, err := parseExpr(rest[:end], loc)
		if err != nil {
			return parsed{}, err
		}
		parts = append(parts, part{expr: e, inQuery: inQuery})
		s = rest[end+len(closeDelim):]
	}
}

func appendLiteral(parts []part, s string) []part {
	if s == "" {
		return parts
	}
	return append(parts, part{literal: s})
}

func parseExpr(src string, loc Location) (*expr, error) {
	tokens, err := lexExpr(src)
	if err != nil {
		return nil, err
	}
	segments := splitPipes(tokens)
	if len(segments)-1 > maxPipelineCalls {
		return nil, syntaxErrorf("more than %d functions in one expression", maxPipelineCalls)
	}
	head := segments[0]
	if len(head) != 1 || (head[0].kind != tokIdent && head[0].kind != tokPath) {
		return nil, syntaxErrorf("expression must start with a path")
	}
	for _, seg := range segments[1:] {
		if len(seg) == 0 || seg[0].kind != tokIdent {
			return nil, syntaxErrorf("expected a function name after '|'")
		}
	}
	path, err := checkPath(head[0].text, loc)
	if err != nil {
		return nil, err
	}
	calls := make([]call, 0, len(segments)-1)
	for _, seg := range segments[1:] {
		c, err := checkCall(seg[0].text, seg[1:])
		if err != nil {
			return nil, err
		}
		calls = append(calls, c)
	}
	return &expr{path: path, calls: calls}, nil
}

// splitPipes groups tokens between '|' separators; an empty expression
// yields one empty group.
func splitPipes(tokens []token) [][]token {
	segments := [][]token{nil}
	for _, t := range tokens {
		if t.kind == tokPipe {
			segments = append(segments, nil)
			continue
		}
		last := len(segments) - 1
		segments[last] = append(segments[last], t)
	}
	return segments
}

// checkPath validates a path against the root table in §7.1.
func checkPath(text string, loc Location) ([]string, error) {
	segs := strings.Split(text, ".")
	if slices.Contains(segs, "") {
		return nil, syntaxErrorf("malformed path %q", text)
	}
	if !isKnownPath(segs) {
		return nil, syntaxErrorf("unknown path %q", text)
	}
	if segs[0] == rootRequest && loc != LocationHeader {
		return nil, syntaxErrorf("%q is only allowed in headers", text)
	}
	return segs, nil
}

func isKnownPath(segs []string) bool {
	switch segs[0] {
	case rootNow:
		return len(segs) == 1
	case rootSecret:
		return len(segs) == 2
	case rootDelivery:
		return len(segs) == 2 && slices.Contains(deliveryFields, segs[1])
	case rootRequest:
		return len(segs) == 2 && segs[1] == "body"
	case rootEvent:
		if len(segs) < 2 {
			return false
		}
		if slices.Contains(eventContainers, segs[1]) {
			return true
		}
		return len(segs) == 2 && slices.Contains(eventFields, segs[1])
	default:
		return false
	}
}

func checkCall(name string, args []token) (call, error) {
	fn, ok := functions[name]
	if !ok {
		return call{}, &Error{Class: ErrorUnknownFunction, Detail: "unknown function " + name}
	}
	arg, err := checkArg(name, fn.arg, args)
	if err != nil {
		return call{}, err
	}
	return call{name: name, fn: fn, arg: arg}, nil
}

// checkArg validates the arguments of one call and returns the decoded
// argument: a JSON literal, a layout name or a secret name.
func checkArg(name string, kind argKind, args []token) (any, error) {
	if kind == argNone {
		if len(args) != 0 {
			return nil, badArgumentf("%s takes no argument", name)
		}
		return nil, nil
	}
	if len(args) != 1 {
		return nil, badArgumentf("%s takes exactly one argument", name)
	}
	a := args[0]
	switch kind {
	case argLiteral:
		if v, ok := literalValue(a); ok {
			return v, nil
		}
		return nil, badArgumentf("%s needs a literal argument", name)
	case argLayout:
		if a.kind == tokIdent && slices.Contains(timeLayouts, a.text) {
			return a.text, nil
		}
		return nil, badArgumentf("%s layout must be one of %s", name, strings.Join(timeLayouts, ", "))
	default: // argSecret
		segs := strings.Split(a.text, ".")
		if a.kind == tokPath && len(segs) == 2 && segs[0] == rootSecret && segs[1] != "" {
			return segs[1], nil
		}
		return nil, badArgumentf("%s needs a secret.NAME argument", name)
	}
}

func literalValue(t token) (any, bool) {
	switch t.kind {
	case tokString, tokNumber:
		return t.value, true
	case tokIdent:
		switch t.text {
		case "true":
			return true, true
		case "false":
			return false, true
		case "null":
			return nil, true
		}
	}
	return nil, false
}
