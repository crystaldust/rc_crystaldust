package domain

import (
	"errors"
	"testing"
)

// legalTransitions mirrors docs/design/domain-model.md §5.2 row by row.
var legalTransitions = map[DeliveryStatus]map[Trigger]DeliveryStatus{
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

const (
	wantLegalTransitions   = 11
	wantIllegalTransitions = 37
)

func TestTransitionCoversEveryStatusTriggerPair(t *testing.T) {
	legal, illegal := 0, 0
	for _, from := range AllStatuses {
		for _, trigger := range AllTriggers {
			want, isLegal := legalTransitions[from][trigger]
			t.Run(string(from)+"/"+string(trigger), func(t *testing.T) {
				// Act
				got, err := Transition(from, trigger)

				// Assert
				if isLegal {
					if err != nil || got != want {
						t.Fatalf("Transition(%s, %s) = (%q, %v), want (%q, nil)", from, trigger, got, err, want)
					}
					return
				}
				if !errors.Is(err, ErrInvalidTransition) || got != "" {
					t.Fatalf("Transition(%s, %s) = (%q, %v), want ErrInvalidTransition", from, trigger, got, err)
				}
			})
			if isLegal {
				legal++
			} else {
				illegal++
			}
		}
	}
	if legal != wantLegalTransitions || illegal != wantIllegalTransitions {
		t.Fatalf("legal=%d illegal=%d, want %d and %d", legal, illegal, wantLegalTransitions, wantIllegalTransitions)
	}
}

func TestIsTerminal(t *testing.T) {
	want := map[DeliveryStatus]bool{
		StatusPending: false, StatusInFlight: false, StatusRetryScheduled: false,
		StatusSucceeded: true, StatusDead: true, StatusSkipped: true,
	}
	for _, s := range AllStatuses {
		if got := s.IsTerminal(); got != want[s] {
			t.Errorf("%s.IsTerminal() = %v, want %v", s, got, want[s])
		}
	}
}

func TestIsOrderingSettled(t *testing.T) {
	tests := []struct {
		status DeliveryStatus
		onDead OnPredecessorDead
		want   bool
	}{
		{StatusDead, PredecessorDeadBlock, false},
		{StatusDead, PredecessorDeadSkip, true},
		{StatusSkipped, PredecessorDeadBlock, true},
		{StatusSucceeded, PredecessorDeadBlock, true},
		{StatusRetryScheduled, PredecessorDeadSkip, false},
		{StatusPending, PredecessorDeadSkip, false},
		{StatusInFlight, PredecessorDeadSkip, false},
	}
	for _, tt := range tests {
		t.Run(string(tt.status)+"/"+string(tt.onDead), func(t *testing.T) {
			if got := tt.status.IsOrderingSettled(tt.onDead); got != tt.want {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestHasOrderingKey(t *testing.T) {
	if (Delivery{}).HasOrderingKey() {
		t.Fatal("empty ordering key reported as present")
	}
	if !(Delivery{OrderingKey: "sku-1"}).HasOrderingKey() {
		t.Fatal("ordering key reported as absent")
	}
}
