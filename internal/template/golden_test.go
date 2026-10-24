package template

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"messagehub/internal/contracts"
)

var goldenNow = time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)

func TestGoldenTemplateCases(t *testing.T) {
	cases, err := contracts.LoadTemplateCases(contracts.TemplateCasesPath())
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			// Arrange
			vars := goldenVars(c)

			// Act
			got, err := renderGolden(c, vars)

			// Assert
			if c.ExpectsError() {
				class, ok := ClassOf(err)
				if !ok || string(class) != string(c.WantError) {
					t.Fatalf("err = %v, want class %s", err, c.WantError)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(normalizeJSON(t, got), normalizeJSON(t, c.Want)) {
				t.Fatalf("got %#v, want %#v", got, c.Want)
			}
		})
	}
}

func TestGoldenTemplatesPassStaticValidation(t *testing.T) {
	cases, err := contracts.LoadTemplateCases(contracts.TemplateCasesPath())
	if err != nil {
		t.Fatal(err)
	}
	fields := map[contracts.Location]string{
		contracts.LocationURL:    "destinations[0].request.url",
		contracts.LocationHeader: "destinations[0].request.headers.X",
		contracts.LocationBody:   "destinations[0].request.body.x",
	}
	for _, c := range cases {
		s, isString := c.Template.(string)
		// Only syntax, unknown_function and static bad_argument problems are
		// detectable without evaluation; missing paths are runtime-only.
		if !isString || c.WantError == contracts.ErrorMissingPath {
			continue
		}
		t.Run(c.Name, func(t *testing.T) {
			// Act
			err := ValidateField(fields[c.Location], s)

			// Assert
			if !c.ExpectsError() {
				if err != nil {
					t.Fatalf("valid template rejected: %v", err)
				}
				return
			}
			if class, ok := ClassOf(err); ok && string(class) != string(c.WantError) {
				t.Fatalf("static class = %s, want %s", class, c.WantError)
			}
		})
	}
}

func goldenVars(c contracts.TemplateCase) Vars {
	ev := c.Event
	v := Vars{
		Event: Event{
			ID: ev.ID, Source: ev.Source, SourceEventID: ev.SourceEventID, EventType: ev.EventType,
			OccurredAt: ev.OccurredAt, ReceivedAt: ev.ReceivedAt,
			IdempotencyKey: ev.IdempotencyKey, OrderingKey: ev.OrderingKey,
			Payload: ev.Payload, Metadata: ev.Metadata,
		},
		Secrets: c.Secrets,
		Now:     goldenNow,
	}
	if c.Delivery != nil {
		v.DeliveryID, v.Attempt = c.Delivery.ID, c.Delivery.Attempt
	}
	return v
}

func renderGolden(c contracts.TemplateCase, v Vars) (any, error) {
	switch c.Location {
	case contracts.LocationBody:
		out, err := RenderBody(c.Template, v)
		return out.Actual, err
	case contracts.LocationHeader:
		var body []byte
		if c.RequestBody != nil {
			body = []byte(*c.RequestBody)
		}
		out, err := RenderHeader(c.Template.(string), v, body)
		return out.Actual, err
	default:
		out, err := RenderURL(c.Template.(string), v)
		return out.Actual, err
	}
}

// normalizeJSON makes int64, json.Number and float64 comparable.
func normalizeJSON(t *testing.T, v any) any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal %#v: %v", v, err)
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}
