-- +goose Up
CREATE TABLE experiment_runs (
    id uuid PRIMARY KEY,
    matrix_cell_id text NOT NULL,
    strategy text NOT NULL CHECK (strategy IN ('blind_retry','idempotency_key_retry','checkpoint_recovery','watchkeeper')),
    workload text NOT NULL,
    capability_profile text NOT NULL,
    failure_scenario text NOT NULL,
    repetition integer NOT NULL CHECK (repetition > 0),
    seed bigint NOT NULL,
    config_hash char(64) NOT NULL,
    source_revision text,
    image_digests jsonb NOT NULL DEFAULT '{}',
    schema_version text NOT NULL,
    status text NOT NULL CHECK (status IN ('planned','running','completed','invalid','failed')),
    started_at timestamptz,
    finished_at timestamptz,
    outcome jsonb NOT NULL DEFAULT '{}',
    UNIQUE (matrix_cell_id, repetition, seed)
);

CREATE TABLE workflows (
    id uuid PRIMARY KEY,
    external_workflow_id text NOT NULL UNIQUE,
    strategy text NOT NULL CHECK (strategy IN ('blind_retry','idempotency_key_retry','checkpoint_recovery','watchkeeper')),
    status text NOT NULL DEFAULT 'running' CHECK (status IN ('running','completed','failed','stopped')),
    experiment_run_id uuid REFERENCES experiment_runs(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE operations (
    id uuid PRIMARY KEY,
    workflow_id uuid NOT NULL REFERENCES workflows(id),
    logical_key text NOT NULL,
    target_service text NOT NULL,
    action text NOT NULL,
    request_fingerprint char(64) NOT NULL,
    expected_effect jsonb NOT NULL CHECK (jsonb_typeof(expected_effect) = 'object'),
    capability_profile text NOT NULL,
    state text NOT NULL CHECK (state IN ('prepared','confirmed','reconciled','retryable','uncertain')),
    attempt_count integer NOT NULL DEFAULT 1 CHECK (attempt_count > 0),
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    prepared_at timestamptz NOT NULL DEFAULT now(),
    confirmed_at timestamptz,
    resolved_at timestamptz,
    next_reconcile_at timestamptz,
    lease_owner text,
    lease_token_hash char(64),
    lease_expires_at timestamptz,
    trace_context jsonb NOT NULL DEFAULT '{}',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (workflow_id, logical_key),
    CHECK ((lease_owner IS NULL AND lease_token_hash IS NULL AND lease_expires_at IS NULL) OR
           (lease_owner IS NOT NULL AND lease_token_hash IS NOT NULL AND lease_expires_at IS NOT NULL)),
    CHECK (state <> 'confirmed' OR (confirmed_at IS NOT NULL AND resolved_at IS NOT NULL)),
    CHECK (state <> 'reconciled' OR resolved_at IS NOT NULL)
);
CREATE INDEX operations_recovery_idx ON operations (state, next_reconcile_at, prepared_at);
CREATE INDEX operations_lease_idx ON operations (lease_expires_at);
CREATE INDEX operations_service_state_idx ON operations (target_service, state);
CREATE INDEX operations_created_idx ON operations (created_at, id);

CREATE TABLE receipts (
    id uuid PRIMARY KEY,
    operation_id uuid NOT NULL UNIQUE REFERENCES operations(id),
    service_receipt_id text NOT NULL,
    receipt_fingerprint char(64) NOT NULL,
    payload jsonb NOT NULL,
    received_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE reconciliation_attempts (
    id uuid PRIMARY KEY,
    operation_id uuid NOT NULL REFERENCES operations(id),
    attempt_number integer NOT NULL CHECK (attempt_number > 0),
    claim_owner text NOT NULL,
    started_at timestamptz NOT NULL DEFAULT now(),
    finished_at timestamptz,
    result_class text CHECK (result_class IS NULL OR result_class IN ('completed','absent','unknown','transient_error','contradictory')),
    error_code text,
    error_detail jsonb NOT NULL DEFAULT '{}',
    UNIQUE (operation_id, attempt_number)
);

CREATE TABLE observations (
    id uuid PRIMARY KEY,
    reconciliation_attempt_id uuid NOT NULL REFERENCES reconciliation_attempts(id),
    mechanism text NOT NULL CHECK (mechanism IN ('receipt_lookup','status_lookup','indirect_lookup','idempotent_repeat','none')),
    outcome text NOT NULL CHECK (outcome IN ('completed','absent','unknown','transient_error','contradictory')),
    authoritative boolean NOT NULL,
    external_reference text,
    evidence jsonb NOT NULL DEFAULT '{}',
    observed_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE operation_events (
    id bigserial PRIMARY KEY,
    operation_id uuid NOT NULL REFERENCES operations(id),
    event_type text NOT NULL,
    from_state text,
    to_state text,
    operation_version bigint NOT NULL,
    source text NOT NULL CHECK (source IN ('api','coordinator','worker','manual')),
    actor text NOT NULL,
    reason_code text NOT NULL,
    details jsonb NOT NULL DEFAULT '{}',
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX operation_events_operation_cursor_idx ON operation_events (operation_id, id);

CREATE TABLE recovery_decisions (
    id uuid PRIMARY KEY,
    operation_id uuid NOT NULL REFERENCES operations(id),
    reconciliation_attempt_id uuid REFERENCES reconciliation_attempts(id),
    decision text NOT NULL CHECK (decision IN ('reconcile','retry','mark_uncertain','compensate_recorded')),
    source text NOT NULL CHECK (source IN ('coordinator','manual')),
    actor text NOT NULL,
    reason_code text NOT NULL,
    evidence_references jsonb NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(evidence_references) = 'array'),
    from_state text NOT NULL,
    to_state text NOT NULL,
    operation_version bigint NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (operation_id, operation_version)
);

CREATE TABLE injected_faults (
    id uuid PRIMARY KEY,
    experiment_run_id uuid NOT NULL REFERENCES experiment_runs(id),
    fault_type text NOT NULL,
    barrier_name text NOT NULL,
    target text NOT NULL,
    scheduled_at timestamptz NOT NULL,
    triggered_at timestamptz,
    released_at timestamptz,
    parameters jsonb NOT NULL DEFAULT '{}',
    result jsonb NOT NULL DEFAULT '{}'
);

-- +goose Down
DROP TABLE injected_faults;
DROP TABLE recovery_decisions;
DROP TABLE operation_events;
DROP TABLE observations;
DROP TABLE reconciliation_attempts;
DROP TABLE receipts;
DROP TABLE operations;
DROP TABLE workflows;
DROP TABLE experiment_runs;
