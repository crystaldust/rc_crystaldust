package outbound

import (
	"bytes"
	"encoding/json"
	"net/url"
	"strings"
	"unicode/utf8"

	"messagehub/internal/domain"
	"messagehub/internal/template"
)

// BuildRequestRecord turns the masked render into what an attempt stores
// (§10, ADR-0008 D2). On top of the expression masking already in
// r.Masked, values under credential names are replaced: whole headers,
// query parameter values and body values. Masking happens before
// truncation, so a cut never leaves part of a secret.
func BuildRequestRecord(r template.Rendered, method string) domain.RecordedRequest {
	m := r.Masked
	headers := make(map[string]string, len(m.Headers))
	for name, v := range m.Headers {
		if domain.IsSensitiveName(name) {
			v = domain.MaskedValue
		}
		headers[name] = v
	}
	body, truncated := recordBody(maskedBodyText(m))
	return domain.RecordedRequest{
		Method:    method,
		URL:       maskQuery(m.URL),
		Headers:   headers,
		Body:      body,
		Truncated: truncated,
	}
}

// BuildResponseRecord stores the vendor response. A JSON body has values
// under credential names masked, as a vendor may echo credentials back;
// anything else is stored as received (ADR-0008 residual risk). Either way
// the body is truncated.
func BuildResponseRecord(status int, contentType string, body []byte) domain.RecordedResponse {
	text, truncated := recordBody(maskResponseBody(body))
	return domain.RecordedResponse{
		StatusCode:  status,
		ContentType: contentType,
		Body:        text,
		Truncated:   truncated,
	}
}

func maskedBodyText(m template.Request) string {
	if m.BodyBytes == nil {
		return ""
	}
	masked, changed := maskByName(m.Body)
	if !changed {
		return string(m.BodyBytes)
	}
	b, err := template.MarshalJSON(masked)
	if err != nil {
		// The same value marshalled once already; never print it unmasked.
		return domain.MaskedValue
	}
	return string(b)
}

func maskResponseBody(body []byte) string {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var doc any
	if err := dec.Decode(&doc); err != nil || dec.More() {
		return string(body)
	}
	masked, changed := maskByName(doc)
	if !changed {
		return string(body)
	}
	b, err := template.MarshalJSON(masked)
	if err != nil {
		return domain.MaskedValue
	}
	return string(b)
}

// maskByName returns a copy of v in which every value under a credential
// key is replaced, together with whether anything was replaced. The input
// is never modified.
func maskByName(v any) (any, bool) {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		changed := false
		for k, child := range t {
			if domain.IsSensitiveName(k) {
				out[k], changed = domain.MaskedValue, true
				continue
			}
			c, ch := maskByName(child)
			out[k], changed = c, changed || ch
		}
		return out, changed
	case []any:
		out := make([]any, len(t))
		changed := false
		for i, child := range t {
			c, ch := maskByName(child)
			out[i], changed = c, changed || ch
		}
		return out, changed
	default:
		return v, false
	}
}

// maskQuery replaces the values of query parameters with credential names.
// The rest of the URL is kept byte for byte.
func maskQuery(raw string) string {
	base, query, hasQuery := strings.Cut(raw, "?")
	if !hasQuery {
		return raw
	}
	query, fragment, hasFragment := strings.Cut(query, "#")
	pairs := strings.Split(query, "&")
	for i, pair := range pairs {
		name, _, hasValue := strings.Cut(pair, "=")
		decoded, err := url.QueryUnescape(name)
		if err != nil {
			decoded = name
		}
		if hasValue && domain.IsSensitiveName(decoded) {
			pairs[i] = name + "=" + domain.MaskedValue
		}
	}
	out := base + "?" + strings.Join(pairs, "&")
	if hasFragment {
		out += "#" + fragment
	}
	return out
}

// recordBody makes a body storable in jsonb (valid UTF-8, no NUL) and cuts
// it to domain.MaxRecordedBodyBytes on a character boundary.
func recordBody(s string) (string, bool) {
	s = strings.ReplaceAll(strings.ToValidUTF8(s, string(utf8.RuneError)), "\x00", string(utf8.RuneError))
	if len(s) <= domain.MaxRecordedBodyBytes {
		return s, false
	}
	cut := domain.MaxRecordedBodyBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut], true
}
