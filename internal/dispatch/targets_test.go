package dispatch

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"messagehub/internal/config"
	"messagehub/internal/domain"
)

const targetsConfigYAML = `version: 1
principals:
  - { id: src, role: source, api_key_sha256_ref: env:HUB_KEY }
policies:
  - id: plain
    timeout: 5s
    retry:
      max_attempts: 3
      backoff: { initial: 1s, multiplier: 2.0, max: 1m, jitter: 0 }
      retry_on: { status: ["5xx"] }
    success: { status: ["2xx"] }
    ordering: { mode: none }
  - id: block
    timeout: 2s
    retry:
      max_attempts: 3
      backoff: { initial: 1s, multiplier: 2.0, max: 1m, jitter: 0 }
      retry_on: { status: ["5xx"] }
    success: { status: ["2xx"] }
    ordering: { mode: per_key }
  - id: skip
    timeout: 2s
    retry:
      max_attempts: 3
      backoff: { initial: 1s, multiplier: 2.0, max: 1m, jitter: 0 }
      retry_on: { status: ["5xx"] }
    success: { status: ["2xx"] }
    ordering: { mode: per_key, on_predecessor_dead: skip }
destinations:
  - { id: aa, policy: plain, request: { method: POST, url: "https://x.example.com/" } }
  - { id: bb, policy: block, max_in_flight: 3, request: { method: POST, url: "https://x.example.com/" } }
  - { id: cc, policy: skip, request: { method: POST, url: "https://x.example.com/" } }
routes:
  - { id: rt, event_type: a.b, destinations: [aa] }
`

func TestTargetsOfDerivesClaimInputFromConfig(t *testing.T) {
	// Arrange
	cfg, err := config.Parse([]byte(targetsConfigYAML))
	if err != nil {
		t.Fatal(err)
	}

	// Act
	got := targetsOf(cfg)

	// Assert
	want := targets{
		ids:          []string{"aa", "bb", "cc"},
		maxInFlight:  []int32{config.DefaultMaxInFlight, 3, config.DefaultMaxInFlight},
		leaseMS:      []int64{(5*time.Second + LeaseMargin).Milliseconds(), (2*time.Second + LeaseMargin).Milliseconds(), (2*time.Second + LeaseMargin).Milliseconds()},
		orderedBlock: []string{"bb"},
		orderedSkip:  []string{"cc"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("targets = %+v\nwant      %+v", got, want)
	}
}

func TestStatusListsFollowTheDomainRules(t *testing.T) {
	tests := []struct {
		name string
		got  []string
		want []string
	}{
		{"claimable", claimableStatuses(), []string{"PENDING", "RETRY_SCHEDULED"}},
		{"settled when dead blocks", settledStatuses(domain.PredecessorDeadBlock), []string{"SUCCEEDED", "SKIPPED"}},
		{"settled when dead is skipped", settledStatuses(domain.PredecessorDeadSkip), []string{"SUCCEEDED", "DEAD", "SKIPPED"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Assert
			if !reflect.DeepEqual(tt.got, tt.want) {
				t.Fatalf("got %v, want %v", tt.got, tt.want)
			}
		})
	}
}

func TestInlinedStatusListsMatchThePartialIndexes(t *testing.T) {
	// The partial indexes in db/migrations are defined on exactly these lists.
	if claimableSQL != "('PENDING','RETRY_SCHEDULED')" || inFlightSQL != "('IN_FLIGHT')" {
		t.Fatalf("claimable = %s, in flight = %s", claimableSQL, inFlightSQL)
	}
	if !strings.Contains(claimSQL, claimableSQL) || !strings.Contains(reclaimSQL, inFlightSQL) {
		t.Fatal("queries do not use the inlined lists")
	}
}

func TestClaimAndReclaimAreLegalTransitions(t *testing.T) {
	for _, s := range claimableStatuses() {
		if to, err := domain.Transition(domain.DeliveryStatus(s), domain.TriggerClaim); err != nil || to != domain.StatusInFlight {
			t.Fatalf("Claim from %s: %s, %v", s, to, err)
		}
	}
	if to, err := domain.Transition(domain.StatusInFlight, domain.TriggerReclaim); err != nil || to != domain.StatusInFlight {
		t.Fatalf("Reclaim: %s, %v", to, err)
	}
}

func TestAttemptTrigger(t *testing.T) {
	if AttemptTrigger(0) != domain.AttemptAuto || AttemptTrigger(2) != domain.AttemptReplay {
		t.Fatal("attempt trigger must be replay after any replay")
	}
}

func TestMillis(t *testing.T) {
	d := 1500 * time.Millisecond
	if got := millis(&d); got == nil || *got != 1500 {
		t.Fatalf("millis = %v", got)
	}
	if millis(nil) != nil {
		t.Fatal("nil delay must stay NULL")
	}
}
