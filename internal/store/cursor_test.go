package store

import (
	"encoding/base64"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestCursorRoundTrip(t *testing.T) {
	// Arrange: microsecond precision, as PostgreSQL timestamps have.
	want := Cursor{
		Time: time.Date(2026, 10, 24, 8, 30, 0, 127001000, time.UTC),
		ID:   uuid.MustParse("01929a3c-5e10-7b2a-8c4d-1f2e3d4c5b6a"),
	}

	// Act
	got, err := DecodeCursor(EncodeCursor(want))

	// Assert
	if err != nil {
		t.Fatalf("DecodeCursor error: %v", err)
	}
	if !got.Time.Equal(want.Time) || got.ID != want.ID {
		t.Errorf("round trip = %+v, want %+v", got, want)
	}
}

func TestDecodeCursorRejectsMalformed(t *testing.T) {
	enc := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
	tests := []struct {
		name   string
		cursor string
	}{
		{"not base64", "!!!"},
		{"not json", enc("hello")},
		{"missing time", enc(`{"id":"01929a3c-5e10-7b2a-8c4d-1f2e3d4c5b6a"}`)},
		{"missing id", enc(`{"t":"2026-10-24T08:30:00Z"}`)},
		{"bad time", enc(`{"t":"yesterday","id":"01929a3c-5e10-7b2a-8c4d-1f2e3d4c5b6a"}`)},
		{"bad id", enc(`{"t":"2026-10-24T08:30:00Z","id":"nope"}`)},
		{"unknown field", enc(`{"t":"2026-10-24T08:30:00Z","id":"01929a3c-5e10-7b2a-8c4d-1f2e3d4c5b6a","x":1}`)},
		{"empty", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := DecodeCursor(tt.cursor); !errors.Is(err, ErrInvalidCursor) {
				t.Fatalf("DecodeCursor(%q) error = %v, want ErrInvalidCursor", tt.cursor, err)
			}
		})
	}
}
