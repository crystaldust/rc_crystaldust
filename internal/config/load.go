package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"go.yaml.in/yaml/v3"

	"messagehub/internal/domain"
)

// rootField is the field path used for problems that concern the whole file.
const rootField = "(root)"

// TemplateValidator checks one template string. field is the config path of
// the string, e.g. "destinations[0].request.headers.Authorization".
type TemplateValidator func(field, tmpl string) error

// Option customises loading.
type Option func(*options)

type options struct {
	templateValidator TemplateValidator
}

// WithTemplateValidator installs a validator called for every request URL,
// header value and body leaf string. The default accepts everything; the
// template engine plugs in here in Phase 4.
func WithTemplateValidator(v TemplateValidator) Option {
	return func(o *options) {
		if v != nil {
			o.templateValidator = v
		}
	}
}

func buildOptions(opts []Option) options {
	o := options{templateValidator: func(string, string) error { return nil }}
	for _, opt := range opts {
		opt(&o)
	}
	return o
}

// Load reads and validates the configuration file at path. Problems in the
// file are returned as *domain.ValidationError; I/O failures are wrapped.
func Load(path string, opts ...Option) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("load config %q: %w", path, err)
	}
	cfg, err := Parse(data, opts...)
	if err != nil {
		return nil, fmt.Errorf("load config %q: %w", path, err)
	}
	return cfg, nil
}

// Parse validates configuration bytes: strict YAML → JSON Schema → typed
// decode → semantic checks → read-only lookup tables and ConfigHash.
func Parse(data []byte, opts ...Option) (*Config, error) {
	o := buildOptions(opts)
	instance, err := decodeYAML(data)
	if err != nil {
		return nil, err
	}
	if err := validateSchema(instance); err != nil {
		return nil, err
	}
	doc, err := decodeTyped(instance)
	if err != nil {
		return nil, err
	}
	doc = applyDefaults(doc)
	problems := validateSemantics(doc)
	problems = append(problems, validateTemplates(doc, o.templateValidator)...)
	if len(problems) > 0 {
		return nil, &domain.ValidationError{Problems: problems}
	}
	return newConfig(doc)
}

// decodeYAML decodes a single YAML document into JSON-compatible values.
// yaml v3 already rejects duplicate mapping keys.
func decodeYAML(data []byte) (any, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var raw any
	if err := dec.Decode(&raw); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, singleProblem(rootField, "configuration is empty")
		}
		return nil, singleProblem(rootField, "invalid YAML: "+err.Error())
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, singleProblem(rootField, "must contain exactly one YAML document")
	}
	return toJSONValue(raw)
}

// toJSONValue converts YAML-decoded values into the types encoding/json
// produces (map[string]any, []any, json.Number, string, bool, nil) via a
// JSON round trip, so schema validation and typed decoding see one shape.
func toJSONValue(raw any) (any, error) {
	b, err := json.Marshal(stringKeys(raw))
	if err != nil {
		return nil, singleProblem(rootField, "unsupported YAML value: "+err.Error())
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var out any
	if err := dec.Decode(&out); err != nil {
		return nil, fmt.Errorf("re-decode config as JSON: %w", err)
	}
	return out, nil
}

// stringKeys converts map[any]any (YAML maps with non-string keys) into
// map[string]any so they can be JSON encoded.
func stringKeys(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, child := range t {
			out[k] = stringKeys(child)
		}
		return out
	case map[any]any:
		out := make(map[string]any, len(t))
		for k, child := range t {
			out[fmt.Sprint(k)] = stringKeys(child)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, child := range t {
			out[i] = stringKeys(child)
		}
		return out
	default:
		return v
	}
}

// decodeTyped decodes the schema-valid instance into the typed document.
func decodeTyped(instance any) (document, error) {
	b, err := json.Marshal(instance)
	if err != nil {
		return document{}, fmt.Errorf("encode config: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	dec.UseNumber()
	var doc document
	if err := dec.Decode(&doc); err != nil {
		return document{}, singleProblem(rootField, err.Error())
	}
	return doc, nil
}

// applyDefaults returns a copy of doc with defaults filled in (§8, §6).
func applyDefaults(doc document) document {
	out := doc
	out.Policies = cloneAll(doc.Policies, func(p Policy) Policy {
		p = p.clone()
		if p.Ordering.Mode == "" {
			p.Ordering.Mode = OrderingNone
		}
		if p.Ordering.Mode == OrderingPerKey && p.Ordering.OnPredecessorDead == "" {
			p.Ordering.OnPredecessorDead = domain.PredecessorDeadBlock
		}
		return p
	})
	out.Destinations = cloneAll(doc.Destinations, func(d Destination) Destination {
		d = d.clone()
		if d.MaxInFlight == 0 {
			d.MaxInFlight = DefaultMaxInFlight
		}
		if d.Auth.Type == "" {
			d.Auth.Type = AuthNone
		}
		return d
	})
	return out
}

func singleProblem(field, reason string) *domain.ValidationError {
	return &domain.ValidationError{Problems: []domain.FieldError{{Field: field, Reason: reason}}}
}
