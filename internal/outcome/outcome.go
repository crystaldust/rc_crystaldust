// Package outcome classifies the result of one delivery attempt
// (docs/design/domain-model.md §7.2, §7.3, §13.3). Classification is pure:
// whether a retryable outcome still has budget is the caller's decision.
// It depends on config, rules and domain.
package outcome

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"

	"messagehub/internal/config"
	"messagehub/internal/domain"
	"messagehub/internal/rules"
)

// Input is everything known about one attempt. At most one of the error
// fields is set; StatusCode, Body and BodyComplete describe a response
// when none is.
type Input struct {
	RenderErr    error
	InfraErr     error
	NetErr       error
	StatusCode   int
	ContentType  string
	Body         []byte
	BodyComplete bool // false when the body exceeded the read limit
}

// Classify applies §7.3 in order: render error, infrastructure error,
// network error or timeout, success, retry_on, otherwise permanent.
// Content-Type is never consulted.
func Classify(in Input, policy config.Policy) domain.Outcome {
	switch {
	case in.RenderErr != nil:
		return domain.OutcomeRenderError
	case in.InfraErr != nil:
		return domain.OutcomeInfraError
	case in.NetErr != nil:
		return domain.OutcomeRetryable
	case isSuccess(in, policy.Success):
		return domain.OutcomeSuccess
	case matchesAny(policy.Retry.RetryOn.Status, in.StatusCode):
		return domain.OutcomeRetryable
	default:
		return domain.OutcomePermanent
	}
}

func isSuccess(in Input, s config.Success) bool {
	if !matchesAny(s.Status, in.StatusCode) {
		return false
	}
	if len(s.Body) == 0 {
		return true
	}
	if !in.BodyComplete {
		return false
	}
	doc, err := decodeJSON(in.Body)
	if err != nil {
		return false
	}
	return rules.EvalAll(s.Body, doc)
}

func matchesAny(matchers []config.StatusMatcher, code int) bool {
	for _, m := range matchers {
		if m.Matches(code) {
			return true
		}
	}
	return false
}

var errTrailingData = errors.New("trailing data after JSON value")

// decodeJSON decodes exactly one JSON value, keeping numbers exact. An
// empty body is not JSON (§13.3).
func decodeJSON(b []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errTrailingData
	}
	return v, nil
}
