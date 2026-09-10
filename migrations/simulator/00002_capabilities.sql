-- +goose Up
CREATE TABLE email_keys (
 key text PRIMARY KEY,
 operation_id uuid NOT NULL,
 fingerprint text NOT NULL,
 receipt_id uuid NOT NULL REFERENCES email_effects(id)
);
CREATE TABLE email_dispatches (
 id bigserial PRIMARY KEY,
 operation_id uuid NOT NULL,
 experiment_run_id uuid,
 logical_message_key text NOT NULL,
 idempotency_key text,
 created_at timestamptz NOT NULL DEFAULT now()
);
-- +goose Down
DROP TABLE email_dispatches;
DROP TABLE email_keys;
