package contracts

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"math"
	"net/url"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"messagehub/internal/domain"
)

const (
	minTemplateCases = 20
	minSuccessCases  = 12
	minRetryCases    = 8
)

// builtinFunctions is the fixed function set of §7.1.
var builtinFunctions = []string{
	"lower", "upper", "trim", "sha256", "base64", "urlencode",
	"default", "format_time", "to_string", "json", "hmac_sha256_hex", "hmac_sha256_base64",
}

// digestFunctions produce values that must be recomputed, never trusted.
var digestFunctions = []string{"sha256", "base64", "hmac_sha256_hex", "hmac_sha256_base64"}

var expressionPattern = regexp.MustCompile(`\{\{(.*?)\}\}`)

func TestTemplateCases(t *testing.T) {
	// Arrange / Act
	cases, err := LoadTemplateCases(TemplateCasesPath())

	// Assert
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(cases) < minTemplateCases {
		t.Fatalf("got %d template cases, want >= %d", len(cases), minTemplateCases)
	}

	usedFunctions := map[string]bool{}
	usedErrors := map[ErrorClass]bool{}
	usedLocations := map[Location]bool{}
	for _, c := range cases {
		for _, fn := range templateFunctions(c.Template) {
			usedFunctions[fn] = true
		}
		usedErrors[c.WantError] = true
		usedLocations[c.Location] = true
		if strings.Contains(templateText(c.Template), "request.body") && c.RequestBody == nil {
			t.Errorf("%q: references request.body but has no request_body", c.Name)
		}
	}
	for _, fn := range builtinFunctions {
		if !usedFunctions[fn] {
			t.Errorf("function %q is not covered by any case", fn)
		}
	}
	for _, class := range ErrorClasses {
		if !usedErrors[class] {
			t.Errorf("error class %q is not covered by any case", class)
		}
	}
	for _, loc := range []Location{LocationBody, LocationHeader, LocationURL} {
		if !usedLocations[loc] {
			t.Errorf("location %q is not covered by any case", loc)
		}
	}
}

// derivedWants recomputes golden values from case inputs with the standard
// library, so a wrong hash, HMAC, encoding or timestamp in the YAML fails here.
var derivedWants = map[string]func(TemplateCase) any{
	"chain lower trim sha256": func(c TemplateCase) any {
		email := c.Event.Payload["email"].(string)
		return map[string]any{"email_hash": sha256Hex(strings.TrimSpace(strings.ToLower(email)))}
	},
	"sha256 stringifies a number input first": func(c TemplateCase) any {
		return sha256Hex(strconv.FormatFloat(c.Event.Payload["amount"].(float64), 'f', -1, 64))
	},
	"base64 of event field": func(c TemplateCase) any {
		return base64.StdEncoding.EncodeToString([]byte(c.Event.SourceEventID))
	},
	"base64 basic auth from secret": func(c TemplateCase) any {
		return "Basic " + base64.StdEncoding.EncodeToString([]byte(c.Secrets["BASIC_CREDENTIALS"]))
	},
	"header hmac_sha256_hex signs request body": func(c TemplateCase) any {
		return "sha256=" + hex.EncodeToString(hmacSHA256(c.Secrets["SIGNING_KEY"], *c.RequestBody))
	},
	"header hmac_sha256_base64 signs request body": func(c TemplateCase) any {
		return base64.StdEncoding.EncodeToString(hmacSHA256(c.Secrets["SIGNING_KEY"], *c.RequestBody))
	},
	"urlencode uses query escaping": func(c TemplateCase) any {
		return map[string]any{"query": "q=" + url.QueryEscape(c.Event.Payload["note"].(string))}
	},
	"url expression is path-segment escaped and literals are untouched": func(c TemplateCase) any {
		return "https://api.example.com/v1/orders/" + url.PathEscape(c.Event.Payload["order_id"].(string)) +
			"/status?src=" + url.PathEscape(c.Event.Source)
	},
	"format_time rfc3339 drops sub-second precision": func(c TemplateCase) any {
		return c.Event.ReceivedAt.UTC().Truncate(time.Second).Format(time.RFC3339)
	},
	"format_time unix in header": func(c TemplateCase) any {
		return strconv.FormatInt(c.Event.OccurredAt.Unix(), 10)
	},
	"format_time unix accepts an rfc3339 string with offset": func(c TemplateCase) any {
		ts, err := time.Parse(time.RFC3339, c.Event.Payload["local_paid_at"].(string))
		if err != nil {
			return err.Error()
		}
		return strconv.FormatInt(ts.Unix(), 10)
	},
	"format_time unix_ms embedded": func(c TemplateCase) any {
		return "ts=" + strconv.FormatInt(c.Event.ReceivedAt.UnixMilli(), 10)
	},
}

