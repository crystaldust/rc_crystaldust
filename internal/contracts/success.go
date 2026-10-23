package contracts

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"

	"go.yaml.in/yaml/v3"

	"messagehub/internal/domain"
)

// StatusPattern is a status list element: an exact code ("200") or a class ("2xx").
type StatusPattern string

var statusClassPattern = regexp.MustCompile(`^[1-5]xx$`)

const (
	minStatusCode = 100
	maxStatusCode = 599
)

// UnmarshalYAML accepts an integer 100–599 or a "Nxx" class string (§11).
func (p *StatusPattern) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.ScalarNode {
		return fmt.Errorf("line %d: status must be a scalar", n.Line)
	}
	if n.ShortTag() == "!!int" {
		code, err := strconv.Atoi(n.Value)
		if err != nil || !isStatusCode(code) {
			return fmt.Errorf("line %d: status %q: want 100-599", n.Line, n.Value)
		}
		*p = StatusPattern(n.Value)
		return nil
	}
	if !statusClassPattern.MatchString(n.Value) {
		return fmt.Errorf("line %d: status %q: want an integer or a class like \"2xx\"", n.Line, n.Value)
	}
	*p = StatusPattern(n.Value)
	return nil
}

// Op is a body rule operator (§7.2).
type Op string

const (
	OpEq        Op = "eq"
	OpNe        Op = "ne"
	OpIn        Op = "in"
	OpExists    Op = "exists"
	OpNotExists Op = "not_exists"
)

// Ops lists every body rule operator.
var Ops = []Op{OpEq, OpNe, OpIn, OpExists, OpNotExists}

// NetworkError is a transport failure that happened before any response.
type NetworkError string

const (
	NetworkConnect NetworkError = "connect"
	NetworkDNS     NetworkError = "dns"
	NetworkTLS     NetworkError = "tls"
	NetworkTimeout NetworkError = "timeout"
)

// NetworkErrors lists every network error kind.
var NetworkErrors = []NetworkError{NetworkConnect, NetworkDNS, NetworkTLS, NetworkTimeout}

// BodyRule is one `success.body` rule. Value is meaningful only when HasValue.
type BodyRule struct {
	Path     string
	Op       Op
	Value    any
	HasValue bool
}

// SuccessSpec mirrors `policy.success`.
type SuccessSpec struct {
	Status []StatusPattern
	Body   []BodyRule
}

// RetryOn mirrors `policy.retry.retry_on`.
type RetryOn struct {
	Status []StatusPattern `yaml:"status"`
}

// Response is the HTTP response the vendor returned; Body is the raw text.
type Response struct {
	Status      int    `yaml:"status"`
	ContentType string `yaml:"content_type"`
	Body        string `yaml:"body"`
}

// SuccessCase is one golden case for success evaluation and classification.
type SuccessCase struct {
	Name         string
	Success      SuccessSpec
	RetryOn      RetryOn
	Response     *Response    // nil when NetworkError is set
	NetworkError NetworkError // empty when Response is set
	WantOutcome  domain.Outcome
}

type rawBodyRule struct {
	Path  string    `yaml:"path"`
	Op    Op        `yaml:"op"`
	Value yaml.Node `yaml:"value"`
}

type rawSuccessSpec struct {
	Status []StatusPattern `yaml:"status"`
	Body   []rawBodyRule   `yaml:"body"`
}

type rawSuccessCase struct {
	Name         string         `yaml:"name"`
	Success      rawSuccessSpec `yaml:"success"`
	RetryOn      RetryOn        `yaml:"retry_on"`
	Response     *Response      `yaml:"response"`
	NetworkError NetworkError   `yaml:"network_error"`
	WantOutcome  domain.Outcome `yaml:"want_outcome"`
}

func (r rawSuccessCase) caseName() string { return r.Name }

// LoadSuccessCases loads and validates success-evaluation golden cases.
func LoadSuccessCases(path string) ([]SuccessCase, error) {
	return loadCases(path, convertSuccessCase)
}

func convertSuccessCase(r rawSuccessCase) (SuccessCase, error) {
	if err := validateSuccessShape(r); err != nil {
		return SuccessCase{}, err
	}
	rules := make([]BodyRule, 0, len(r.Success.Body))
	for i, raw := range r.Success.Body {
		rule, err := convertBodyRule(raw)
		if err != nil {
			return SuccessCase{}, fmt.Errorf("success.body[%d]: %w", i, err)
		}
		rules = append(rules, rule)
	}
	return SuccessCase{
		Name:         r.Name,
		Success:      SuccessSpec{Status: r.Success.Status, Body: rules},
		RetryOn:      r.RetryOn,
		Response:     r.Response,
		NetworkError: r.NetworkError,
		WantOutcome:  r.WantOutcome,
	}, nil
}

func validateSuccessShape(r rawSuccessCase) error {
	if err := requireName(r.Name); err != nil {
		return err
	}
	if len(r.Success.Status) == 0 {
		return errors.New("success.status must not be empty")
	}
	if (r.Response == nil) == (r.NetworkError == "") {
		return errors.New("exactly one of response and network_error is required")
	}
	if r.Response != nil && !isStatusCode(r.Response.Status) {
		return fmt.Errorf("response.status %d: want 100-599", r.Response.Status)
	}
	if r.NetworkError != "" && !contains(NetworkErrors, r.NetworkError) {
		return fmt.Errorf("network_error %q: want connect, dns, tls or timeout", r.NetworkError)
	}
	switch r.WantOutcome {
	case domain.OutcomeSuccess, domain.OutcomeRetryable, domain.OutcomePermanent:
	default:
		return fmt.Errorf("want_outcome %q: want success, retryable or permanent", r.WantOutcome)
	}
	return nil
}

func convertBodyRule(r rawBodyRule) (BodyRule, error) {
	if r.Path == "" {
		return BodyRule{}, errors.New("path is required")
	}
	if !contains(Ops, r.Op) {
		return BodyRule{}, fmt.Errorf("op %q: unknown operator", r.Op)
	}
	hasValue := isSet(r.Value)
	switch {
	case (r.Op == OpExists || r.Op == OpNotExists) && hasValue:
		return BodyRule{}, fmt.Errorf("op %s takes no value", r.Op)
	case (r.Op == OpEq || r.Op == OpNe || r.Op == OpIn) && !hasValue:
		return BodyRule{}, fmt.Errorf("op %s requires a value", r.Op)
	case r.Op == OpIn && r.Value.Kind != yaml.SequenceNode:
		return BodyRule{}, errors.New("op in requires an array value")
	}
	var value any
	if hasValue {
		v, err := jsonValue(r.Value)
		if err != nil {
			return BodyRule{}, fmt.Errorf("value: %w", err)
		}
		value = v
	}
	return BodyRule{Path: r.Path, Op: r.Op, Value: value, HasValue: hasValue}, nil
}

func isStatusCode(code int) bool { return code >= minStatusCode && code <= maxStatusCode }

func contains[T comparable](list []T, v T) bool {
	for _, item := range list {
		if item == v {
			return true
		}
	}
	return false
}
