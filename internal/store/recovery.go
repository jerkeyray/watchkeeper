package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jerkeyray/watchkeeper/internal/domain"
)

func (p *Postgres) BeginRetry(ctx context.Context, operationID string, expectedVersion int64, reason string) (domain.Operation, error) {
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return domain.Operation{}, unavailable("begin retry transaction", err)
	}
	defer tx.Rollback(ctx)
	operation, err := getOperationForUpdate(ctx, tx, operationID)
	if err != nil {
		return domain.Operation{}, err
	}
	if operation.State != domain.StateRetryable {
		return domain.Operation{}, domain.NewError(domain.ErrorConflict, "operation_state_conflict", "only a retryable operation can begin another attempt")
	}
	if operation.Version != expectedVersion {
		return domain.Operation{}, domain.NewError(domain.ErrorConflict, "operation_version_conflict", "operation version does not match expected_version")
	}
	newVersion := operation.Version + 1
	_, err = tx.Exec(ctx, `UPDATE operations SET state='prepared',attempt_count=attempt_count+1,version=$2,prepared_at=now(),resolved_at=NULL,next_reconcile_at=NULL,updated_at=now() WHERE id=$1`, operationID, newVersion)
	if err != nil {
		return domain.Operation{}, unavailable("begin retry", err)
	}
	_, err = tx.Exec(ctx, `INSERT INTO operation_events(operation_id,event_type,from_state,to_state,operation_version,source,actor,reason_code,details) VALUES($1,'retry_started','retryable','prepared',$2,'worker','workflow-worker',$3,'{}')`, operationID, newVersion, reason)
	if err != nil {
		return domain.Operation{}, unavailable("append retry event", err)
	}
	operation, err = getOperationTx(ctx, tx, operationID)
	if err != nil {
		return domain.Operation{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Operation{}, unavailable("commit retry", err)
	}
	return operation, nil
}

func (p *Postgres) RequestReconciliation(ctx context.Context, operationID string, expectedVersion int64, actor, reason string) (domain.Operation, error) {
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return domain.Operation{}, unavailable("begin reconciliation request", err)
	}
	defer tx.Rollback(ctx)
	operation, err := getOperationForUpdate(ctx, tx, operationID)
	if err != nil {
		return domain.Operation{}, err
	}
	if operation.State != domain.StatePrepared && operation.State != domain.StateUncertain {
		return domain.Operation{}, domain.NewError(domain.ErrorConflict, "operation_state_conflict", "only prepared or uncertain operations can be reconciled")
	}
	if operation.Version != expectedVersion {
		return domain.Operation{}, domain.NewError(domain.ErrorConflict, "operation_version_conflict", "operation version does not match expected_version")
	}
	newVersion := operation.Version + 1
	_, err = tx.Exec(ctx, `UPDATE operations SET version=$2,next_reconcile_at=now(),updated_at=now() WHERE id=$1`, operationID, newVersion)
	if err != nil {
		return domain.Operation{}, unavailable("queue reconciliation", err)
	}
	_, err = tx.Exec(ctx, `INSERT INTO operation_events(operation_id,event_type,from_state,to_state,operation_version,source,actor,reason_code,details) VALUES($1,'reconciliation_requested',$2,$2,$3,'manual',$4,$5,'{}')`, operationID, operation.State, newVersion, actor, reason)
	if err != nil {
		return domain.Operation{}, unavailable("append reconciliation request", err)
	}
	operation, err = getOperationTx(ctx, tx, operationID)
	if err != nil {
		return domain.Operation{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Operation{}, unavailable("commit reconciliation request", err)
	}
	return operation, nil
}

