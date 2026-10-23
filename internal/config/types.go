// Package config loads and validates the Message Hub YAML configuration
// (docs/design/domain-model.md §11, ADR-0005). A loaded *Config is read-only:
// every accessor returns copies, so callers can never change shared state.
package config

import (
	"messagehub/internal/domain"
)

// Role is the permission role of a Principal (§9).
type Role string

const (
	RoleSource   Role = "source"
	RoleViewer   Role = "viewer"
	RoleOperator Role = "operator"
)

// OrderingMode selects ordered delivery behaviour (§6).
type OrderingMode string

const (
	OrderingNone   OrderingMode = "none"
	OrderingPerKey OrderingMode = "per_key"
)

// AuthType selects how outbound requests are authenticated.
type AuthType string

const (
	AuthNone                    AuthType = "none"
	AuthOAuth2ClientCredentials AuthType = "oauth2_client_credentials"
)

// RuleOp is a body / match rule operator (§7.2).
type RuleOp string

const (
	OpEq        RuleOp = "eq"
	OpNe        RuleOp = "ne"
	OpIn        RuleOp = "in"
	OpExists    RuleOp = "exists"
	OpNotExists RuleOp = "not_exists"
)

// DefaultMaxInFlight applies when a destination omits max_in_flight (§8).
const DefaultMaxInFlight = 10

// document is the typed form of the whole YAML file.
type document struct {
	Version      int           `json:"version"`
	Principals   []Principal   `json:"principals"`
	Policies     []Policy      `json:"policies"`
	Destinations []Destination `json:"destinations"`
	Routes       []Route       `json:"routes"`
}

// Principal is an API caller. APIKeySHA256Ref is an env reference
// ("env:NAME") to the SHA-256 hex of the key, resolved at runtime.
type Principal struct {
	ID              string `json:"id"`
	Role            Role   `json:"role"`
	APIKeySHA256Ref string `json:"api_key_sha256_ref"`
}

// Policy groups timeout, retry, success and ordering rules.
type Policy struct {
	ID       string   `json:"id"`
	Timeout  Duration `json:"timeout"`
	Retry    Retry    `json:"retry"`
	Success  Success  `json:"success"`
	Ordering Ordering `json:"ordering"`
}

// Retry is the retry budget and backoff (§5.3, §7.4).
type Retry struct {
	MaxAttempts int     `json:"max_attempts"`
	Backoff     Backoff `json:"backoff"`
	RetryOn     RetryOn `json:"retry_on"`
}

// Backoff parameters; Jitter is in [0, 1).
type Backoff struct {
	Initial    Duration `json:"initial"`
	Multiplier float64  `json:"multiplier"`
	Max        Duration `json:"max"`
	Jitter     float64  `json:"jitter"`
}

// RetryOn lists status codes that are retryable.
type RetryOn struct {
	Status []StatusMatcher `json:"status"`
}

// Success decides whether a response counts as delivered (§7.2).
type Success struct {
	Status []StatusMatcher `json:"status"`
	Body   []Rule          `json:"body,omitempty"`
}

// Ordering configures per-key ordered delivery. OnPredecessorDead is set
// (default block) only when Mode is per_key.
type Ordering struct {
	Mode              OrderingMode             `json:"mode"`
	OnPredecessorDead domain.OnPredecessorDead `json:"on_predecessor_dead,omitempty"`
}

// Rule is one {path, op, value} condition. Value is a decoded JSON value
// (numbers are json.Number); it is nil for exists / not_exists.
type Rule struct {
	Path  string `json:"path"`
	Op    RuleOp `json:"op"`
	Value any    `json:"value,omitempty"`
}

// Destination is one vendor endpoint and its request template.
type Destination struct {
	ID          string            `json:"id"`
	Policy      string            `json:"policy"`
	MaxInFlight int               `json:"max_in_flight"`
	Secrets     map[string]string `json:"secrets,omitempty"`
	Auth        Auth              `json:"auth"`
	Request     Request           `json:"request"`
	// AllowLiteral lists credential-looking fields that legitimately hold a
	// literal value (§11 "凭据字段"), e.g. "headers.X-Signature-Version".
	AllowLiteral []string `json:"allow_literal,omitempty"`
}

// Auth is the outbound authentication. OAuth2 fields are set only for
// oauth2_client_credentials; *Ref fields are env references.
type Auth struct {
	Type            AuthType `json:"type"`
	TokenURL        string   `json:"token_url,omitempty"`
	ClientIDRef     string   `json:"client_id_ref,omitempty"`
	ClientSecretRef string   `json:"client_secret_ref,omitempty"`
	Scopes          []string `json:"scopes,omitempty"`
}

// Request is the request template. Body is any decoded JSON value whose
// leaf strings may be templates.
type Request struct {
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    any               `json:"body,omitempty"`
}

// Route maps an event_type (plus optional conditions) to destinations (§4).
type Route struct {
	ID           string   `json:"id"`
	EventType    string   `json:"event_type"`
	Sources      []string `json:"sources,omitempty"`
	Match        []Rule   `json:"match,omitempty"`
	Destinations []string `json:"destinations"`
}
