package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"messagehub/internal/domain"
)

// invalidCases maps each testdata/invalid file to the problem it must report.
var invalidCases = map[string]struct {
	field  string
	reason string
}{
	"unknown_field":            {"destinations[0].priority", "unknown field"},
	"duplicate_id":             {"principals[1].id", `duplicate id "order-service"`},
	"dangling_destination":     {"routes[0].destinations[1]", `unknown destination "warehouse"`},
	"dangling_policy":          {"destinations[0].policy", `unknown policy "missing-policy"`},
	"source_not_source_role":   {"routes[0].sources[0]", `has role "viewer"`},
	"unknown_source_principal": {"routes[0].sources[0]", `unknown principal "ghost-service"`},
	"unsupported_yaml_value":   {rootField, "unsupported YAML value"},
	"bad_env_ref":              {"principals[1].api_key_sha256_ref", "does not match pattern"},
	"bad_duration":             {"policies[0].timeout", "does not match pattern"},
	"jitter_one":               {"policies[0].retry.backoff.jitter", "exclusiveMaximum"},
	"multiplier_below_one":     {"policies[0].retry.backoff.multiplier", "minimum"},
	"on_dead_with_mode_none":   {"policies[0].ordering.on_predecessor_dead", "not allowed"},
	"bad_event_type":           {"routes[0].event_type", "must match"},
	"timeout_not_below_max":    {"policies[0].timeout", "must be less than retry.backoff.max"},
	"status_6xx":               {"policies[0].success.status[1]", "'6xx' does not match pattern"},
	"bad_auth":                 {"destinations[0].auth.token_url", "unknown field"},
	"bad_id":                   {"destinations[0].id", "does not match pattern"},
	"max_attempts_too_high":    {"policies[0].retry.max_attempts", "maximum"},
	"missing_required":         {"destinations[0].request.method", "required field missing"},
	"rule_exists_with_value":   {"policies[0].success.body[0].value", "not allowed"},
	"multiple_semantic":        {"routes[0].destinations[0]", `unknown destination "ghost"`},
	"duplicate_key_ref":        {"principals[1].api_key_sha256_ref", "duplicate api_key_sha256_ref"},
	"empty":                    {rootField, "empty"},
	"malformed_yaml":           {rootField, "invalid YAML"},
	"two_documents":            {rootField, "exactly one YAML document"},
	"duration_overflow":        {rootField, "out of range"},
	"wrong_version":            {"version", "must be 1"},
}

func TestInvalidConfigs(t *testing.T) {
	files, err := filepath.Glob("testdata/invalid/*.yaml")
	if err != nil || len(files) == 0 {
		t.Fatalf("no invalid fixtures: %v", err)
	}
	for _, path := range files {
		name := strings.TrimSuffix(filepath.Base(path), ".yaml")
		t.Run(name, func(t *testing.T) {
			want, ok := invalidCases[name]
			if !ok {
				t.Fatalf("fixture %s has no expectation in invalidCases", name)
			}

			// Act
			_, err := Load(path)

			// Assert
			problems := validationProblems(t, err)
			if !hasProblem(problems, want.field, want.reason) {
				t.Fatalf("problems = %v, want field %q with reason containing %q", problems, want.field, want.reason)
			}
		})
	}
}

func TestInvalidCasesHaveFixtures(t *testing.T) {
	for name := range invalidCases {
		if _, err := os.Stat(filepath.Join("testdata", "invalid", name+".yaml")); err != nil {
			t.Errorf("expectation %q has no fixture file: %v", name, err)
		}
	}
}

func TestMultipleSemanticErrorsReportedTogether(t *testing.T) {
	// Act
	_, err := Load("testdata/invalid/multiple_semantic.yaml")

	// Assert
	problems := validationProblems(t, err)
	want := []string{"destinations[0].policy", "routes[0].event_type", "routes[0].destinations[0]"}
	if len(problems) != len(want) {
		t.Fatalf("got %d problems %v, want %d", len(problems), problems, len(want))
	}
	for _, f := range want {
		if !(&domain.ValidationError{Problems: problems}).HasField(f) {
			t.Errorf("missing problem for %s in %v", f, problems)
		}
	}
}

func TestLoadMissingFile(t *testing.T) {
	_, err := Load("testdata/does-not-exist.yaml")
	var verr *domain.ValidationError
	if err == nil || errors.As(err, &verr) {
		t.Fatalf("err = %v, want a non-validation I/O error", err)
	}
}

func validationProblems(t *testing.T, err error) []domain.FieldError {
	t.Helper()
	var verr *domain.ValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("err = %v (%T), want *domain.ValidationError", err, err)
	}
	return verr.Problems
}

func hasProblem(problems []domain.FieldError, field, reason string) bool {
	for _, p := range problems {
		if p.Field == field && strings.Contains(p.Reason, reason) {
			return true
		}
	}
	return false
}
