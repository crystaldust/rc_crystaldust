package domain

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

// Envelope limits (docs/design/domain-model.md §2).
const (
	MaxSourceEventIDLen    = 200
	MaxEventTypeLen        = 100
	MaxIdempotencyKeyLen   = 200
	MaxOrderingKeyLen      = 200
	MaxMetadataEntries     = 20
	MaxMetadataValueLen    = 256
	MaxPayloadBytes        = 256 * 1024
	reasonRequired         = "is required"
	reasonMustBeString     = "must be a string"
	reasonUnknownField     = "is not an allowed field"
	reasonPayloadNotObject = "must be a JSON object"
)

var (
	// EventTypePattern is the required shape of event_type, e.g. "order.paid".
	EventTypePattern = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$`)
	// MetadataKeyPattern is the required shape of metadata keys.
	MetadataKeyPattern = regexp.MustCompile(`^[a-zA-Z0-9_.-]{1,64}$`)

	// ErrPayloadTooLarge is returned when payload exceeds MaxPayloadBytes (HTTP 413).
	ErrPayloadTooLarge = fmt.Errorf("payload exceeds %d bytes", MaxPayloadBytes)
)

// Envelope is the request body a source system submits to POST /v1/events.
// Empty optional strings and a nil Metadata mean "absent".
type Envelope struct {
	SourceEventID  string
	EventType      string
	OccurredAt     time.Time
	Payload        json.RawMessage
	IdempotencyKey string
	OrderingKey    string
	Metadata       map[string]string
}

// RoutingStatus records whether an accepted event matched any route.
type RoutingStatus string

const (
	RoutingRouted   RoutingStatus = "routed"
	RoutingUnrouted RoutingStatus = "unrouted"
)

// RoutingStatusFor is the single place deciding routed vs unrouted.
func RoutingStatusFor(matchedRouteIDs []string) RoutingStatus {
	if len(matchedRouteIDs) == 0 {
		return RoutingUnrouted
	}
	return RoutingRouted
}

// Event is an accepted, immutable envelope plus the fields the Hub assigns.
type Event struct {
	Envelope
	ID                uuid.UUID
	Source            string
	IngestSeq         int64
	ReceivedAt        time.Time
	RoutingStatus     RoutingStatus
	MatchedRouteIDs   []string
	RoutingConfigHash string
}

// ParseEnvelope strictly decodes and validates a raw ingest request body.
// It returns ErrPayloadTooLarge for oversized payloads, a *ValidationError
// listing every other problem, or the parsed Envelope.
func ParseEnvelope(raw []byte) (Envelope, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return Envelope{}, &ValidationError{Problems: []FieldError{{Field: "", Reason: "body must be a JSON object"}}}
	}
	if payload, ok := fields["payload"]; ok && len(payload) > MaxPayloadBytes {
		return Envelope{}, ErrPayloadTooLarge
	}

	p := &parser{fields: fields}
	env := Envelope{
		SourceEventID:  p.requiredString("source_event_id", MaxSourceEventIDLen),
		EventType:      p.requiredString("event_type", MaxEventTypeLen),
		OccurredAt:     p.requiredTime("occurred_at"),
		Payload:        p.requiredObject("payload"),
		IdempotencyKey: p.optionalString("idempotency_key", MaxIdempotencyKeyLen),
		OrderingKey:    p.optionalString("ordering_key", MaxOrderingKeyLen),
		Metadata:       p.optionalMetadata("metadata"),
	}
	if env.EventType != "" && !EventTypePattern.MatchString(env.EventType) {
		p.fail("event_type", "must match "+EventTypePattern.String())
	}
	p.rejectUnknownFields()

	if len(p.problems) > 0 {
		return Envelope{}, &ValidationError{Problems: p.problems}
	}
	return env, nil
}

// IsPayloadTooLarge reports whether err means the payload exceeded the limit.
func IsPayloadTooLarge(err error) bool {
	return errors.Is(err, ErrPayloadTooLarge)
}

var knownEnvelopeFields = map[string]bool{
	"source_event_id": true, "event_type": true, "occurred_at": true, "payload": true,
	"idempotency_key": true, "ordering_key": true, "metadata": true,
}

type parser struct {
	fields   map[string]json.RawMessage
	problems []FieldError
}

func (p *parser) fail(field, reason string) {
	p.problems = append(p.problems, FieldError{Field: field, Reason: reason})
}

func (p *parser) rejectUnknownFields() {
	unknown := make([]string, 0, len(p.fields))
	for name := range p.fields {
		if !knownEnvelopeFields[name] {
			unknown = append(unknown, name)
		}
	}
	sort.Strings(unknown)
	for _, name := range unknown {
		p.fail(name, reasonUnknownField)
	}
}

func (p *parser) stringValue(name string, maxLen int) (string, bool) {
	var s string
	if err := json.Unmarshal(p.fields[name], &s); err != nil || isJSONNull(p.fields[name]) {
		p.fail(name, reasonMustBeString)
		return "", false
	}
	n := utf8.RuneCountInString(s)
	if n == 0 || n > maxLen {
		p.fail(name, fmt.Sprintf("length must be between 1 and %d characters", maxLen))
		return "", false
	}
	return s, true
}

func (p *parser) requiredString(name string, maxLen int) string {
	if _, ok := p.fields[name]; !ok {
		p.fail(name, reasonRequired)
		return ""
	}
	s, _ := p.stringValue(name, maxLen)
	return s
}

func (p *parser) optionalString(name string, maxLen int) string {
	if _, ok := p.fields[name]; !ok {
		return ""
	}
	s, _ := p.stringValue(name, maxLen)
	return s
}

func (p *parser) requiredTime(name string) time.Time {
	if _, ok := p.fields[name]; !ok {
		p.fail(name, reasonRequired)
		return time.Time{}
	}
	var s string
	if err := json.Unmarshal(p.fields[name], &s); err != nil || isJSONNull(p.fields[name]) {
		p.fail(name, reasonMustBeString)
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		p.fail(name, "must be an RFC 3339 date-time with time zone")
		return time.Time{}
	}
	return t
}

func (p *parser) requiredObject(name string) json.RawMessage {
	raw, ok := p.fields[name]
	if !ok {
		p.fail(name, reasonRequired)
		return nil
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		p.fail(name, reasonPayloadNotObject)
		return nil
	}
	return append(json.RawMessage(nil), trimmed...)
}

func (p *parser) optionalMetadata(name string) map[string]string {
	raw, ok := p.fields[name]
	if !ok {
		return nil
	}
	var entries map[string]json.RawMessage
	if err := json.Unmarshal(raw, &entries); err != nil || entries == nil {
		p.fail(name, "must be an object of string values")
		return nil
	}
	if len(entries) > MaxMetadataEntries {
		p.fail(name, fmt.Sprintf("must have at most %d entries", MaxMetadataEntries))
	}
	keys := make([]string, 0, len(entries))
	for k := range entries {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make(map[string]string, len(entries))
	for _, k := range keys {
		field := name + "." + k
		if !MetadataKeyPattern.MatchString(k) {
			p.fail(field, "key must match "+MetadataKeyPattern.String())
			continue
		}
		var v string
		if err := json.Unmarshal(entries[k], &v); err != nil || isJSONNull(entries[k]) {
			p.fail(field, reasonMustBeString)
			continue
		}
		if utf8.RuneCountInString(v) > MaxMetadataValueLen {
			p.fail(field, fmt.Sprintf("value must be at most %d characters", MaxMetadataValueLen))
			continue
		}
		out[k] = v
	}
	return out
}

func isJSONNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}
