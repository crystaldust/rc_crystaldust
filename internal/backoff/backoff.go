// Package backoff computes retry delays (docs/design/domain-model.md §7.4,
// §13.3): capped exponential backoff with symmetric jitter, extended by a
// valid Retry-After on 429 / 503. It is pure; randomness and the current
// time are injected. It depends on config only.
package backoff

import (
	"math"
	"net/http"
	"strconv"
	"time"

	"messagehub/internal/config"
)

// Delay returns the wait before retry number retryNumber (1 = the retry
// after the first attempt). statusCode is 0 when there was no response;
// retryAfter is the raw header value. rnd returns a value in [0, 1) and
// spreads the jitter over [−jitter, +jitter].
func Delay(b config.Backoff, retryNumber int, statusCode int, retryAfter string, now time.Time, rnd func() float64) time.Duration {
	maxDelay := b.Max.Std()
	d := jittered(base(b, retryNumber), b.Jitter, rnd)
	if d > float64(maxDelay) {
		d = float64(maxDelay)
	}
	delay := time.Duration(d)
	if honoursRetryAfter(statusCode) {
		if ra, ok := ParseRetryAfter(retryAfter, now); ok && ra > delay {
			delay = min(ra, maxDelay)
		}
	}
	return delay
}

// base is min(max, initial × multiplier^(n−1)) in float nanoseconds; the
// float form cannot overflow for large n.
func base(b config.Backoff, n int) float64 {
	maxDelay := float64(b.Max.Std())
	v := float64(b.Initial.Std()) * math.Pow(b.Multiplier, float64(max(n, 1)-1))
	if v > maxDelay || math.IsInf(v, 1) || math.IsNaN(v) {
		return maxDelay
	}
	return v
}

func jittered(v, jitter float64, rnd func() float64) float64 {
	if jitter == 0 {
		return v
	}
	u := jitter * (2*rnd() - 1)
	return v * (1 + u)
}

func honoursRetryAfter(statusCode int) bool {
	return statusCode == http.StatusTooManyRequests || statusCode == http.StatusServiceUnavailable
}

// ParseRetryAfter accepts delay-seconds or an HTTP date relative to now.
// Negative, past or unparsable values are rejected.
func ParseRetryAfter(v string, now time.Time) (time.Duration, bool) {
	if v == "" {
		return 0, false
	}
	if secs, err := strconv.ParseInt(v, 10, 64); err == nil {
		if secs < 0 || secs > int64(math.MaxInt64/int64(time.Second)) {
			return 0, false
		}
		return time.Duration(secs) * time.Second, true
	}
	at, err := http.ParseTime(v)
	if err != nil {
		return 0, false
	}
	d := at.Sub(now)
	return d, d >= 0
}
