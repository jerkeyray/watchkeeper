-- +goose Up
ALTER TABLE operations ADD COLUMN dispatch_authorized boolean NOT NULL DEFAULT true;
-- Existing operations may already have sent an external request.
ALTER TABLE operations ALTER COLUMN dispatch_authorized SET DEFAULT false;

-- +goose Down
ALTER TABLE operations DROP COLUMN dispatch_authorized;
