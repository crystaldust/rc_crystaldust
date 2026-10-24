-- Initial Message Hub schema (docs/design/domain-model.md, ADR-0003, ADR-0004).
-- Column names map 1:1 to internal/domain Event, Delivery and Attempt fields.
-- Enum CHECK values must stay identical to the domain string constants.

-- +goose Up

-- events: accepted, immutable envelopes plus Hub-assigned fields (§2, §3).
-- Not partitioned so UNIQUE (source, source_event_id) stays global (dedup key).
CREATE TABLE events (
    id                  uuid        PRIMARY KEY,
    ingest_seq          bigint      GENERATED ALWAYS AS IDENTITY UNIQUE,
    source              text        NOT NULL,
    source_event_id     text        NOT NULL,
    event_type          text        NOT NULL,
    occurred_at         timestamptz NOT NULL,
    received_at         timestamptz NOT NULL DEFAULT now(),
    idempotency_key     text        NULL,
    ordering_key        text        NULL,
    metadata            jsonb       NOT NULL DEFAULT '{}',
    routing_status      text        NOT NULL,
    matched_route_ids   text[]      NOT NULL DEFAULT '{}',
    routing_config_hash text        NOT NULL,
    CONSTRAINT events_source_event_id_key UNIQUE (source, source_event_id),
    CONSTRAINT events_routing_status_check
        CHECK (routing_status IN ('routed', 'unrouted')),
    -- routing_status and matched_route_ids can never contradict each other (§3).
    CONSTRAINT events_routing_consistency_check
        CHECK ((routing_status = 'unrouted') = (cardinality(matched_route_ids) = 0)),
    -- Absent optional strings are stored as NULL, never as ''.
    CONSTRAINT events_idempotency_key_check CHECK (idempotency_key <> ''),
    CONSTRAINT events_ordering_key_check CHECK (ordering_key <> ''),
    CONSTRAINT events_metadata_object_check CHECK (jsonb_typeof(metadata) = 'object')
);
COMMENT ON TABLE events IS 'Accepted events; dedup by (source, source_event_id); ingest_seq orders per-key delivery';

-- GET /v1/events filtered by event_type, newest first.
CREATE INDEX events_event_type_received_at_idx ON events (event_type, received_at DESC);
-- GET /v1/events unfiltered listing, newest first (cursor pagination).
CREATE INDEX events_received_at_idx ON events (received_at DESC);
-- Events sharing an ordering_key (troubleshooting ordered delivery).
CREATE INDEX events_ordering_key_idx ON events (ordering_key) WHERE ordering_key IS NOT NULL;
-- Unrouted events for troubleshooting and alerting (§3, Phase 5 metrics).
CREATE INDEX events_unrouted_received_at_idx ON events (received_at DESC)
    WHERE routing_status = 'unrouted';

-- event_payloads: payload kept apart from events so retention can purge it (ADR-0004).
CREATE TABLE event_payloads (
    event_id   uuid        PRIMARY KEY REFERENCES events (id) ON DELETE CASCADE,
    payload    jsonb       NOT NULL,
    expires_at timestamptz NULL,
    CONSTRAINT event_payloads_payload_object_check CHECK (jsonb_typeof(payload) = 'object')
);
COMMENT ON TABLE event_payloads IS 'Event payloads, one per event, purged by retention (Phase 5)';

-- Retention sweeper: payloads past expires_at.
CREATE INDEX event_payloads_expires_at_idx ON event_payloads (expires_at)
    WHERE expires_at IS NOT NULL;

-- deliveries: one Event x Destination delivery unit with its own state machine (§5).
CREATE TABLE deliveries (
    id               uuid        PRIMARY KEY,
    event_id         uuid        NOT NULL REFERENCES events (id) ON DELETE CASCADE,
    destination_id   text        NOT NULL,
    policy_id        text        NOT NULL,
    status           text        NOT NULL DEFAULT 'PENDING',
    ordering_key     text        NULL,
    ingest_seq       bigint      NOT NULL,
    attempt_count    integer     NOT NULL DEFAULT 0,
    replay_count     integer     NOT NULL DEFAULT 0,
    scheduled_at     timestamptz NOT NULL DEFAULT now(),
    lease_owner      text        NULL,
    lease_expires_at timestamptz NULL,
    last_attempt_at  timestamptz NULL,
    last_outcome     text        NULL,
    last_status_code integer     NULL,
    last_error       text        NULL,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    completed_at     timestamptz NULL,
    CONSTRAINT deliveries_event_destination_key UNIQUE (event_id, destination_id),
    CONSTRAINT deliveries_status_check CHECK (status IN (
        'PENDING', 'IN_FLIGHT', 'RETRY_SCHEDULED', 'SUCCEEDED', 'DEAD', 'SKIPPED')),
    CONSTRAINT deliveries_last_outcome_check CHECK (last_outcome IN (
        'success', 'retryable', 'permanent', 'render_error', 'infra_error')),
    CONSTRAINT deliveries_attempt_count_check CHECK (attempt_count >= 0),
    CONSTRAINT deliveries_replay_count_check CHECK (replay_count >= 0),
    CONSTRAINT deliveries_last_status_code_check CHECK (last_status_code BETWEEN 100 AND 599),
    CONSTRAINT deliveries_ordering_key_check CHECK (ordering_key <> '')
);
COMMENT ON TABLE deliveries IS 'Event x Destination delivery units; claimed with FOR UPDATE SKIP LOCKED (ADR-0003)';
COMMENT ON COLUMN deliveries.ingest_seq IS 'Copy of events.ingest_seq so per-key ordering needs no join (§6)';

