-- +goose Up
CREATE TABLE email_effects (
    id uuid PRIMARY KEY,
    operation_id uuid NOT NULL,
    experiment_run_id uuid,
    recipient text NOT NULL,
    template text NOT NULL,
    logical_message_key text NOT NULL,
    committed_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX email_effects_operation_idx ON email_effects (operation_id, committed_at);

-- +goose Down
DROP TABLE email_effects;
