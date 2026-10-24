// Package domain holds the Message Hub core types and pure rules.
// It depends on no other internal package. Behaviour is specified in
// docs/design/domain-model.md.
package domain

import (
	"errors"
	"strings"
)

// ErrInvalidTransition is returned when a trigger is not allowed from a status.
var ErrInvalidTransition = errors.New("invalid delivery status transition")

// FieldError describes one validation problem at a field path.
type FieldError struct {
	Field  string
	Reason string
}

// ValidationError aggregates every problem found, rather than stopping at the first.
type ValidationError struct {
	Problems []FieldError
}

func (e *ValidationError) Error() string {
	lines := make([]string, 0, len(e.Problems))
	for _, p := range e.Problems {
		lines = append(lines, p.Field+": "+p.Reason)
	}
	return "validation failed:\n" + strings.Join(lines, "\n")
}

// HasField reports whether any problem refers to the given field path.
func (e *ValidationError) HasField(field string) bool {
	for _, p := range e.Problems {
		if p.Field == field {
			return true
		}
	}
	return false
}
