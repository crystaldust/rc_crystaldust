package dispatch

import (
	"time"

	"messagehub/internal/config"
	"messagehub/internal/domain"
)

// LeaseMargin is added to a destination's policy timeout to form its lease,
// covering rendering, token fetches and the write of the result.
const LeaseMargin = 30 * time.Second

// targets is the per-destination claim input derived from one config
// snapshot, as parallel arrays for SQL unnest.
type targets struct {
	ids         []string
	maxInFlight []int32
	leaseMS     []int64
	// Destinations ordered per key, split by on_predecessor_dead.
	orderedBlock []string
	orderedSkip  []string
}

func targetsOf(cfg *config.Config) targets {
	var t targets
	for _, d := range cfg.Destinations() {
		p, ok := cfg.Policy(d.Policy)
		if !ok {
			continue // rejected by config validation; never claim blindly
		}
		t.ids = append(t.ids, d.ID)
		t.maxInFlight = append(t.maxInFlight, int32(d.MaxInFlight))
		t.leaseMS = append(t.leaseMS, (p.Timeout.Std() + LeaseMargin).Milliseconds())
		if p.Ordering.Mode != config.OrderingPerKey {
			continue
		}
		if p.Ordering.OnPredecessorDead == domain.PredecessorDeadSkip {
			t.orderedSkip = append(t.orderedSkip, d.ID)
		} else {
			t.orderedBlock = append(t.orderedBlock, d.ID)
		}
	}
	return t
}

// settledStatuses lists the statuses that no longer hold back successors
// (§6), derived from the domain rule rather than written out in SQL.
func settledStatuses(onDead domain.OnPredecessorDead) []string {
	var out []string
	for _, s := range domain.AllStatuses {
		if s.IsOrderingSettled(onDead) {
			out = append(out, string(s))
		}
	}
	return out
}

// claimableStatuses lists the statuses a Claim may leave, taken from the
// transition table so the SQL can never drift from §5.2.
func claimableStatuses() []string {
	var out []string
	for _, s := range domain.AllStatuses {
		if _, err := domain.Transition(s, domain.TriggerClaim); err == nil {
			out = append(out, string(s))
		}
	}
	return out
}
