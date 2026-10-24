package domain

import "strings"

// Names whose values are credentials (docs/design/domain-model.md §10, §11).
// The same rule drives config validation (no literal credentials) and
// attempt-record masking, so the two can never disagree.
var (
	sensitiveExactNames = map[string]bool{
		"authorization": true, "proxy-authorization": true, "cookie": true,
		"set-cookie": true, "key": true,
	}
	sensitiveNameParts = []string{
		"token", "secret", "password", "passwd", "signature", "credential",
		"api-key", "apikey", "access-key", "private-key",
	}
	// Well-known names that look like credentials but are not, checked first.
	nonSensitiveNameParts = []string{"idempotency"}
)

const sensitiveSuffix = "-key"

// IsSensitiveName reports whether a header name, URL query parameter name or
// body field name conventionally carries a credential. Matching ignores case
// and treats '_' like '-'.
func IsSensitiveName(name string) bool {
	n := strings.ReplaceAll(strings.ToLower(strings.TrimSpace(name)), "_", "-")
	if n == "" {
		return false
	}
	for _, part := range nonSensitiveNameParts {
		if strings.Contains(n, part) {
			return false
		}
	}
	if sensitiveExactNames[n] || strings.HasSuffix(n, sensitiveSuffix) {
		return true
	}
	for _, part := range sensitiveNameParts {
		if strings.Contains(n, part) {
			return true
		}
	}
	return false
}
