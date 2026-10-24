package store

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"messagehub/internal/config"
	"messagehub/internal/domain"
)

// blockedBySQL finds, for each given delivery, the earliest predecessor in
// the same (destination_id, ordering_key) that is not yet ordering-settled
// (§6). Settled status sets come from the domain, never written in SQL:
// $3 when DEAD blocks, $4 when DEAD is skipped. Served by deliveries_ordering_idx.
// The blocker's source is returned so scoped callers never see another
// source's delivery id.
const blockedBySQL = `SELECT x.id, b.id, b.source
FROM unnest($1::uuid[], $2::bool[]) AS x(id, skip_dead)
JOIN deliveries d ON d.id = x.id
CROSS JOIN LATERAL (
	SELECT p.id, pe.source FROM deliveries p JOIN events pe ON pe.id = p.event_id
	WHERE p.destination_id = d.destination_id
	  AND p.ordering_key = d.ordering_key
	  AND p.ingest_seq < d.ingest_seq
	  AND NOT (p.status = ANY (CASE WHEN x.skip_dead THEN $4::text[] ELSE $3::text[] END))
	ORDER BY p.ingest_seq
	LIMIT 1
) b`

// blockedBy derives blocked_by for the deliveries that wait to be claimed
// under a per_key policy, in one query per page. A delivery whose policy
// was removed from the config gets none. Ordering scopes are shared across
// sources (§6), but a scoped caller only learns blockers of its own source
// (§9): a blocker from another source is reported as none.
func (s *Store) blockedBy(ctx context.Context, scope Scope, deliveries []domain.Delivery) (map[uuid.UUID]uuid.UUID, error) {
	cfg := s.currentConfig()
	var ids []uuid.UUID
	var skipDead []bool
	for _, d := range deliveries {
		if onDead, ok := orderedPolicy(cfg, d); ok {
			ids = append(ids, d.ID)
			skipDead = append(skipDead, onDead == domain.PredecessorDeadSkip)
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx, blockedBySQL, ids, skipDead,
		settledStatuses(domain.PredecessorDeadBlock), settledStatuses(domain.PredecessorDeadSkip))
	if err != nil {
		return nil, fmt.Errorf("derive blocked_by: %w", err)
	}
	out := make(map[uuid.UUID]uuid.UUID, len(ids))
	var id, blocker uuid.UUID
	var blockerSource string
	_, err = pgx.ForEachRow(rows, []any{&id, &blocker, &blockerSource}, func() error {
		if scope.Source == "" || scope.Source == blockerSource {
			out[id] = blocker
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("derive blocked_by: %w", err)
	}
	return out, nil
}

// orderedPolicy reports whether d waits for a claim under a per_key policy.
func orderedPolicy(cfg *config.Config, d domain.Delivery) (domain.OnPredecessorDead, bool) {
	if !d.HasOrderingKey() || !awaitsClaim(d.Status) {
		return "", false
	}
	p, ok := cfg.Policy(d.PolicyID)
	if !ok || p.Ordering.Mode != config.OrderingPerKey {
		return "", false
	}
	return p.Ordering.OnPredecessorDead, true
}

// awaitsClaim reports whether a Claim may still move d out of status s.
func awaitsClaim(s domain.DeliveryStatus) bool {
	_, err := domain.Transition(s, domain.TriggerClaim)
	return err == nil
}

func settledStatuses(onDead domain.OnPredecessorDead) []string {
	var out []string
	for _, st := range domain.AllStatuses {
		if st.IsOrderingSettled(onDead) {
			out = append(out, string(st))
		}
	}
	return out
}