-- Claim scan: due PENDING / RETRY_SCHEDULED deliveries by scheduled_at.
CREATE INDEX deliveries_claimable_scheduled_at_idx ON deliveries (scheduled_at)
    WHERE status IN ('PENDING', 'RETRY_SCHEDULED');
-- Lease reaper: IN_FLIGHT deliveries whose lease expired (Reclaim, §5.2).
CREATE INDEX deliveries_in_flight_lease_idx ON deliveries (lease_expires_at)
    WHERE status = 'IN_FLIGHT';
-- Ordered delivery: earliest unsettled predecessor per (destination_id, ordering_key)
-- by ingest_seq (§6); also used to derive blocked_by.
CREATE INDEX deliveries_ordering_idx ON deliveries (destination_id, ordering_key, ingest_seq)
    WHERE ordering_key IS NOT NULL;
-- max_in_flight: count IN_FLIGHT deliveries per destination at claim time (§8).
CREATE INDEX deliveries_in_flight_destination_idx ON deliveries (destination_id)
    WHERE status = 'IN_FLIGHT';
-- GET /v1/deliveries filtered by destination and status, newest first.
CREATE INDEX deliveries_destination_status_created_at_idx
    ON deliveries (destination_id, status, created_at DESC);
-- GET /v1/events/{id}: deliveries of one event. The UNIQUE (event_id, destination_id)
-- index can serve this too; kept as a narrow dedicated index per plan.
CREATE INDEX deliveries_event_id_idx ON deliveries (event_id);

-- attempts: append-only HTTP call log (§10), monthly range partitions on started_at
-- (ADR-0004). A runtime job creates monthly partitions (Phase 5); this migration only
-- creates the DEFAULT partition.
CREATE TABLE attempts (
    id              uuid        NOT NULL,
    -- FK from a partitioned table to a plain table (PG >= 12). Deleting a delivery has
    -- to cascade into every attempts partition, so bulk deletes scan all partitions;
    -- archiving and cleanup should detach / drop partitions instead (Phase 5).
    delivery_id     uuid        NOT NULL REFERENCES deliveries (id) ON DELETE CASCADE,
    number          integer     NOT NULL,
    trigger         text        NOT NULL,
    started_at      timestamptz NOT NULL,
    duration_ms     integer     NOT NULL DEFAULT 0,
    outcome         text        NOT NULL,
    status_code     integer     NULL,
    error           text        NULL,
    request         jsonb       NULL,
    response        jsonb       NULL,
    config_hash     text        NOT NULL,
    next_attempt_at timestamptz NULL,
    -- The partition key must be part of the primary key.
    CONSTRAINT attempts_pkey PRIMARY KEY (started_at, id),
    CONSTRAINT attempts_trigger_check CHECK (trigger IN ('auto', 'replay')),
    CONSTRAINT attempts_outcome_check CHECK (outcome IN (
        'success', 'retryable', 'permanent', 'render_error', 'infra_error')),
    CONSTRAINT attempts_number_check CHECK (number >= 1),
    CONSTRAINT attempts_duration_ms_check CHECK (duration_ms >= 0),
    CONSTRAINT attempts_status_code_check CHECK (status_code BETWEEN 100 AND 599)
) PARTITION BY RANGE (started_at);
COMMENT ON TABLE attempts IS 'Append-only attempt log, monthly range partitions on started_at';

CREATE TABLE attempts_default PARTITION OF attempts DEFAULT;

-- GET /v1/deliveries/{id}: attempts of one delivery in order.
CREATE INDEX attempts_delivery_id_number_idx ON attempts (delivery_id, number);

-- operator_actions: audit trail of replay / skip (§5.3). No ON DELETE CASCADE so
-- audit rows are never removed implicitly; retention must handle them (Phase 5).
CREATE TABLE operator_actions (
    id          uuid        PRIMARY KEY,
    delivery_id uuid        NOT NULL REFERENCES deliveries (id),
    action      text        NOT NULL,
    actor       text        NOT NULL,
    reason      text        NOT NULL,
    from_status text        NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT operator_actions_action_check CHECK (action IN ('replay', 'skip')),
    CONSTRAINT operator_actions_from_status_check CHECK (from_status IN (
        'PENDING', 'IN_FLIGHT', 'RETRY_SCHEDULED', 'SUCCEEDED', 'DEAD', 'SKIPPED'))
);
COMMENT ON TABLE operator_actions IS 'Audit of operator replay / skip actions per delivery';

-- Audit history of one delivery, in order.
CREATE INDEX operator_actions_delivery_id_created_at_idx
    ON operator_actions (delivery_id, created_at);

-- +goose Down

DROP TABLE IF EXISTS operator_actions;
DROP TABLE IF EXISTS attempts;
DROP TABLE IF EXISTS deliveries;
DROP TABLE IF EXISTS event_payloads;
DROP TABLE IF EXISTS events;
