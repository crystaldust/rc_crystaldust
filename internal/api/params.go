package api

import (
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"messagehub/internal/domain"
	"messagehub/internal/store"
)

// configIDPattern is the ConfigID schema (api/openapi.yaml, config schema).
var configIDPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{1,62}$`)

// params validates query and path parameters, collecting every problem
// so one 400 response lists them all.
type params struct {
	query    url.Values
	problems []domain.FieldError
}

func newParams(q url.Values) *params { return &params{query: q} }

func (p *params) fail(field, reason string) {
	p.problems = append(p.problems, domain.FieldError{Field: field, Reason: reason})
}

// err returns the collected problems, or nil.
func (p *params) err() *domain.ValidationError {
	if len(p.problems) == 0 {
		return nil
	}
	return &domain.ValidationError{Problems: p.problems}
}

// str returns an optional string parameter checked against length and pattern.
func (p *params) str(name string, maxLen int, pattern *regexp.Regexp) string {
	v := p.query.Get(name)
	if !p.query.Has(name) {
		return ""
	}
	n := utf8.RuneCountInString(v)
	switch {
	case !utf8.ValidString(v) || strings.ContainsRune(v, 0):
		p.fail(name, "must be valid UTF-8 without the NUL character")
		return ""
	case n < 1 || n > maxLen:
		p.fail(name, fmt.Sprintf("length must be between 1 and %d characters", maxLen))
		return ""
	case pattern != nil && !pattern.MatchString(v):
		p.fail(name, "must match "+pattern.String())
		return ""
	}
	return v
}

// canonicalUUIDLen is the length of the 8-4-4-4-12 form, the only one
// accepted (OpenAPI format: uuid); uuid.Parse also takes urn:, braces etc.
const canonicalUUIDLen = 36

func parseCanonicalUUID(s string) (uuid.UUID, bool) {
	if len(s) != canonicalUUIDLen {
		return uuid.UUID{}, false
	}
	id, err := uuid.Parse(s)
	return id, err == nil
}

// pathUUID parses a path parameter that must be a UUID.
func (p *params) pathUUID(name, value string) uuid.UUID {
	id, ok := parseCanonicalUUID(value)
	if !ok {
		p.fail(name, "must be a UUID")
	}
	return id
}

func (p *params) uuid(name string) *uuid.UUID {
	if !p.query.Has(name) {
		return nil
	}
	id, ok := parseCanonicalUUID(p.query.Get(name))
	if !ok {
		p.fail(name, "must be a UUID")
		return nil
	}
	return &id
}

func (p *params) time(name string) *time.Time {
	if !p.query.Has(name) {
		return nil
	}
	t, err := time.Parse(time.RFC3339, p.query.Get(name))
	if err != nil {
		p.fail(name, "must be an RFC 3339 date-time with time zone")
		return nil
	}
	return &t
}

// enum returns an optional parameter that must be one of allowed.
func (p *params) enum(name string, allowed []string) string {
	if !p.query.Has(name) {
		return ""
	}
	v := p.query.Get(name)
	for _, a := range allowed {
		if v == a {
			return v
		}
	}
	p.fail(name, fmt.Sprintf("must be one of %v", allowed))
	return ""
}

func (p *params) limit() int {
	if !p.query.Has("limit") {
		return store.DefaultLimit
	}
	n, err := strconv.Atoi(p.query.Get("limit"))
	if err != nil || n < 1 || n > store.MaxLimit {
		p.fail("limit", fmt.Sprintf("must be an integer between 1 and %d", store.MaxLimit))
		return store.DefaultLimit
	}
	return n
}

func (p *params) cursor() *store.Cursor {
	if !p.query.Has("cursor") {
		return nil
	}
	c, err := store.DecodeCursor(p.query.Get("cursor"))
	if err != nil {
		p.fail("cursor", "is not a cursor returned by this API")
		return nil
	}
	return &c
}

func deliveryStatuses() []string {
	out := make([]string, len(domain.AllStatuses))
	for i, s := range domain.AllStatuses {
		out[i] = string(s)
	}
	return out
}

var routingStatuses = []string{string(domain.RoutingRouted), string(domain.RoutingUnrouted)}
