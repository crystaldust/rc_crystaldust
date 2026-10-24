package template

import (
	"errors"
	"os"
	"strings"
	"testing"

	"messagehub/internal/config"
	"messagehub/internal/domain"
)

func TestValidateFieldInfersLocationFromField(t *testing.T) {
	tests := []struct {
		name  string
		field string
		tmpl  string
		want  ErrorClass // empty: valid
	}{
		{"request.body allowed in header", "destinations[0].request.headers.X-Sig", "{{ request.body | sha256 }}", ""},
		{"request.body allowed in quoted header", `destinations[0].request.headers["X Sig"]`, "{{ request.body }}", ""},
		{"request.body rejected in url", "destinations[1].request.url", "https://x/{{ request.body }}", ErrorSyntax},
		{"request.body rejected in body", "destinations[0].request.body.items[0].sig", "{{ request.body }}", ErrorSyntax},
		{"plain literal", "destinations[0].request.body.status", "paid", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			err := ValidateField(tt.field, tt.tmpl)

			// Assert
			class, _ := ClassOf(err)
			if class != tt.want {
				t.Fatalf("err = %v, want class %q", err, tt.want)
			}
		})
	}
}

func TestExampleConfigPassesTemplateValidation(t *testing.T) {
	// Act
	_, err := config.Load("../../config/examples/hub.example.yaml", config.WithTemplateValidator(ValidateField))

	// Assert
	if err != nil {
		t.Fatalf("example config rejected: %v", err)
	}
}

func TestConfigWithBadTemplateFailsToLoad(t *testing.T) {
	// Arrange
	raw, err := os.ReadFile("../../config/examples/hub.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	bad := strings.Replace(string(raw), "{{ event.payload.plan }}", "{{ event.payload.plan | reverse }}", 1)

	// Act
	_, err = config.Parse([]byte(bad), config.WithTemplateValidator(ValidateField))

	// Assert
	var verr *domain.ValidationError
	if !errors.As(err, &verr) || !verr.HasField("destinations[1].request.body.plan") {
		t.Fatalf("err = %v, want a problem at destinations[1].request.body.plan", err)
	}
}

func TestParseRejectsMalformedTemplates(t *testing.T) {
	tests := []struct {
		name string
		tmpl string
		want ErrorClass
	}{
		{"empty expression", "{{}}", ErrorSyntax},
		{"trailing pipe", "{{ event.id | }}", ErrorSyntax},
		{"literal as head", `{{ "x" | upper }}`, ErrorSyntax},
		{"two heads", "{{ event.id event.source }}", ErrorSyntax},
		{"function name not an identifier", "{{ event.id | 1 }}", ErrorSyntax},
		{"unknown root", "{{ env.HOME }}", ErrorSyntax},
		{"unknown event field", "{{ event.nope }}", ErrorSyntax},
		{"bare event", "{{ event }}", ErrorSyntax},
		{"secret without name", "{{ secret }}", ErrorSyntax},
		{"secret too deep", "{{ secret.A.B }}", ErrorSyntax},
		{"delivery unknown field", "{{ delivery.nope }}", ErrorSyntax},
		{"now with field", "{{ now.year }}", ErrorSyntax},
		{"request other than body", "{{ request.url }}", ErrorSyntax},
		{"empty path segment", "{{ event..id }}", ErrorSyntax},
		{"unexpected character", "{{ event.id # }}", ErrorSyntax},
		{"unterminated string", `{{ event.id | default "x }}`, ErrorSyntax},
		{"invalid string escape", `{{ event.id | default "\q" }}`, ErrorSyntax},
		{"invalid number", "{{ event.id | default 1.2.3 }}", ErrorSyntax},
		{"too many functions", "{{ event.id" + strings.Repeat(" | lower", maxPipelineCalls+1) + " }}", ErrorSyntax},
		{"too long", strings.Repeat("a", maxTemplateBytes+1), ErrorSyntax},
		{"default with path argument", "{{ event.id | default event.source }}", ErrorBadArgument},
		{"default with two arguments", `{{ event.id | default "a" "b" }}`, ErrorBadArgument},
		{"format_time with string layout", `{{ now | format_time "unix" }}`, ErrorBadArgument},
		{"hmac with bare secret", "{{ event.id | hmac_sha256_hex secret }}", ErrorBadArgument},
		{"hmac with literal", `{{ event.id | hmac_sha256_hex "k" }}`, ErrorBadArgument},
		{"unknown function", "{{ event.id | nope }}", ErrorUnknownFunction},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			err := ValidateField("destinations[0].request.headers.X", tt.tmpl)

			// Assert
			if class, _ := ClassOf(err); class != tt.want {
				t.Fatalf("err = %v, want class %s", err, tt.want)
			}
		})
	}
}

func TestParseFindsQueryPartOfURL(t *testing.T) {
	// Act
	p, err := parse("https://x/{{event.id}}/a?b={{event.id}}&c={{event.id}}", LocationURL)

	// Assert
	if err != nil {
		t.Fatal(err)
	}
	var inQuery []bool
	for _, pt := range p.parts {
		if pt.expr != nil {
			inQuery = append(inQuery, pt.inQuery)
		}
	}
	if len(inQuery) != 3 || inQuery[0] || !inQuery[1] || !inQuery[2] {
		t.Fatalf("inQuery = %v, want [false true true]", inQuery)
	}
}

func TestSingleExpressionDetection(t *testing.T) {
	tests := []struct {
		tmpl string
		want bool
	}{
		{"{{ event.id }}", true},
		{"  {{ event.id }}\n", true},
		{"x{{ event.id }}", false},
		{"{{ event.id }}{{ event.id }}", false},
		{"plain", false},
	}
	for _, tt := range tests {
		t.Run(tt.tmpl, func(t *testing.T) {
			// Arrange
			p, err := parse(tt.tmpl, LocationBody)
			if err != nil {
				t.Fatal(err)
			}

			// Act
			_, got := p.single()

			// Assert
			if got != tt.want {
				t.Fatalf("single = %v, want %v", got, tt.want)
			}
		})
	}
}
