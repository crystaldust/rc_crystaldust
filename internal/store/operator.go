package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"messagehub/internal/domain"
)

// OperatorActionKind is the audit action name (operator_actions.action).
type OperatorActionKind string

const (
	ActionReplay OperatorActionKind = "replay"
	ActionSkip   OperatorActionKind = "skip"
)

// OperatorAction is one audit row of a replay or skip.
type OperatorAction struct {
	ID         uuid.UUID
	Action     OperatorActionKind
	DeliveryID uuid.UUID
	Actor      string
	Reason     string
	FromStatus domain.DeliveryStatus
	ToStatus   domain.DeliveryStatus
	CreatedAt  time.Time
}

// OperatorInput identifies the delivery, the operator and the reason.
type OperatorInput struct {
	ActionID   uuid.UUID
	DeliveryID uuid.UUID
	Actor      string
	Reason     string
}

// Replay applies the Replay transition (§5.3): a new retry budget and an
// immediate schedule.
const replaySQL = `UPDATE deliveries SET status = $3, replay_count = replay_count + 1,
	attempt_count = 0, scheduled_at = now(), lease_owner = NULL, lease_expires_at = NULL,
	completed_at = NULL, updated_at = now()
WHERE id = $1 AND status = $2`

// skipSQL applies the Skip transition.
const skipSQL = `UPDATE deliveries SET status = $3, completed_at = now(), updated_at = now()
WHERE id = $1 AND status = $2`

// Replay moves a terminal delivery back to PENDING.
func (s *Store) Replay(ctx context.Context, in OperatorInput) (OperatorAction, error) {
	return s.operate(ctx, in, ActionReplay, domain.TriggerReplay, replaySQL)
}

// Skip moves a DEAD delivery to SKIPPED, releasing ordered successors.
func (s *Store) Skip(ctx context.Context, in OperatorInput) (OperatorAction, error) {
	return s.operate(ctx, in, ActionSkip, domain.TriggerSkip, skipSQL)
}

// operate runs one operator action in a transaction: lock the row, check the
// transition with domain.Transition, apply it with a conditional update
// (ADR-0009 M2), write the audit row and notify workers once.
func (s *Store) operate(ctx context.Context, in OperatorInput, kind OperatorActionKind,
	trigger domain.Trigger, updateSQL string) (OperatorAction, error) {
	var action OperatorAction
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		from, err := lockStatus(ctx, tx, in.DeliveryID)
		if err != nil {
			return err
		}
		to, err := domain.Transition(from, trigger)
		if err != nil {
			return &StateError{Action: string(kind), Current: from}
		}
		tag, err := tx.Exec(ctx, updateSQL, in.DeliveryID, string(from), string(to))
		if err != nil {
			return fmt.Errorf("update delivery: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return &StateError{Action: string(kind), Current: from}
		}
		action, err = insertAction(ctx, tx, in, kind, from, to)
		if err != nil {
			return err
		}
		return notifyReady(ctx, tx)
	})
	if err != nil {
		return OperatorAction{}, fmt.Errorf("%s delivery %s: %w", kind, in.DeliveryID, classify(err))
	}
	return action, nil
}

func lockStatus(ctx context.Context, tx pgx.Tx, id uuid.UUID) (domain.DeliveryStatus, error) {
	var status string
	err := tx.QueryRow(ctx, `SELECT status FROM deliveries WHERE id = $1 FOR UPDATE`, id).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("lock delivery: %w", err)
	}
	return domain.DeliveryStatus(status), nil
}

func insertAction(ctx context.Context, tx pgx.Tx, in OperatorInput, kind OperatorActionKind,
	from, to domain.DeliveryStatus) (OperatorAction, error) {
	a := OperatorAction{ID: in.ActionID, Action: kind, DeliveryID: in.DeliveryID,
		Actor: in.Actor, Reason: in.Reason, FromStatus: from, ToStatus: to}
	err := tx.QueryRow(ctx, `INSERT INTO operator_actions (id, delivery_id, action, actor, reason, from_status)
		VALUES ($1, $2, $3, $4, $5, $6) RETURNING created_at`,
		a.ID, a.DeliveryID, string(kind), a.Actor, a.Reason, string(from)).Scan(&a.CreatedAt)
	if err != nil {
		return OperatorAction{}, fmt.Errorf("insert operator action: %w", err)
	}
	return a, nil
}