func TestTemplateGoldenValuesAreRecomputed(t *testing.T) {
	cases, err := LoadTemplateCases(TemplateCasesPath())
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	byName := make(map[string]TemplateCase, len(cases))
	for _, c := range cases {
		byName[c.Name] = c
	}

	t.Run("every digest case has a recomputation", func(t *testing.T) {
		for _, c := range cases {
			if c.ExpectsError() || !usesAny(c.Template, digestFunctions) {
				continue
			}
			if _, ok := derivedWants[c.Name]; !ok {
				t.Errorf("%q uses a digest function but its want is not recomputed", c.Name)
			}
		}
	})

	for name, derive := range derivedWants {
		t.Run(name, func(t *testing.T) {
			c, ok := byName[name]
			if !ok {
				t.Fatalf("case %q not found", name)
			}
			// Act
			want := derive(c)
			// Assert
			if !reflect.DeepEqual(c.Want, want) {
				t.Fatalf("golden want = %#v, recomputed %#v", c.Want, want)
			}
		})
	}
}

func TestSuccessCases(t *testing.T) {
	cases, err := LoadSuccessCases(SuccessCasesPath())
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(cases) < minSuccessCases {
		t.Fatalf("got %d success cases, want >= %d", len(cases), minSuccessCases)
	}

	usedOps := map[Op]bool{}
	usedNetwork := map[NetworkError]bool{}
	usedOutcomes := map[domain.Outcome]bool{}
	for _, c := range cases {
		for _, r := range c.Success.Body {
			usedOps[r.Op] = true
		}
		usedNetwork[c.NetworkError] = true
		usedOutcomes[c.WantOutcome] = true
	}
	for _, op := range Ops {
		if !usedOps[op] {
			t.Errorf("operator %q is not covered", op)
		}
	}
	for _, ne := range NetworkErrors {
		if !usedNetwork[ne] {
			t.Errorf("network error %q is not covered", ne)
		}
	}
	for _, o := range []domain.Outcome{domain.OutcomeSuccess, domain.OutcomeRetryable, domain.OutcomePermanent} {
		if !usedOutcomes[o] {
			t.Errorf("outcome %q is not covered", o)
		}
	}
}

func TestSuccessCasesContainPoCS3(t *testing.T) {
	cases, err := LoadSuccessCases(SuccessCasesPath())
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	for _, c := range cases {
		if c.Response == nil || c.Response.Status != 200 || c.Response.Body != `{"code":1001}` {
			continue
		}
		rules := c.Success.Body
		if len(rules) == 1 && rules[0].Path == "code" && rules[0].Op == OpEq && rules[0].Value == float64(0) &&
			c.WantOutcome == domain.OutcomePermanent {
			return
		}
	}
	t.Fatal(`no case: 200 + {"code":1001} with rule code eq 0 → permanent`)
}

// referenceDelay is the §7.4 formula with jitter 0.
func referenceDelay(c RetryCase) time.Duration {
	b := c.Backoff
	base := float64(b.Initial) * math.Pow(b.Multiplier, float64(c.RetryNumber-1))
	delay := time.Duration(math.Min(float64(b.Max), base))
	if c.StatusCode != 429 && c.StatusCode != 503 {
		return delay
	}
	secs, err := strconv.Atoi(c.RetryAfter)
	if err != nil || secs < 0 {
		return delay
	}
	return min(b.Max, max(delay, time.Duration(secs)*time.Second))
}

func TestRetryCases(t *testing.T) {
	cases, err := LoadRetryCases(RetryCasesPath())
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(cases) < minRetryCases {
		t.Fatalf("got %d retry cases, want >= %d", len(cases), minRetryCases)
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			// Act
			got := referenceDelay(c)
			// Assert
			if got != c.WantDelay {
				t.Fatalf("reference delay = %v, golden want_delay = %v", got, c.WantDelay)
			}
		})
	}
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func hmacSHA256(key, msg string) []byte {
	m := hmac.New(sha256.New, []byte(key))
	m.Write([]byte(msg))
	return m.Sum(nil)
}

// templateText concatenates every string leaf of a template value.
func templateText(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case []any:
		parts := make([]string, 0, len(t))
		for _, item := range t {
			parts = append(parts, templateText(item))
		}
		return strings.Join(parts, "\n")
	case map[string]any:
		parts := make([]string, 0, len(t))
		for _, item := range t {
			parts = append(parts, templateText(item))
		}
		return strings.Join(parts, "\n")
	default:
		return ""
	}
}

// templateFunctions returns the function names piped in a template's
// well-formed expressions (the first word of every segment after a `|`).
func templateFunctions(v any) []string {
	var names []string
	for _, m := range expressionPattern.FindAllStringSubmatch(templateText(v), -1) {
		segments := strings.Split(m[1], "|")
		for _, seg := range segments[1:] {
			if fields := strings.Fields(seg); len(fields) > 0 {
				names = append(names, fields[0])
			}
		}
	}
	return names
}

func usesAny(v any, functions []string) bool {
	for _, fn := range templateFunctions(v) {
		for _, want := range functions {
			if fn == want {
				return true
			}
		}
	}
	return false
}
