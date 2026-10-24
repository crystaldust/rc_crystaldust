package domain

import "fmt"

// Trigger is an event that moves a Delivery between statuses (§5.2).
type Trigger string

const (
	TriggerClaim      Trigger = "Claim"
	TriggerReclaim    Trigger = "Reclaim"
	TriggerSucceed    Trigger = "Succeed"
	TriggerRetry      Trigger = "Retry"
	TriggerInfraRetry Trigger = "InfraRetry"
	TriggerGiveUp     Trigger = "GiveUp"
	TriggerReplay     Trigger = "Replay"
	TriggerSkip       Trigger = "Skip"
)

// AllTriggers lists every trigger.
var AllTriggers = []Trigger{
	TriggerClaim, TriggerReclaim, TriggerSucceed, TriggerRetry,
	TriggerInfraRetry, TriggerGiveUp, TriggerReplay, TriggerSkip,
}

// transitions is the complete set of legal moves; anything absent is illegal.
var transitions = map[DeliveryStatus]map[Trigger]DeliveryStatus{
	StatusPending:        {TriggerClaim: StatusInFlight},
	StatusRetryScheduled: {TriggerClaim: StatusInFlight},
	StatusInFlight: {
		TriggerReclaim:    StatusInFlight,
		TriggerSucceed:    StatusSucceeded,
		TriggerRetry:      StatusRetryScheduled,
		TriggerInfraRetry: StatusRetryScheduled,
		TriggerGiveUp:     StatusDead,
	},
	StatusDead:      {TriggerReplay: StatusPending, TriggerSkip: StatusSkipped},
	StatusSucceeded: {TriggerReplay: StatusPending},
	StatusSkipped:   {TriggerReplay: StatusPending},
}

// Transition returns the status reached by applying trigger to from.
// It is pure: it neither reads nor mutates any Delivery.
func Transition(from DeliveryStatus, trigger Trigger) (DeliveryStatus, error) {
	if to, ok := transitions[from][trigger]; ok {
		return to, nil
	}
	return "", fmt.Errorf("%w: %s on %s", ErrInvalidTransition, trigger, from)
}
