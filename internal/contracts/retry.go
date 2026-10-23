package contracts

import (
	"errors"
	"fmt"
	"regexp"
	"time"
)

// configDuration is the duration format allowed in hub configuration (§11).
var configDuration = regexp.MustCompile(`^[0-9]+(ms|s|m|h)$`)

// Backoff mirrors `policy.retry.backoff`.
type Backoff struct {
	Initial    time.Duration
	Multiplier float64
	Max        time.Duration
	Jitter     float64
}

// RetryCase is one golden case for the retry delay calculator (§7.4).
type RetryCase struct {
	Name        string
	Backoff     Backoff
	RetryNumber int    // n ≥ 1
	StatusCode  int    // 0 when the attempt had no HTTP response
	RetryAfter  string // raw Retry-After header value; empty when absent
	WantDelay   time.Duration
}

type rawBackoff struct {
	Initial    string  `yaml:"initial"`
	Multiplier float64 `yaml:"multiplier"`
	Max        string  `yaml:"max"`
	Jitter     float64 `yaml:"jitter"`
}

type rawRetryCase struct {
	Name        string     `yaml:"name"`
	Backoff     rawBackoff `yaml:"backoff"`
	RetryNumber int        `yaml:"retry_number"`
	StatusCode  int        `yaml:"status_code"`
	RetryAfter  string     `yaml:"retry_after"`
	WantDelay   string     `yaml:"want_delay"`
}

func (r rawRetryCase) caseName() string { return r.Name }

// LoadRetryCases loads and validates retry-delay golden cases.
func LoadRetryCases(path string) ([]RetryCase, error) {
	return loadCases(path, convertRetryCase)
}

func convertRetryCase(r rawRetryCase) (RetryCase, error) {
	if err := requireName(r.Name); err != nil {
		return RetryCase{}, err
	}
	backoff, err := convertBackoff(r.Backoff)
	if err != nil {
		return RetryCase{}, fmt.Errorf("backoff: %w", err)
	}
	if r.RetryNumber < 1 {
		return RetryCase{}, fmt.Errorf("retry_number %d: must be >= 1", r.RetryNumber)
	}
	if r.StatusCode != 0 && !isStatusCode(r.StatusCode) {
		return RetryCase{}, fmt.Errorf("status_code %d: want 100-599", r.StatusCode)
	}
	want, err := time.ParseDuration(r.WantDelay)
	if err != nil {
		return RetryCase{}, fmt.Errorf("want_delay: %w", err)
	}
	return RetryCase{
		Name:        r.Name,
		Backoff:     backoff,
		RetryNumber: r.RetryNumber,
		StatusCode:  r.StatusCode,
		RetryAfter:  r.RetryAfter,
		WantDelay:   want,
	}, nil
}

func convertBackoff(r rawBackoff) (Backoff, error) {
	initial, err := parseConfigDuration(r.Initial)
	if err != nil {
		return Backoff{}, fmt.Errorf("initial: %w", err)
	}
	maxDelay, err := parseConfigDuration(r.Max)
	if err != nil {
		return Backoff{}, fmt.Errorf("max: %w", err)
	}
	if initial <= 0 || maxDelay < initial {
		return Backoff{}, errors.New("want 0 < initial <= max")
	}
	if r.Multiplier < 1 {
		return Backoff{}, fmt.Errorf("multiplier %v: must be >= 1", r.Multiplier)
	}
	if r.Jitter != 0 {
		return Backoff{}, fmt.Errorf("jitter %v: golden cases require 0", r.Jitter)
	}
	return Backoff{Initial: initial, Multiplier: r.Multiplier, Max: maxDelay, Jitter: r.Jitter}, nil
}

func parseConfigDuration(s string) (time.Duration, error) {
	if !configDuration.MatchString(s) {
		return 0, fmt.Errorf("%q: want <digits>(ms|s|m|h)", s)
	}
	return time.ParseDuration(s)
}
