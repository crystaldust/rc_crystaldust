package backoff

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"messagehub/internal/config"
	"messagehub/internal/contracts"
)

var testNow = time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)

func TestGoldenRetryCases(t *testing.T) {
	cases, err := contracts.LoadRetryCases(contracts.RetryCasesPath())
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			// Arrange
			b := backoffOf(t, c.Backoff.Initial, c.Backoff.Max, c.Backoff.Multiplier, c.Backoff.Jitter)

			// Act
			got := Delay(b, c.RetryNumber, c.StatusCode, c.RetryAfter, testNow, nil)

			// Assert
			if got != c.WantDelay {
				t.Fatalf("delay = %v, want %v", got, c.WantDelay)
			}
		})
	}
}

func TestJitterBounds(t *testing.T) {
	b := backoffOf(t, 10*time.Second, time.Hour, 2, 0.2)
	tests := []struct {
		name string
		rnd  float64
		want time.Duration
	}{
		{"lowest", 0, 8 * time.Second},
		{"middle", 0.5, 10 * time.Second},
		{"highest", 1, 12 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got := Delay(b, 1, 0, "", testNow, func() float64 { return tt.rnd })

			// Assert
			if got != tt.want {
				t.Fatalf("delay = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestJitterNeverExceedsMax(t *testing.T) {
	// Arrange
	b := backoffOf(t, time.Minute, time.Minute, 2, 0.5)

	// Act
	got := Delay(b, 1, 0, "", testNow, func() float64 { return 1 })

	// Assert
	if got != time.Minute {
		t.Fatalf("delay = %v, want max 1m", got)
	}
}

func TestRetryAfterHTTPDate(t *testing.T) {
	// Arrange
	b := backoffOf(t, 5*time.Second, time.Hour, 2, 0)
	future := testNow.Add(90 * time.Second).Format(http.TimeFormat)
	past := testNow.Add(-time.Minute).Format(http.TimeFormat)

	// Act
	gotFuture := Delay(b, 1, http.StatusServiceUnavailable, future, testNow, nil)
	gotPast := Delay(b, 1, http.StatusServiceUnavailable, past, testNow, nil)

	// Assert
	if gotFuture != 90*time.Second {
		t.Errorf("future date: delay = %v, want 1m30s", gotFuture)
	}
	if gotPast != 5*time.Second {
		t.Errorf("past date: delay = %v, want computed 5s", gotPast)
	}
}

func TestParseRetryAfterRejectsInvalidValues(t *testing.T) {
	for _, v := range []string{"", "-1", "1.5", "soon", "99999999999999999999"} {
		t.Run(v, func(t *testing.T) {
			// Act
			_, ok := ParseRetryAfter(v, testNow)

			// Assert
			if ok {
				t.Fatalf("ParseRetryAfter(%q) accepted", v)
			}
		})
	}
}

func backoffOf(t *testing.T, initial, maxDelay time.Duration, multiplier, jitter float64) config.Backoff {
	t.Helper()
	return config.Backoff{
		Initial: durationOf(t, initial), Max: durationOf(t, maxDelay),
		Multiplier: multiplier, Jitter: jitter,
	}
}

// durationOf goes through the config parser so the test uses real values.
func durationOf(t *testing.T, d time.Duration) config.Duration {
	t.Helper()
	cd, err := config.ParseDuration(fmt.Sprintf("%dms", d.Milliseconds()))
	if err != nil {
		t.Fatal(err)
	}
	return cd
}
