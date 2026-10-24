package secrets

import (
	"errors"
	"strings"
	"testing"

	"messagehub/internal/config"
)

const exampleConfig = "../../config/examples/hub.example.yaml"

func lookupFrom(env map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		v, ok := env[name]
		return v, ok
	}
}

func TestEnvResolverResolve(t *testing.T) {
	r := NewEnvResolver(lookupFrom(map[string]string{
		"SET_VAR":   "abc123",
		"EMPTY_VAR": "",
	}))
	tests := []struct {
		name    string
		ref     string
		want    string
		wantErr error
	}{
		{"set variable", "env:SET_VAR", "abc123", nil},
		{"unset variable", "env:MISSING_VAR", "", ErrNotSet},
		{"empty variable counts as unset", "env:EMPTY_VAR", "", ErrNotSet},
		{"empty name", "env:", "", ErrNotSet},
		{"unsupported scheme", "vault:kv/hub#token", "", ErrUnsupportedScheme},
		{"no scheme", "SET_VAR", "", ErrUnsupportedScheme},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got, err := r.Resolve(tt.ref)

			// Assert
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Resolve(%q) error = %v, want %v", tt.ref, err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("Resolve(%q) = %q, want %q", tt.ref, got, tt.want)
			}
		})
	}
}

func TestResolveErrorNeverContainsValue(t *testing.T) {
	r := NewEnvResolver(lookupFrom(map[string]string{"EMPTY_VAR": ""}))
	_, err := r.Resolve("env:EMPTY_VAR")
	if err == nil || !strings.Contains(err.Error(), "EMPTY_VAR") {
		t.Fatalf("error = %v, want it to name EMPTY_VAR", err)
	}
}

func loadExample(t *testing.T) *config.Config {
	t.Helper()
	cfg, err := config.Load(exampleConfig)
	if err != nil {
		t.Fatalf("Load(%q) error: %v", exampleConfig, err)
	}
	return cfg
}

func TestCheckRequiredListsEveryMissingVariable(t *testing.T) {
	// Arrange: set every required variable except three.
	cfg := loadExample(t)
	required := cfg.RequiredEnv()
	missing := map[string]bool{required[0]: true, required[3]: true, required[len(required)-1]: true}
	env := make(map[string]string)
	for _, name := range required {
		if !missing[name] {
			env[name] = "fake-secret-value"
		}
	}

	// Act
	err := CheckRequired(cfg, NewEnvResolver(lookupFrom(env)))

	// Assert
	var merr *MissingError
	if !errors.As(err, &merr) {
		t.Fatalf("error = %v, want *MissingError", err)
	}
	if len(merr.Names) != len(missing) {
		t.Fatalf("missing names = %v, want %d names", merr.Names, len(missing))
	}
	for _, name := range merr.Names {
		if !missing[name] {
			t.Errorf("unexpected missing name %q", name)
		}
	}
	if strings.Contains(err.Error(), "fake-secret-value") {
		t.Errorf("error %q leaks a secret value", err)
	}
}

func TestCheckRequiredPassesWhenAllSet(t *testing.T) {
	cfg := loadExample(t)
	env := make(map[string]string)
	for _, name := range cfg.RequiredEnv() {
		env[name] = "fake-secret-value"
	}

	if err := CheckRequired(cfg, NewEnvResolver(lookupFrom(env))); err != nil {
		t.Fatalf("CheckRequired error: %v", err)
	}
}

func TestCheckRequiredReportsResolverFailures(t *testing.T) {
	cfg := loadExample(t)

	err := CheckRequired(cfg, failingResolver{})

	if err == nil || errors.As(err, new(*MissingError)) {
		t.Fatalf("error = %v, want a non-MissingError failure", err)
	}
}

type failingResolver struct{}

func (failingResolver) Resolve(string) (string, error) { return "", ErrUnsupportedScheme }
