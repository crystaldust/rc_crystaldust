package domain

import (
	"bytes"
	"testing"
)

const idSampleSize = 1000

func TestNewIDIsVersion7AndOrdered(t *testing.T) {
	prev := NewID()
	for i := 1; i < idSampleSize; i++ {
		next := NewID()
		if next.Version() != 7 {
			t.Fatalf("version = %d, want 7", next.Version())
		}
		if bytes.Compare(prev[:], next[:]) >= 0 {
			t.Fatalf("ids not strictly increasing at %d: %s >= %s", i, prev, next)
		}
		prev = next
	}
}

func TestValidationErrorFormatting(t *testing.T) {
	err := &ValidationError{Problems: []FieldError{{Field: "a", Reason: "bad"}, {Field: "b", Reason: "worse"}}}

	if got := err.Error(); got != "validation failed:\na: bad\nb: worse" {
		t.Fatalf("unexpected message %q", got)
	}
	if err.HasField("c") {
		t.Fatal("HasField reported absent field")
	}
}