func randomClaimToken() (string, string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", err
	}
	plain := base64.RawURLEncoding.EncodeToString(raw)
	sum := sha256.Sum256([]byte(plain))
	return plain, hex.EncodeToString(sum[:]), nil
}
func claimTokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func (p *Postgres) ClaimRecovery(ctx context.Context, workerID string, limit int, leaseDuration, preparedGrace time.Duration) ([]domain.RecoveryClaim, error) {
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, unavailable("begin claim transaction", err)
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT id::text FROM operations WHERE ((state='prepared' AND (prepared_at <= now()-make_interval(secs=>$1) OR next_reconcile_at<=now())) OR (state='uncertain' AND next_reconcile_at<=now())) AND (next_reconcile_at IS NULL OR next_reconcile_at<=now()) AND (lease_expires_at IS NULL OR lease_expires_at<=now()) ORDER BY next_reconcile_at NULLS FIRST,prepared_at,id FOR UPDATE SKIP LOCKED LIMIT $2`, preparedGrace.Seconds(), limit)
	if err != nil {
		return nil, unavailable("select recovery work", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, unavailable("scan recovery ID", err)
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, unavailable("iterate recovery IDs", err)
	}
	claims := make([]domain.RecoveryClaim, 0, len(ids))
	for _, id := range ids {
		plain, hashed, err := randomClaimToken()
		if err != nil {
			return nil, err
		}
		attemptID, err := newID()
		if err != nil {
			return nil, err
		}
		var attemptNumber int
		if err := tx.QueryRow(ctx, `SELECT coalesce(max(attempt_number),0)+1 FROM reconciliation_attempts WHERE operation_id=$1`, id).Scan(&attemptNumber); err != nil {
			return nil, unavailable("allocate reconciliation attempt", err)
		}
		var expires time.Time
		var version int64
		if err := tx.QueryRow(ctx, `UPDATE operations SET lease_owner=$2,lease_token_hash=$3,lease_expires_at=now()+make_interval(secs=>$4),version=version+1,updated_at=now() WHERE id=$1 RETURNING lease_expires_at,version`, id, workerID, hashed, leaseDuration.Seconds()).Scan(&expires, &version); err != nil {
			return nil, unavailable("claim recovery operation", err)
		}
		_, err = tx.Exec(ctx, `INSERT INTO reconciliation_attempts(id,operation_id,attempt_number,claim_owner) VALUES($1,$2,$3,$4)`, attemptID, id, attemptNumber, workerID)
		if err != nil {
			return nil, unavailable("create reconciliation attempt", err)
		}
		_, err = tx.Exec(ctx, `INSERT INTO operation_events(operation_id,event_type,from_state,to_state,operation_version,source,actor,reason_code,details) SELECT id,'recovery_claimed',state,state,version,'coordinator',$2,'lease_acquired',jsonb_build_object('attempt_id',$3::text,'lease_expires_at',lease_expires_at) FROM operations WHERE id=$1`, id, workerID, attemptID)
		if err != nil {
			return nil, unavailable("append recovery claim", err)
		}
		operation, err := getOperationTx(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		claims = append(claims, domain.RecoveryClaim{Operation: operation, ClaimToken: plain, AttemptID: attemptID, LeaseExpiresAt: expires, Version: version})
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, unavailable("commit recovery claims", err)
	}
	return claims, nil
}

func (p *Postgres) RenewClaim(ctx context.Context, operationID, token string, duration time.Duration) (time.Time, error) {
	var expires time.Time
	err := p.pool.QueryRow(ctx, `UPDATE operations SET lease_expires_at=now()+make_interval(secs=>$3),updated_at=now() WHERE id=$1 AND lease_token_hash=$2 AND lease_expires_at>now() RETURNING lease_expires_at`, operationID, claimTokenHash(token), duration.Seconds()).Scan(&expires)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, domain.NewError(domain.ErrorConflict, "lease_lost", "recovery lease is missing or expired")
	}
	if err != nil {
		return time.Time{}, unavailable("renew recovery lease", err)
	}
	return expires, nil
}

func (p *Postgres) SubmitRecoveryResult(ctx context.Context, operationID string, result domain.RecoveryResult) (domain.Operation, error) {
	if err := validObservation(result.Observation); err != nil {
		return domain.Operation{}, validation("invalid_observation", err.Error())
	}
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return domain.Operation{}, unavailable("begin recovery result", err)
	}
	defer tx.Rollback(ctx)
	operation, err := getOperationForUpdate(ctx, tx, operationID)
	if err != nil {
		return domain.Operation{}, err
	}
	var tokenHash pgtype.Text
	var leaseExpires pgtype.Timestamptz
	var leaseValid bool
	if err := tx.QueryRow(ctx, `SELECT lease_token_hash,lease_expires_at,coalesce(lease_expires_at>now(),false) FROM operations WHERE id=$1`, operationID).Scan(&tokenHash, &leaseExpires, &leaseValid); err != nil {
		return domain.Operation{}, unavailable("read recovery lease", err)
	}
	if !tokenHash.Valid || tokenHash.String != claimTokenHash(result.ClaimToken) || !leaseExpires.Valid || !leaseValid {
		return domain.Operation{}, domain.NewError(domain.ErrorConflict, "lease_lost", "recovery lease is missing or expired")
	}
	if operation.Version != result.ExpectedVersion {
		return domain.Operation{}, domain.NewError(domain.ErrorConflict, "operation_version_conflict", "operation version does not match expected_version")
	}
	var attemptNumber int
	var finished pgtype.Timestamptz
	if err := tx.QueryRow(ctx, `SELECT attempt_number,finished_at FROM reconciliation_attempts WHERE id=$1 AND operation_id=$2 FOR UPDATE`, result.AttemptID, operationID).Scan(&attemptNumber, &finished); errors.Is(err, pgx.ErrNoRows) {
		return domain.Operation{}, domain.NewError(domain.ErrorConflict, "reconciliation_attempt_conflict", "attempt does not belong to this operation")
	} else if err != nil {
		return domain.Operation{}, unavailable("read reconciliation attempt", err)
	}
	if finished.Valid {
		return domain.Operation{}, domain.NewError(domain.ErrorConflict, "reconciliation_attempt_conflict", "attempt is already finished")
	}
	evidence := result.Observation.Evidence
	if len(evidence) == 0 {
		evidence = json.RawMessage(`{}`)
	}
	observationID, err := newID()
	if err != nil {
		return domain.Operation{}, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO observations(id,reconciliation_attempt_id,mechanism,outcome,authoritative,external_reference,evidence) VALUES($1,$2,$3,$4,$5,$6,$7)`, observationID, result.AttemptID, result.Observation.Mechanism, result.Observation.Outcome, result.Observation.Authoritative, result.Observation.ExternalReference, evidence)
	if err != nil {
		return domain.Operation{}, unavailable("store recovery observation", err)
	}
	toState := operation.State
	reason := "verification_transient"
	decision := ""
	var delaySeconds float64
	resolved := false
	switch {
	case result.Observation.Authoritative && result.Observation.Outcome == domain.OutcomeCompleted:
		toState = domain.StateReconciled
		reason = "effect_completed"
		decision = "reconcile"
		resolved = true
	case result.Observation.Authoritative && result.Observation.Outcome == domain.OutcomeAbsent:
		toState = domain.StateRetryable
		reason = "effect_absent"
		decision = "retry"
	case result.Observation.Outcome == domain.OutcomeTransient && attemptNumber < 5:
		delay := 250 * time.Millisecond * time.Duration(1<<(attemptNumber-1))
		if delay > 10*time.Second {
			delay = 10 * time.Second
		}
		delaySeconds = delay.Seconds()
	case result.Observation.Outcome == domain.OutcomeContradictory:
		toState = domain.StateUncertain
		reason = "evidence_contradictory"
		decision = "mark_uncertain"
		resolved = true
	default:
		toState = domain.StateUncertain
		reason = "evidence_insufficient"
		decision = "mark_uncertain"
		resolved = true
	}
	newVersion := operation.Version + 1
	_, err = tx.Exec(ctx, `UPDATE operations SET state=$2,version=$3,resolved_at=CASE WHEN $4 THEN now() ELSE NULL END,next_reconcile_at=CASE WHEN $5::float8>0 THEN now()+make_interval(secs=>$5) ELSE NULL END,lease_owner=NULL,lease_token_hash=NULL,lease_expires_at=NULL,updated_at=now() WHERE id=$1`, operationID, toState, newVersion, resolved, delaySeconds)
	if err != nil {
		return domain.Operation{}, unavailable("apply recovery result", err)
	}
	_, err = tx.Exec(ctx, `UPDATE reconciliation_attempts SET finished_at=now(),result_class=$2 WHERE id=$1`, result.AttemptID, result.Observation.Outcome)
	if err != nil {
		return domain.Operation{}, unavailable("finish reconciliation attempt", err)
	}
	if decision != "" {
		decisionID, err := newID()
		if err != nil {
			return domain.Operation{}, err
		}
		refs, _ := json.Marshal([]string{observationID})
		_, err = tx.Exec(ctx, `INSERT INTO recovery_decisions(id,operation_id,reconciliation_attempt_id,decision,source,actor,reason_code,evidence_references,from_state,to_state,operation_version) VALUES($1,$2,$3,$4,'coordinator','recovery-coordinator',$5,$6,$7,$8,$9)`, decisionID, operationID, result.AttemptID, decision, reason, refs, operation.State, toState, newVersion)
		if err != nil {
			return domain.Operation{}, unavailable("store recovery decision", err)
		}
	}
	_, err = tx.Exec(ctx, `INSERT INTO operation_events(operation_id,event_type,from_state,to_state,operation_version,source,actor,reason_code,details) VALUES($1,'recovery_result',$2,$3,$4,'coordinator','recovery-coordinator',$5,jsonb_build_object('attempt_id',$6::text,'observation_id',$7::text))`, operationID, operation.State, toState, newVersion, reason, result.AttemptID, observationID)
	if err != nil {
		return domain.Operation{}, unavailable("append recovery result", err)
	}
	operation, err = getOperationTx(ctx, tx, operationID)
	if err != nil {
		return domain.Operation{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Operation{}, unavailable("commit recovery result", err)
	}
	return operation, nil
}

