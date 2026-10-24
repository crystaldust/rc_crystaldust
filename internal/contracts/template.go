package contracts

import (
	"errors"
	"fmt"
	"time"

	"go.yaml.in/yaml/v3"
)

// Location is where a template appears in a Destination request (§7.1).
type Location string

const (
	LocationBody   Location = "body"
	LocationHeader Location = "header"
	LocationURL    Location = "url"
)

// ErrorClass is a template render error category (§7.1).
type ErrorClass string

const (
	ErrorSyntax          ErrorClass = "syntax"
	ErrorMissingPath     ErrorClass = "missing_path"
	ErrorUnknownFunction ErrorClass = "unknown_function"
	ErrorBadArgument     ErrorClass = "bad_argument"
)

// ErrorClasses lists every render error class.
var ErrorClasses = []ErrorClass{ErrorSyntax, ErrorMissingPath, ErrorUnknownFunction, ErrorBadArgument}

// Event is the accepted event a template renders against.
type Event struct {
	ID             string
	Source         string
	SourceEventID  string
	EventType      string
	OccurredAt     time.Time
	ReceivedAt     time.Time
	IdempotencyKey string // empty when absent
	OrderingKey    string // empty when absent
	Payload        map[string]any
	Metadata       map[string]string
}

// Delivery carries the `delivery.*` template roots.
type Delivery struct {
	ID      string `yaml:"id"`
	Attempt int    `yaml:"attempt"`
}

// TemplateCase is one golden case for the template engine.
type TemplateCase struct {
	Name     string
	Location Location
	// Template is a string for header and url; any JSON value for body.
	Template any
	Event    Event
	Secrets  map[string]string
	Delivery *Delivery
	// RequestBody is the rendered body bytes for `request.body` (header only).
	RequestBody *string
	// Want is the expected output when WantError is empty: a JSON value for
	// body, a string for header and url.
	Want      any
	WantError ErrorClass
}

// ExpectsError reports whether rendering this case must fail.
func (c TemplateCase) ExpectsError() bool { return c.WantError != "" }

type rawEvent struct {
	ID             string            `yaml:"id"`
	Source         string            `yaml:"source"`
	SourceEventID  string            `yaml:"source_event_id"`
	EventType      string            `yaml:"event_type"`
	OccurredAt     string            `yaml:"occurred_at"`
	ReceivedAt     string            `yaml:"received_at"`
	IdempotencyKey string            `yaml:"idempotency_key"`
	OrderingKey    string            `yaml:"ordering_key"`
	Payload        yaml.Node         `yaml:"payload"`
	Metadata       map[string]string `yaml:"metadata"`
}

type rawTemplateCase struct {
	Name        string            `yaml:"name"`
	Location    Location          `yaml:"location"`
	Template    yaml.Node         `yaml:"template"`
	Event       rawEvent          `yaml:"event"`
	Secrets     map[string]string `yaml:"secrets"`
	Delivery    *Delivery         `yaml:"delivery"`
	RequestBody *string           `yaml:"request_body"`
	Want        yaml.Node         `yaml:"want"`
	WantError   ErrorClass        `yaml:"want_error"`
}

func (r rawTemplateCase) caseName() string { return r.Name }

// LoadTemplateCases loads and validates template golden cases.
func LoadTemplateCases(path string) ([]TemplateCase, error) {
	return loadCases(path, convertTemplateCase)
}

func convertTemplateCase(r rawTemplateCase) (TemplateCase, error) {
	if err := validateTemplateShape(r); err != nil {
		return TemplateCase{}, err
	}
	tmpl, err := jsonValue(r.Template)
	if err != nil {
		return TemplateCase{}, fmt.Errorf("template: %w", err)
	}
	event, err := convertEvent(r.Event)
	if err != nil {
		return TemplateCase{}, fmt.Errorf("event: %w", err)
	}
	var want any
	if isSet(r.Want) {
		if want, err = jsonValue(r.Want); err != nil {
			return TemplateCase{}, fmt.Errorf("want: %w", err)
		}
	}
	return TemplateCase{
		Name:        r.Name,
		Location:    r.Location,
		Template:    tmpl,
		Event:       event,
		Secrets:     r.Secrets,
		Delivery:    r.Delivery,
		RequestBody: r.RequestBody,
		Want:        want,
		WantError:   r.WantError,
	}, nil
}

func validateTemplateShape(r rawTemplateCase) error {
	if err := requireName(r.Name); err != nil {
		return err
	}
	switch r.Location {
	case LocationBody, LocationHeader, LocationURL:
	default:
		return fmt.Errorf("location %q: want body, header or url", r.Location)
	}
	if !isSet(r.Template) {
		return errors.New("template is required")
	}
	if isSet(r.Want) == (r.WantError != "") {
		return errors.New("exactly one of want and want_error is required")
	}
	if r.WantError != "" && !isErrorClass(r.WantError) {
		return fmt.Errorf("want_error %q: unknown error class", r.WantError)
	}
	if r.Location != LocationBody {
		if !isString(r.Template) {
			return fmt.Errorf("template must be a string for %s", r.Location)
		}
		if isSet(r.Want) && !isString(r.Want) {
			return fmt.Errorf("want must be a string for %s", r.Location)
		}
	}
	if r.RequestBody != nil && r.Location != LocationHeader {
		return errors.New("request_body is only allowed for header")
	}
	if r.Delivery != nil && r.Delivery.Attempt < 1 {
		return errors.New("delivery.attempt must be >= 1")
	}
	return nil
}

func convertEvent(r rawEvent) (Event, error) {
	required := []struct{ field, value string }{
		{"id", r.ID}, {"source", r.Source}, {"source_event_id", r.SourceEventID}, {"event_type", r.EventType},
	}
	for _, f := range required {
		if f.value == "" {
			return Event{}, fmt.Errorf("%s is required", f.field)
		}
	}
	occurredAt, err := time.Parse(time.RFC3339, r.OccurredAt)
	if err != nil {
		return Event{}, fmt.Errorf("occurred_at: %w", err)
	}
	receivedAt, err := time.Parse(time.RFC3339, r.ReceivedAt)
	if err != nil {
		return Event{}, fmt.Errorf("received_at: %w", err)
	}
	if r.Payload.Kind != yaml.MappingNode {
		return Event{}, errors.New("payload must be an object")
	}
	payload, err := jsonValue(r.Payload)
	if err != nil {
		return Event{}, fmt.Errorf("payload: %w", err)
	}
	return Event{
		ID:             r.ID,
		Source:         r.Source,
		SourceEventID:  r.SourceEventID,
		EventType:      r.EventType,
		OccurredAt:     occurredAt,
		ReceivedAt:     receivedAt,
		IdempotencyKey: r.IdempotencyKey,
		OrderingKey:    r.OrderingKey,
		Payload:        payload.(map[string]any),
		Metadata:       r.Metadata,
	}, nil
}

func isErrorClass(c ErrorClass) bool {
	for _, known := range ErrorClasses {
		if c == known {
			return true
		}
	}
	return false
}

func isString(n yaml.Node) bool {
	return n.Kind == yaml.ScalarNode && n.ShortTag() == "!!str"
}
