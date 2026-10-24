package store

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
)

// ErrInvalidCursor is returned for a cursor this package did not produce.
var ErrInvalidCursor = errors.New("invalid cursor")

// Cursor is the position after the last item of a page. Lists are ordered
// by (time DESC, id DESC), so the next page holds rows strictly before it.
type Cursor struct {
	Time time.Time
	ID   uuid.UUID
}

type cursorJSON struct {
	T  string `json:"t"`
	ID string `json:"id"`
}

// EncodeCursor returns the opaque form: base64url of {"t": RFC 3339 nano, "id"}.
func EncodeCursor(c Cursor) string {
	// Marshalling two strings cannot fail.
	b, _ := json.Marshal(cursorJSON{T: c.Time.UTC().Format(time.RFC3339Nano), ID: c.ID.String()})
	return base64.RawURLEncoding.EncodeToString(b)
}

// DecodeCursor parses a cursor produced by EncodeCursor.
func DecodeCursor(s string) (Cursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil || len(raw) == 0 {
		return Cursor{}, ErrInvalidCursor
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var c cursorJSON
	if err := dec.Decode(&c); err != nil {
		return Cursor{}, ErrInvalidCursor
	}
	t, err := time.Parse(time.RFC3339Nano, c.T)
	if err != nil {
		return Cursor{}, ErrInvalidCursor
	}
	id, err := uuid.Parse(c.ID)
	if err != nil {
		return Cursor{}, ErrInvalidCursor
	}
	return Cursor{Time: t, ID: id}, nil
}
