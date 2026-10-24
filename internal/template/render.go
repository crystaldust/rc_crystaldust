// Package template implements the request template language (ADR-0006,
// docs/design/domain-model.md §7.1, §13.2): parsing, static validation for
// config loading, and rendering of a Destination request. Every render
// yields two results from one evaluation: the actual request, and a masked
// copy in which secret-bearing expressions are replaced by domain.MaskedValue
// (§10). It depends on config (request shape) and domain only.
package template

import (
	"errors"
	"fmt"
	"maps"
	"net/url"
	"slices"
	"strings"
	"time"

	"messagehub/internal/config"
	"messagehub/internal/domain"
)

// Location is where a template appears in a request (§7.1).
type Location string

const (
	LocationBody   Location = "body"
	LocationHeader Location = "header"
	LocationURL    Location = "url"
)

// ErrorClass is a render error category (§7.1); values match the golden cases.
type ErrorClass string

const (
	ErrorSyntax          ErrorClass = "syntax"
	ErrorMissingPath     ErrorClass = "missing_path"
	ErrorUnknownFunction ErrorClass = "unknown_function"
	ErrorBadArgument     ErrorClass = "bad_argument"
)

// Error is a template failure. Detail names paths and functions, never values.
type Error struct {
	Class  ErrorClass
	Detail string
}

func (e *Error) Error() string { return string(e.Class) + ": " + e.Detail }

// ClassOf returns the class of a template error anywhere in err's chain.
func ClassOf(err error) (ErrorClass, bool) {
	var te *Error
	if errors.As(err, &te) {
		return te.Class, true
	}
	return "", false
}

func badArgumentf(format string, args ...any) *Error {
	return &Error{Class: ErrorBadArgument, Detail: fmt.Sprintf(format, args...)}
}

// Event is the accepted event a template renders against. Empty optional
// strings mean "absent".
type Event struct {
	ID             string
	Source         string
	SourceEventID  string
	EventType      string
	OccurredAt     time.Time
	ReceivedAt     time.Time
	IdempotencyKey string
	OrderingKey    string
	Payload        map[string]any
	Metadata       map[string]string
}

// Vars is the render context. Now is injected by the caller so renders are
// deterministic in tests.
type Vars struct {
	Event      Event
	Secrets    map[string]string
	DeliveryID string
	Attempt    int
	Now        time.Time
}

// Text is a rendered string and its masked form.
type Text struct {
	Actual, Masked string
}

// Value is a rendered body and its masked form.
type Value struct {
	Actual, Masked any
}

// Request is one rendered request. BodyBytes is nil when the destination
// has no body.
type Request struct {
	URL       string
	Headers   map[string]string
	Body      any
	BodyBytes []byte
}

// Rendered holds the request to send and the masked copy to record.
type Rendered struct {
	Actual, Masked Request
}

// Render renders a destination request in the order URL → Body → Headers;
// headers see the actual body bytes as request.body.
func Render(req config.Request, v Vars) (Rendered, error) {
	u, err := RenderURL(req.URL, v)
	if err != nil {
		return Rendered{}, fmt.Errorf("render url: %w", err)
	}
	actual := Request{URL: u.Actual, Headers: map[string]string{}}
	masked := Request{URL: u.Masked, Headers: map[string]string{}}
	if req.Body != nil {
		body, err := RenderBody(req.Body, v)
		if err != nil {
			return Rendered{}, fmt.Errorf("render body: %w", err)
		}
		if actual.BodyBytes, err = MarshalJSON(body.Actual); err != nil {
			return Rendered{}, fmt.Errorf("render body: %w", err)
		}
		if masked.BodyBytes, err = MarshalJSON(body.Masked); err != nil {
			return Rendered{}, fmt.Errorf("render body: %w", err)
		}
		actual.Body, masked.Body = body.Actual, body.Masked
	}
	for _, name := range sortedNames(req.Headers) {
		h, err := RenderHeader(req.Headers[name], v, actual.BodyBytes)
		if err != nil {
			return Rendered{}, fmt.Errorf("render header %s: %w", name, err)
		}
		actual.Headers[name], masked.Headers[name] = h.Actual, h.Masked
	}
	return Rendered{Actual: actual, Masked: masked}, nil
}

