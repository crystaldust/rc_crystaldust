package domain

import (
	"time"

	"github.com/google/uuid"
)

// DeliveryStatus is the state of one Event x Destination delivery (§5.1).
type DeliveryStatus string

const (
	StatusPending        DeliveryStatus = "PENDING"
	StatusInFlight       DeliveryStatus = "IN_FLIGHT"
	StatusRetryScheduled DeliveryStatus = "RETRY_SCHEDULED"
	StatusSucceeded      DeliveryStatus = "SUCCEEDED"
	StatusDead           DeliveryStatus = "DEAD"
	StatusSkipped        DeliveryStatus = "SKIPPED"
)

// AllStatuses lists every status, in lifecycle order.
var AllStatuses = []DeliveryStatus{
	StatusPending, StatusInFlight, StatusRetryScheduled,
	StatusSucceeded, StatusDead, StatusSkipped,
}

// IsTerminal reports whether no automatic transition leaves this status.
func (s DeliveryStatus) IsTerminal() bool {
	return s == StatusSucceeded || s == StatusDead || s == StatusSkipped
}

// OnPredecessorDead controls ordered delivery after a predecessor dies (§6).
type OnPredecessorDead string

const (
	PredecessorDeadBlock OnPredecessorDead = "block"
	PredecessorDeadSkip  OnPredecessorDead = "skip"
)

// IsOrderingSettled reports whether a delivery in this status no longer
// holds back later deliveries with the same (destination, ordering_key).
func (s DeliveryStatus) IsOrderingSettled(onDead OnPredecessorDead) bool {
	switch s {
	case StatusSucceeded, StatusSkipped:
		return true
	case StatusDead:
		return onDead == PredecessorDeadSkip
	default:
		return false
	}
}

// Delivery is one Event x Destination delivery unit.
type Delivery struct {
	ID             uuid.UUID
	EventID        uuid.UUID
	DestinationID  string
	PolicyID       string
	Status         DeliveryStatus
	OrderingKey    string
	IngestSeq      int64
	AttemptCount   int
	ReplayCount    int
	ScheduledAt    time.Time
	LeaseOwner     string
	LeaseExpiresAt *time.Time
	LastAttemptAt  *time.Time
	LastOutcome    Outcome
	LastStatusCode *int
	LastError      string
	CreatedAt      time.Time
	UpdatedAt      time.Time
	CompletedAt    *time.Time
}

// HasOrderingKey reports whether the delivery participates in ordering.
func (d Delivery) HasOrderingKey() bool {
	return d.OrderingKey != ""
}
