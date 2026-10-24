//go:build integration

package store

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"messagehub/internal/domain"
)

// planRows is enough data for the planner to prefer indexes (plan Task 4).
const planRows = 10000

// seedVolume inserts planRows events with one delivery each, spread over
// event types, destinations and statuses, then refreshes statistics.
func seedVolume(t *testing.T, f fixture) {
	t.Helper()
	ctx := context.Background()
	stmts := []string{
		`INSERT INTO events (id, source, source_event_id, event_type, occurred_at, received_at,
			routing_status, matched_route_ids, routing_config_hash)
		SELECT gen_random_uuid(), 'order-service', 'evt-' || g,
			(ARRAY['order.paid','order.refunded','user.registered','order.purchased'])[1 + g % 4],
			now(), now() - g * interval '1 second',
			CASE WHEN g % 10 = 0 THEN 'unrouted' ELSE 'routed' END,
			CASE WHEN g % 10 = 0 THEN '{}'::text[] ELSE '{test-route}'::text[] END, 'hash'
		FROM generate_series(1, $1) AS g`,
		`INSERT INTO deliveries (id, event_id, destination_id, policy_id, status, ingest_seq, created_at)
		SELECT gen_random_uuid(), e.id,
			(ARRAY['inventory','data-warehouse','crm','ad-platform'])[1 + e.ingest_seq % 4], 'default',
			(ARRAY['PENDING','SUCCEEDED','DEAD','RETRY_SCHEDULED'])[1 + e.ingest_seq % 4],
			e.ingest_seq, e.received_at
		FROM events e WHERE e.routing_status = 'routed'`,
		`ANALYZE events`, `ANALYZE deliveries`,
	}
	for i, stmt := range stmts {
		var err error
		if i == 0 {
			_, err = f.pool.Exec(ctx, stmt, planRows)
		} else {
			_, err = f.pool.Exec(ctx, stmt)
		}
		if err != nil {
			t.Fatalf("seed volume: %v", err)
		}
	}
}

func explain(t *testing.T, f fixture, query string, args []any) string {
	t.Helper()
	rows, err := f.pool.Query(context.Background(), "EXPLAIN "+query, args...)
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	defer rows.Close()
	var lines []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatalf("scan plan: %v", err)
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func TestListQueriesUseIndexes(t *testing.T) {
	// Arrange
	f := newFixture(t)
	seedVolume(t, f)
	cursor := &Cursor{Time: time.Now().Add(-time.Hour), ID: uuid.New()}
	type built struct {
		query string
		args  []any
	}
	events := func(s Scope, flt EventFilter) built { q, a, _ := eventsQuery(s, flt); return built{q, a} }
	deliveries := func(s Scope, flt DeliveryFilter) built { q, a, _ := deliveriesQuery(s, flt); return built{q, a} }
	tests := []struct {
		name  string
		q     built
		table string
	}{
		{"events newest first", events(Scope{}, EventFilter{}), "events"},
		{"events next page", events(Scope{}, EventFilter{Cursor: cursor}), "events"},
		{"events by event_type", events(Scope{}, EventFilter{EventType: "order.paid"}), "events"},
		{"events unrouted", events(Scope{}, EventFilter{RoutingStatus: domain.RoutingUnrouted}), "events"},
		{"deliveries by destination and status", deliveries(Scope{}, DeliveryFilter{DestinationID: "crm", Status: domain.StatusDead}), "deliveries"},
		{"deliveries of one event", deliveries(Scope{}, DeliveryFilter{EventID: &cursor.ID}), "deliveries"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			plan := explain(t, f, tt.q.query, tt.q.args)

			// Assert
			if strings.Contains(plan, "Seq Scan on "+tt.table) {
				t.Errorf("plan scans %s sequentially:\n%s", tt.table, plan)
			}
		})
	}
}
