package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// Config is a validated, read-only configuration. Lookup tables are built
// once in Parse and never modified; accessors return deep copies.
type Config struct {
	doc               document
	principalByID     map[string]int
	policyByID        map[string]int
	destinationByID   map[string]int
	routesByEventType map[string][]int
	hash              string
	warnings          []string
}

func newConfig(doc document) (*Config, error) {
	hash, err := computeHash(doc)
	if err != nil {
		return nil, err
	}
	routesByType := make(map[string][]int)
	for i, r := range doc.Routes {
		routesByType[r.EventType] = append(routesByType[r.EventType], i)
	}
	return &Config{
		doc:               doc,
		principalByID:     indexByID(doc.Principals, func(p Principal) string { return p.ID }),
		policyByID:        indexByID(doc.Policies, func(p Policy) string { return p.ID }),
		destinationByID:   indexByID(doc.Destinations, func(d Destination) string { return d.ID }),
		routesByEventType: routesByType,
		hash:              hash,
		warnings:          duplicateRouteWarnings(doc.Routes),
	}, nil
}

func indexByID[T any](items []T, id func(T) string) map[string]int {
	out := make(map[string]int, len(items))
	for i, it := range items {
		out[id(it)] = i
	}
	return out
}

// computeHash is the SHA-256 lower-case hex of the canonical JSON form of
// the loaded config (defaults applied, object keys sorted). Re-decoding
// into generic values makes encoding/json emit every object's keys sorted.
func computeHash(doc document) (string, error) {
	b, err := json.Marshal(doc)
	if err != nil {
		return "", fmt.Errorf("encode config for hash: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var generic any
	if err := dec.Decode(&generic); err != nil {
		return "", fmt.Errorf("decode config for hash: %w", err)
	}
	canonical, err := json.Marshal(generic)
	if err != nil {
		return "", fmt.Errorf("encode canonical config: %w", err)
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}

// Hash returns the ConfigHash recorded on events and attempts.
func (c *Config) Hash() string { return c.hash }

// Version returns the config format version.
func (c *Config) Version() int { return c.doc.Version }

// Warnings returns non-fatal findings, e.g. duplicate routes.
func (c *Config) Warnings() []string { return cloneSlice(c.warnings) }

// Principal returns the principal with the given id.
func (c *Config) Principal(id string) (Principal, bool) {
	i, ok := c.principalByID[id]
	if !ok {
		return Principal{}, false
	}
	return c.doc.Principals[i], true
}

// Principals returns all principals in file order. The API layer resolves
// each APIKeySHA256Ref at runtime to build its key-hash lookup.
func (c *Config) Principals() []Principal { return cloneSlice(c.doc.Principals) }

// Policy returns the policy with the given id.
func (c *Config) Policy(id string) (Policy, bool) {
	i, ok := c.policyByID[id]
	if !ok {
		return Policy{}, false
	}
	return c.doc.Policies[i].clone(), true
}

// Policies returns all policies in file order.
func (c *Config) Policies() []Policy { return cloneAll(c.doc.Policies, Policy.clone) }

// Destination returns the destination with the given id.
func (c *Config) Destination(id string) (Destination, bool) {
	i, ok := c.destinationByID[id]
	if !ok {
		return Destination{}, false
	}
	return c.doc.Destinations[i].clone(), true
}

// Destinations returns all destinations in file order.
func (c *Config) Destinations() []Destination {
	return cloneAll(c.doc.Destinations, Destination.clone)
}

// Routes returns all routes in file order.
func (c *Config) Routes() []Route { return cloneAll(c.doc.Routes, Route.clone) }

// RoutesForEventType returns the routes whose event_type equals eventType
// exactly (§4), in file order; nil when none match.
func (c *Config) RoutesForEventType(eventType string) []Route {
	idx := c.routesByEventType[eventType]
	if len(idx) == 0 {
		return nil
	}
	out := make([]Route, len(idx))
	for i, j := range idx {
		out[i] = c.doc.Routes[j].clone()
	}
	return out
}
