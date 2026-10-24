// Package secrets resolves secret references such as "env:NAME" to their
// values (ADR-0008 D3). It depends only on config; values never appear in
// errors, only reference names.
package secrets

import (
	"errors"
	"fmt"
	"strings"

	"messagehub/internal/config"
)

var (
	// ErrUnsupportedScheme is returned for a reference whose prefix has no
	// resolver yet; only config.EnvRefPrefix is supported today.
	ErrUnsupportedScheme = errors.New("unsupported secret reference scheme")
	// ErrNotSet is returned when the referenced value is missing or empty.
	ErrNotSet = errors.New("secret reference is not set")
)

// Resolver turns a reference ("env:NAME") into its value.
type Resolver interface {
	Resolve(ref string) (string, error)
}

// EnvResolver resolves "env:" references from the process environment.
type EnvResolver struct {
	lookup func(string) (string, bool)
}

// NewEnvResolver returns a resolver reading through lookup (os.LookupEnv in production).
func NewEnvResolver(lookup func(string) (string, bool)) EnvResolver {
	return EnvResolver{lookup: lookup}
}

// Resolve returns the value of an "env:NAME" reference. An empty value
// counts as not set.
func (r EnvResolver) Resolve(ref string) (string, error) {
	name, ok := strings.CutPrefix(ref, config.EnvRefPrefix)
	if !ok {
		return "", fmt.Errorf("%w: %q", ErrUnsupportedScheme, schemeOf(ref))
	}
	v, ok := r.lookup(name)
	if !ok || v == "" || name == "" {
		return "", fmt.Errorf("%w: %s", ErrNotSet, ref)
	}
	return v, nil
}

// schemeOf returns the part before the first ":" so errors never echo a
// whole reference that might have been mistyped with a value in it.
func schemeOf(ref string) string {
	scheme, _, found := strings.Cut(ref, ":")
	if !found {
		return ""
	}
	return scheme
}

// MissingError lists every required environment variable that is not set.
type MissingError struct {
	Names []string
}

func (e *MissingError) Error() string {
	return "missing environment variables: " + strings.Join(e.Names, ", ")
}

// CheckRequired resolves every variable cfg references and reports all the
// missing ones at once as *MissingError. Any other resolver failure is
// returned as is.
func CheckRequired(cfg *config.Config, r Resolver) error {
	var missing []string
	for _, name := range cfg.RequiredEnv() {
		_, err := r.Resolve(config.EnvRefPrefix + name)
		switch {
		case err == nil:
		case errors.Is(err, ErrNotSet):
			missing = append(missing, name)
		default:
			return fmt.Errorf("resolve %s: %w", name, err)
		}
	}
	if len(missing) > 0 {
		return &MissingError{Names: missing}
	}
	return nil
}