func (p *Postgres) ManualResolve(ctx context.Context, operationID string, resolution domain.ManualResolution) (domain.Operation, error) {
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return domain.Operation{}, unavailable("begin manual resolution", err)
	}
	defer tx.Rollback(ctx)
	operation, err := getOperationForUpdate(ctx, tx, operationID)
	if err != nil {
		return domain.Operation{}, err
	}
	if operation.State != domain.StateUncertain {
		return domain.Operation{}, domain.NewError(domain.ErrorConflict, "operation_state_conflict", "only uncertain operations can be resolved manually")
	}
	if operation.Version != resolution.ExpectedVersion {
		return domain.Operation{}, domain.NewError(domain.ErrorConflict, "operation_version_conflict", "operation version does not match expected_version")
	}
	toState := operation.State
	decision := "compensate_recorded"
	switch resolution.Outcome {
	case "completed":
		toState = domain.StateReconciled
		decision = "reconcile"
	case "absent":
		toState = domain.StateRetryable
		decision = "retry"
	case "compensation_recorded":
	default:
		return domain.Operation{}, validation("invalid_manual_outcome", "manual outcome must be completed, absent, or compensation_recorded")
	}
	newVersion := operation.Version + 1
	_, err = tx.Exec(ctx, `UPDATE operations SET state=$2,version=$3,resolved_at=CASE WHEN $4 THEN now() ELSE NULL END,next_reconcile_at=NULL,updated_at=now() WHERE id=$1`, operationID, toState, newVersion, toState == domain.StateReconciled)
	if err != nil {
		return domain.Operation{}, unavailable("apply manual resolution", err)
	}
	decisionID, err := newID()
	if err != nil {
		return domain.Operation{}, err
	}
	refs, _ := json.Marshal([]string{resolution.EvidenceReference})
	_, err = tx.Exec(ctx, `INSERT INTO recovery_decisions(id,operation_id,decision,source,actor,reason_code,evidence_references,from_state,to_state,operation_version) VALUES($1,$2,$3,'manual',$4,$5,$6,$7,$8,$9)`, decisionID, operationID, decision, resolution.Actor, resolution.Reason, refs, operation.State, toState, newVersion)
	if err != nil {
		return domain.Operation{}, unavailable("store manual decision", err)
	}
	_, err = tx.Exec(ctx, `INSERT INTO operation_events(operation_id,event_type,from_state,to_state,operation_version,source,actor,reason_code,details) VALUES($1,'manual_resolution',$2,$3,$4,'manual',$5,$6,jsonb_build_object('evidence_reference',$7::text))`, operationID, operation.State, toState, newVersion, resolution.Actor, resolution.Reason, resolution.EvidenceReference)
	if err != nil {
		return domain.Operation{}, unavailable("append manual resolution", err)
	}
	operation, err = getOperationTx(ctx, tx, operationID)
	if err != nil {
		return domain.Operation{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Operation{}, unavailable("commit manual resolution", err)
	}
	return operation, nil
}

func validObservation(observation domain.Observation) error {
	switch observation.Mechanism {
	case domain.MechanismReceipt, domain.MechanismStatus, domain.MechanismIndirect, domain.MechanismIdempotentRepeat, domain.MechanismNone:
	default:
		return fmt.Errorf("invalid verification mechanism")
	}
	switch observation.Outcome {
	case domain.OutcomeCompleted, domain.OutcomeAbsent, domain.OutcomeUnknown, domain.OutcomeTransient, domain.OutcomeContradictory:
		return nil
	default:
		return fmt.Errorf("invalid observation outcome")
	}
}
