-- deliveries tuning for the delivery engine (Phase 4).
--
-- Storage (ADR-0009 P2): every claim, completion and retry rewrites a
-- deliveries row, so leave free space in each page for HOT updates and
-- vacuum / analyze the table far more eagerly than the global defaults.
-- These are initial values; Phase 7 load tests calibrate them. fillfactor
-- only affects pages written from now on; no table rewrite is needed.
--
-- Claim index (domain-model.md §13.6): claiming is per destination (each
-- destination takes at most its free max_in_flight slots, §8), so the scan
-- for due deliveries is keyed by (destination_id, scheduled_at). It
-- replaces the Phase 2 index on scheduled_at alone, which nothing else uses.

-- +goose Up
ALTER TABLE deliveries SET (
    fillfactor = 80,
    autovacuum_vacuum_scale_factor = 0.02,
    autovacuum_analyze_scale_factor = 0.02
);

CREATE INDEX deliveries_claimable_destination_idx ON deliveries (destination_id, scheduled_at)
    WHERE status IN ('PENDING', 'RETRY_SCHEDULED');
DROP INDEX deliveries_claimable_scheduled_at_idx;

-- +goose Down
CREATE INDEX deliveries_claimable_scheduled_at_idx ON deliveries (scheduled_at)
    WHERE status IN ('PENDING', 'RETRY_SCHEDULED');
DROP INDEX deliveries_claimable_destination_idx;

ALTER TABLE deliveries RESET (
    fillfactor,
    autovacuum_vacuum_scale_factor,
    autovacuum_analyze_scale_factor
);
