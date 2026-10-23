package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// durationPattern mirrors the schema's duration format (§11).
var durationPattern = regexp.MustCompile(`^([0-9]+)(ms|s|m|h)$`)

var durationUnits = map[string]time.Duration{
	"ms": time.Millisecond,
	"s":  time.Second,
	"m":  time.Minute,
	"h":  time.Hour,
}

// errDurationRange reports a duration too large to represent.
var errDurationRange = errors.New("duration out of range")

// Duration is a time.Duration written as "<digits><ms|s|m|h>", e.g. "5s".
// It keeps its original text so the config hash reflects the file.
type Duration struct {
	d    time.Duration
	text string
}

// ParseDuration parses the config duration format.
func ParseDuration(s string) (Duration, error) {
	m := durationPattern.FindStringSubmatch(s)
	if m == nil {
		return Duration{}, fmt.Errorf("invalid duration %q: must match %s", s, durationPattern.String())
	}
	n, err := strconv.ParseInt(m[1], 10, 64)
	unit := durationUnits[m[2]]
	if err != nil || n > int64(time.Duration(1<<63-1)/unit) {
		return Duration{}, fmt.Errorf("invalid duration %q: %w", s, errDurationRange)
	}
	return Duration{d: time.Duration(n) * unit, text: s}, nil
}

// Std returns the value as a time.Duration.
func (d Duration) Std() time.Duration { return d.d }

// String returns the original text form.
func (d Duration) String() string { return d.text }

// MarshalJSON writes the original text form.
func (d Duration) MarshalJSON() ([]byte, error) { return json.Marshal(d.text) }

// UnmarshalJSON accepts a JSON string in the config duration format.
func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("duration must be a string: %w", err)
	}
	parsed, err := ParseDuration(s)
	if err != nil {
		return err
	}
	*d = parsed
	return nil
}

// UnmarshalYAML accepts a YAML scalar in the config duration format.
func (d *Duration) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.ScalarNode {
		return fmt.Errorf("duration must be a scalar, got %v", node.Tag)
	}
	parsed, err := ParseDuration(node.Value)
	if err != nil {
		return err
	}
	*d = parsed
	return nil
}

// statusClassSuffix is the suffix of a status class such as "2xx".
const statusClassSuffix = "xx"

// statusClassDivisor maps a status code to its class digit.
const statusClassDivisor = 100

// StatusMatcher is an exact status code (e.g. 200) or a class (e.g. "2xx").
// Exactly one of Code or Class is non-zero.
type StatusMatcher struct {
	Code  int
	Class int
}

// Matches reports whether the HTTP status code satisfies the matcher.
func (m StatusMatcher) Matches(code int) bool {
	if m.Class != 0 {
		return code/statusClassDivisor == m.Class
	}
	return code == m.Code
}

// String returns "200" or "2xx".
func (m StatusMatcher) String() string {
	if m.Class != 0 {
		return strconv.Itoa(m.Class) + statusClassSuffix
	}
	return strconv.Itoa(m.Code)
}

// MarshalJSON writes an integer for codes and a string for classes.
func (m StatusMatcher) MarshalJSON() ([]byte, error) {
	if m.Class != 0 {
		return json.Marshal(m.String())
	}
	return json.Marshal(m.Code)
}

// UnmarshalJSON accepts an integer code or a "Nxx" class string. Range
// checks are left to the JSON Schema.
func (m *StatusMatcher) UnmarshalJSON(b []byte) error {
	var code int
	if err := json.Unmarshal(b, &code); err == nil {
		*m = StatusMatcher{Code: code}
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("status must be an integer or a class string: %w", err)
	}
	digit, ok := strings.CutSuffix(s, statusClassSuffix)
	class, err := strconv.Atoi(digit)
	if !ok || err != nil || len(digit) != 1 {
		return fmt.Errorf("invalid status class %q", s)
	}
	*m = StatusMatcher{Class: class}
	return nil
}
