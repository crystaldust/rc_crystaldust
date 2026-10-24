package template

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// missing marks a path that does not exist; it flows through the pipeline
// until a `default` replaces it, otherwise rendering fails (§7.1).
type missing struct{}

// env is everything one render can read.
type env struct {
	vars        Vars
	requestBody string
}

func (e *expr) eval(en *env) (any, error) {
	v, err := resolve(e.path, en)
	if err != nil {
		return nil, err
	}
	for _, c := range e.calls {
		if _, isMissing := v.(missing); isMissing && c.name != fnDefault {
			continue
		}
		if v, err = c.fn.apply(v, c.arg, en); err != nil {
			return nil, err
		}
	}
	if _, isMissing := v.(missing); isMissing {
		return nil, &Error{Class: ErrorMissingPath, Detail: "path " + strings.Join(e.path, ".") + " not found"}
	}
	return v, nil
}

func resolve(path []string, en *env) (any, error) {
	switch path[0] {
	case rootNow:
		return en.vars.Now.UTC(), nil
	case rootSecret:
		s, ok := en.vars.Secrets[path[1]]
		if !ok {
			return nil, badArgumentf("secret %s is not declared", path[1])
		}
		return s, nil
	case rootDelivery:
		if path[1] == "id" {
			return en.vars.DeliveryID, nil
		}
		return en.vars.Attempt, nil
	case rootRequest:
		return en.requestBody, nil
	default:
		return resolveEvent(path[1:], en.vars.Event), nil
	}
}

func resolveEvent(path []string, ev Event) any {
	switch path[0] {
	case "payload":
		return walk(path[1:], ev.Payload)
	case "metadata":
		return walk(path[1:], metadataValue(ev.Metadata))
	case "occurred_at":
		return ev.OccurredAt
	case "received_at":
		return ev.ReceivedAt
	}
	fields := map[string]string{
		"id": ev.ID, "source": ev.Source, "source_event_id": ev.SourceEventID,
		"event_type": ev.EventType, "idempotency_key": ev.IdempotencyKey, "ordering_key": ev.OrderingKey,
	}
	if s := fields[path[0]]; s != "" {
		return s
	}
	return missing{} // optional envelope field absent (§13.2)
}

func metadataValue(m map[string]string) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// walk descends into a decoded JSON value; a null, scalar or out-of-range
// step yields missing.
func walk(path []string, v any) any {
	for _, seg := range path {
		switch t := v.(type) {
		case map[string]any:
			next, ok := t[seg]
			if !ok {
				return missing{}
			}
			v = next
		case []any:
			i, err := strconv.Atoi(seg)
			if err != nil || i < 0 || i >= len(t) {
				return missing{}
			}
			v = t[i]
		default:
			return missing{}
		}
	}
	return v
}

// toText stringifies a value for embedding in text (§7.1): null → "",
// time → RFC 3339, object / array → compact JSON with sorted keys.
func toText(v any) (string, error) {
	switch t := v.(type) {
	case nil:
		return "", nil
	case string:
		return t, nil
	case bool:
		return strconv.FormatBool(t), nil
	case json.Number:
		return t.String(), nil
	case int:
		return strconv.Itoa(t), nil
	case int64:
		return strconv.FormatInt(t, 10), nil
	case time.Time:
		return formatRFC3339(t), nil
	default:
		b, err := MarshalJSON(t)
		return string(b), err
	}
}

// toJSONValue converts a value for a typed body position; times become
// RFC 3339 strings.
func toJSONValue(v any) any {
	if t, ok := v.(time.Time); ok {
		return formatRFC3339(t)
	}
	return v
}

func formatRFC3339(t time.Time) string {
	return t.UTC().Truncate(time.Second).Format(time.RFC3339)
}

// MarshalJSON is the compact, key-sorted, non-HTML-escaped JSON encoding
// used for request bodies, so a signature over the bytes is reproducible.
func MarshalJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, fmt.Errorf("encode json: %w", err)
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}
