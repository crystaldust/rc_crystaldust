package outcome

import (
	"encoding/json"
	"errors"
	"testing"

	"messagehub/internal/config"
	"messagehub/internal/contracts"
	"messagehub/internal/domain"
)

func TestGoldenSuccessCases(t *testing.T) {
	cases, err := contracts.LoadSuccessCases(contracts.SuccessCasesPath())
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			// Arrange
			policy := config.Policy{
				Success: config.Success{Status: matchers(t, c.Success.Status), Body: configRules(c.Success.Body)},
				Retry:   config.Retry{RetryOn: config.RetryOn{Status: matchers(t, c.RetryOn.Status)}},
			}
			in := Input{NetErr: networkErr(c.NetworkError)}
			if c.Response != nil {
				in = Input{
					StatusCode: c.Response.Status, ContentType: c.Response.ContentType,
					Body: []byte(c.Response.Body), BodyComplete: true,
				}
			}

			// Act
			got := Classify(in, policy)

			// Assert
			if got != c.WantOutcome {
				t.Fatalf("outcome = %s, want %s", got, c.WantOutcome)
			}
		})
	}
}

func TestClassifyPrecedence(t *testing.T) {
	ok := config.Policy{Success: config.Success{Status: []config.StatusMatcher{{Class: 2}}}}
	boom := errors.New("boom")
	tests := []struct {
		name string
		in   Input
		want domain.Outcome
	}{
		{"render error wins over everything", Input{RenderErr: boom, InfraErr: boom, StatusCode: 200}, domain.OutcomeRenderError},
		{"infra error wins over network error", Input{InfraErr: boom, NetErr: boom}, domain.OutcomeInfraError},
		{"network error wins over a status", Input{NetErr: boom, StatusCode: 200}, domain.OutcomeRetryable},
		{"plain 2xx", Input{StatusCode: 204}, domain.OutcomeSuccess},
		{"401 is permanent by default", Input{StatusCode: 401}, domain.OutcomePermanent},
		{"403 is permanent by default", Input{StatusCode: 403}, domain.OutcomePermanent},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got := Classify(tt.in, ok)

			// Assert
			if got != tt.want {
				t.Fatalf("outcome = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestBodyRulesNeedCompleteSingleJSONValue(t *testing.T) {
	policy := config.Policy{Success: config.Success{
		Status: []config.StatusMatcher{{Code: 200}},
		Body:   []config.Rule{{Path: "code", Op: config.OpEq, Value: json.Number("0")}},
	}}
	tests := []struct {
		name     string
		body     string
		complete bool
		want     domain.Outcome
	}{
		{"complete json", `{"code":0}`, true, domain.OutcomeSuccess},
		{"cut off at the read limit", `{"code":0}`, false, domain.OutcomePermanent},
		{"trailing data", `{"code":0} {"code":1}`, true, domain.OutcomePermanent},
		{"trailing whitespace is fine", "{\"code\":0}\n", true, domain.OutcomeSuccess},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got := Classify(Input{StatusCode: 200, Body: []byte(tt.body), BodyComplete: tt.complete}, policy)

			// Assert
			if got != tt.want {
				t.Fatalf("outcome = %s, want %s", got, tt.want)
			}
		})
	}
}

func networkErr(kind contracts.NetworkError) error {
	if kind == "" {
		return nil
	}
	return errors.New(string(kind))
}

// matchers converts golden status patterns through the config decoder, so
// the test exercises the same StatusMatcher parsing as a loaded config.
func matchers(t *testing.T, patterns []contracts.StatusPattern) []config.StatusMatcher {
	t.Helper()
	out := make([]config.StatusMatcher, 0, len(patterns))
	for _, p := range patterns {
		raw := string(p)
		if !isDigits(raw) {
			raw = `"` + raw + `"`
		}
		var m config.StatusMatcher
		if err := json.Unmarshal([]byte(raw), &m); err != nil {
			t.Fatalf("status %q: %v", p, err)
		}
		out = append(out, m)
	}
	return out
}

func isDigits(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return s != ""
}

func configRules(in []contracts.BodyRule) []config.Rule {
	out := make([]config.Rule, 0, len(in))
	for _, r := range in {
		out = append(out, config.Rule{Path: r.Path, Op: config.RuleOp(r.Op), Value: r.Value})
	}
	return out
}
