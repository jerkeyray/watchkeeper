package benchmark

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jerkeyray/watchkeeper/internal/domain"
	"github.com/jerkeyray/watchkeeper/internal/store"
	"os"
	"path/filepath"
	"time"
)

func (r *runner) readTruth(ctx context.Context, t *trial, deadline bool) error {
	t.mu.Lock()
	id := t.result.ExecutionID
	t.mu.Unlock()
	rows, err := r.sim.Query(ctx, `SELECT template,count(*) FROM email_effects WHERE experiment_run_id=$1 GROUP BY template`, id)
	if err != nil {
		return err
	}
	defer rows.Close()
	counts := map[string]int{}
	for rows.Next() {
		var key string
		var count int
		if err = rows.Scan(&key, &count); err != nil {
			return err
		}
		counts[key] = count
	}
	if err = rows.Err(); err != nil {
		return err
	}
	var ledger json.RawMessage
	if err = r.sim.QueryRow(ctx, `SELECT coalesce(jsonb_agg(to_jsonb(e) ORDER BY committed_at,id),'[]') FROM email_effects e WHERE experiment_run_id=$1`, id).Scan(&ledger); err != nil {
		return err
	}
	name := "final-ledger.json"
	if deadline {
		name = "deadline-ledger.json"
	}
	if err = writeJSONFile(filepath.Join(r.output, "runs", id, name), ledger); err != nil {
		return err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for i := range t.result.Steps {
		if deadline {
			t.result.Steps[i].DeadlineEffects = counts[t.result.Steps[i].Key]
		} else {
			t.result.Steps[i].Effects = counts[t.result.Steps[i].Key]
		}
	}
	return nil
}
func (r *runner) storage(ctx context.Context) (int64, error) { return r.storagePools(ctx, 0) }
func (r *runner) storagePools(ctx context.Context, total int64) (int64, error) {
	for _, p := range []*pgxpool.Pool{r.wk, r.sim, r.baseline} {
		var n int64
		err := p.QueryRow(ctx, `SELECT coalesce(sum(pg_total_relation_size(relid)),0)::bigint FROM pg_catalog.pg_statio_user_tables WHERE relname NOT IN ('experiment_runs','injected_faults','goose_db_version')`).Scan(&n)
		if err != nil {
			return 0, err
		}
		total += n
	}
	return total, nil
}
func (r *runner) watchkeeperWrites(ctx context.Context, id string) (int64, error) {
	// Counts committed row inserts/updates of the defined API transactions, including
	// idempotent prepare's workflow INSERT ... ON CONFLICT (no write on conflict).
	var n int64
	err := r.wk.QueryRow(ctx, `SELECT coalesce(sum(CASE e.event_type WHEN 'intent_prepared' THEN 2 WHEN 'dispatch_authorized' THEN 2 WHEN 'receipt_confirmed' THEN 3 WHEN 'retry_started' THEN 2 WHEN 'recovery_claimed' THEN 3 WHEN 'recovery_result' THEN CASE WHEN e.reason_code='verification_transient' THEN 4 ELSE 5 END WHEN 'reconciliation_requested' THEN 2 WHEN 'manual_resolution' THEN 3 ELSE 1 END),0)::bigint+(SELECT count(*) FROM workflows WHERE experiment_run_id=$1) FROM operation_events e JOIN operations o ON o.id=e.operation_id JOIN workflows w ON w.id=o.workflow_id WHERE w.experiment_run_id=$1`, id).Scan(&n)
	return n, err
}
func (r *runner) saveAudit(ctx context.Context, p Result, dir string) error {
	queries := []struct {
		name, sql string
		sim       bool
	}{
		{"ledger.json", `SELECT coalesce(jsonb_agg(to_jsonb(e) ORDER BY committed_at,id),'[]') FROM email_effects e WHERE experiment_run_id=$1`, true},
		{"dispatches.json", `SELECT coalesce(jsonb_agg(to_jsonb(e) ORDER BY id),'[]') FROM email_dispatches e WHERE experiment_run_id=$1`, true},
		{"operations.json", `SELECT coalesce(jsonb_agg(to_jsonb(o) - 'lease_token_hash' ORDER BY logical_key),'[]') FROM operations o JOIN workflows w ON w.id=o.workflow_id WHERE w.experiment_run_id=$1`, false},
		{"audit.json", `SELECT coalesce(jsonb_agg(to_jsonb(e) ORDER BY e.id),'[]') FROM operation_events e JOIN operations o ON o.id=e.operation_id JOIN workflows w ON w.id=o.workflow_id WHERE w.experiment_run_id=$1`, false},
		{"attempts.json", `SELECT coalesce(jsonb_agg(to_jsonb(a)),'[]') FROM reconciliation_attempts a JOIN operations o ON o.id=a.operation_id JOIN workflows w ON w.id=o.workflow_id WHERE w.experiment_run_id=$1`, false},
		{"observations.json", `SELECT coalesce(jsonb_agg(to_jsonb(b)),'[]') FROM observations b JOIN reconciliation_attempts a ON a.id=b.reconciliation_attempt_id JOIN operations o ON o.id=a.operation_id JOIN workflows w ON w.id=o.workflow_id WHERE w.experiment_run_id=$1`, false},
		{"decisions.json", `SELECT coalesce(jsonb_agg(to_jsonb(d)),'[]') FROM recovery_decisions d JOIN operations o ON o.id=d.operation_id JOIN workflows w ON w.id=o.workflow_id WHERE w.experiment_run_id=$1`, false},
	}
	for _, q := range queries {
		pool := r.wk
		if q.sim {
			pool = r.sim
		}
		var raw json.RawMessage
		if err := pool.QueryRow(ctx, q.sql, p.ExecutionID).Scan(&raw); err != nil {
			return err
		}
		if err := writeJSONFile(filepath.Join(dir, q.name), raw); err != nil {
			return err
		}
	}
	if p.Scenario != "none" {
		raw, err := os.ReadFile(filepath.Join(dir, "events.json"))
		if err != nil {
			return err
		}
		var events []Event
		if err = json.Unmarshal(raw, &events); err != nil {
			return err
		}
		var triggered, released *time.Time
		for _, e := range events {
			at := e.At
			if e.Barrier == p.Scenario && e.Phase == "triggered" {
				triggered = &at
			}
			if e.Barrier == p.Scenario && e.Phase == "released" {
				released = &at
			}
		}
		_, err = r.wk.Exec(ctx, `UPDATE injected_faults SET triggered_at=$2,released_at=$3,result=$4 WHERE experiment_run_id=$1`, p.ExecutionID, triggered, released, json.RawMessage(raw))
		if err != nil {
			return fmt.Errorf("save fault: %w", err)
		}
	}
	return nil
}

type scopedStore struct {
	*store.Postgres
	workflowID string
}

func (s scopedStore) ClaimRecovery(ctx context.Context, worker string, limit int, lease, grace time.Duration) ([]domain.RecoveryClaim, error) {
	return s.ClaimRecoveryForWorkflow(ctx, worker, limit, lease, grace, s.workflowID)
}
