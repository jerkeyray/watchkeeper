-- +goose Up
ALTER TABLE experiment_runs DROP CONSTRAINT experiment_runs_matrix_cell_id_repetition_seed_key;
CREATE INDEX experiment_runs_cell_idx ON experiment_runs(matrix_cell_id,repetition,seed);
-- +goose Down
-- This rollback fails transactionally if retained replacement executions share cells.
-- Never delete research history to force the rollback.
ALTER TABLE experiment_runs ADD CONSTRAINT experiment_runs_matrix_cell_id_repetition_seed_key UNIQUE(matrix_cell_id,repetition,seed);
DROP INDEX experiment_runs_cell_idx;