// RenderURL renders a URL template: expression output is path-segment
// escaped before the first literal '?' and query escaped after it (§13.2).
func RenderURL(tmpl string, v Vars) (Text, error) {
	p, err := parse(tmpl, LocationURL)
	if err != nil {
		return Text{}, err
	}
	en := &env{vars: v}
	var actual, masked strings.Builder
	for _, pt := range p.parts {
		if pt.expr == nil {
			actual.WriteString(pt.literal)
			masked.WriteString(pt.literal)
			continue
		}
		s, err := evalText(pt.expr, en)
		if err != nil {
			return Text{}, err
		}
		escaped := url.PathEscape(s)
		if pt.inQuery {
			escaped = url.QueryEscape(s)
		}
		actual.WriteString(escaped)
		if pt.expr.carriesSecret() {
			escaped = domain.MaskedValue
		}
		masked.WriteString(escaped)
	}
	return Text{Actual: actual.String(), Masked: masked.String()}, nil
}

// RenderHeader renders a header value; requestBody is what request.body
// resolves to. A value containing a secret expression is masked whole.
func RenderHeader(tmpl string, v Vars, requestBody []byte) (Text, error) {
	p, err := parse(tmpl, LocationHeader)
	if err != nil {
		return Text{}, err
	}
	s, err := concat(p, &env{vars: v, requestBody: string(requestBody)})
	if err != nil {
		return Text{}, err
	}
	return Text{Actual: s, Masked: maskIf(p.carriesSecret(), s)}, nil
}

// RenderBody renders a body template (any decoded JSON value). A leaf that
// is exactly one expression keeps the JSON type of its result.
func RenderBody(tmpl any, v Vars) (Value, error) {
	return renderBodyValue(tmpl, &env{vars: v}, 0)
}

func renderBodyValue(tmpl any, en *env, depth int) (Value, error) {
	if depth > maxBodyDepth {
		return Value{}, syntaxErrorf("body nests deeper than %d levels", maxBodyDepth)
	}
	switch t := tmpl.(type) {
	case string:
		return renderBodyLeaf(t, en)
	case map[string]any:
		actual, masked := make(map[string]any, len(t)), make(map[string]any, len(t))
		for _, k := range sortedNames(t) {
			child, err := renderBodyValue(t[k], en, depth+1)
			if err != nil {
				return Value{}, err
			}
			actual[k], masked[k] = child.Actual, child.Masked
		}
		return Value{Actual: actual, Masked: masked}, nil
	case []any:
		actual, masked := make([]any, len(t)), make([]any, len(t))
		for i, item := range t {
			child, err := renderBodyValue(item, en, depth+1)
			if err != nil {
				return Value{}, err
			}
			actual[i], masked[i] = child.Actual, child.Masked
		}
		return Value{Actual: actual, Masked: masked}, nil
	default: // number, bool, null pass through
		return Value{Actual: t, Masked: t}, nil
	}
}

func renderBodyLeaf(s string, en *env) (Value, error) {
	p, err := parse(s, LocationBody)
	if err != nil {
		return Value{}, err
	}
	if !p.hasExpr() {
		return Value{Actual: s, Masked: s}, nil
	}
	var out any
	if e, ok := p.single(); ok {
		v, err := e.eval(en)
		if err != nil {
			return Value{}, err
		}
		out = toJSONValue(v)
	} else if out, err = concat(p, en); err != nil {
		return Value{}, err
	}
	if p.carriesSecret() {
		return Value{Actual: out, Masked: domain.MaskedValue}, nil
	}
	return Value{Actual: out, Masked: out}, nil
}

func concat(p parsed, en *env) (string, error) {
	var b strings.Builder
	for _, pt := range p.parts {
		if pt.expr == nil {
			b.WriteString(pt.literal)
			continue
		}
		s, err := evalText(pt.expr, en)
		if err != nil {
			return "", err
		}
		b.WriteString(s)
	}
	return b.String(), nil
}

func evalText(e *expr, en *env) (string, error) {
	v, err := e.eval(en)
	if err != nil {
		return "", err
	}
	return toText(v)
}

func maskIf(secret bool, s string) string {
	if secret {
		return domain.MaskedValue
	}
	return s
}

// ValidateField statically checks one template string from the config
// (syntax, functions, arguments, path roots) without evaluating it. The
// location is inferred from the field path, e.g.
// "destinations[0].request.headers.X-Signature". It has the signature of
// config.TemplateValidator.
func ValidateField(field, tmpl string) error {
	_, err := parse(tmpl, locationOf(field))
	return err
}

func locationOf(field string) Location {
	_, rest, _ := strings.Cut(field, ".request")
	switch {
	case strings.HasPrefix(rest, ".url"):
		return LocationURL
	case strings.HasPrefix(rest, ".headers"):
		return LocationHeader
	default:
		return LocationBody
	}
}

func sortedNames[V any](m map[string]V) []string {
	return slices.Sorted(maps.Keys(m))
}
