package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jerkeyray/watchkeeper/internal/domain"
)

type Postgres struct{ pool *pgxpool.Pool }

func NewPostgres(pool *pgxpool.Pool) *Postgres { return &Postgres{pool: pool} }

func (p *Postgres) Ping(ctx context.Context) error { return p.pool.Ping(ctx) }

func (p *Postgres) SchemaReady(ctx context.Context) error {
	const expectedSchemaVersion int64 = 1
	var operations, events, migrations string
	if err := p.pool.QueryRow(ctx, `SELECT coalesce(to_regclass('public.operations')::text,''), coalesce(to_regclass('public.operation_events')::text,''), coalesce(to_regclass('public.goose_db_version')::text,'')`).Scan(&operations, &events, &migrations); err != nil {
		return err
	}
	if operations == "" || events == "" || migrations == "" {
		return errors.New("required schema is not migrated")
	}
	var version int64
	if err := p.pool.QueryRow(ctx, `SELECT version_id FROM goose_db_version WHERE is_applied ORDER BY id DESC LIMIT 1`).Scan(&version); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if version != expectedSchemaVersion {
		return fmt.Errorf("schema version %d is incompatible with expected version %d", version, expectedSchemaVersion)
	}
	return nil
}

func newID() (string, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return "", fmt.Errorf("generate UUIDv7: %w", err)
	}
	return id.String(), nil
}

func (p *Postgres) Prepare(ctx context.Context, req domain.PrepareRequest) (domain.Operation, bool, error) {
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return domain.Operation{}, false, unavailable("begin prepare transaction", err)
	}
	defer tx.Rollback(ctx)

	workflowID, err := p.ensureWorkflow(ctx, tx, req)
	if err != nil {
		return domain.Operation{}, false, err
	}

	operationID, err := newID()
	if err != nil {
		return domain.Operation{}, false, err
	}
	trace := req.TraceContext
	if len(trace) == 0 {
		trace = json.RawMessage(`{}`)
	}
	var insertedID string
	err = tx.QueryRow(ctx, `
		INSERT INTO operations
		(id, workflow_id, logical_key, target_service, action, request_fingerprint, expected_effect, capability_profile, state, trace_context)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,'prepared',$9)
		ON CONFLICT (workflow_id, logical_key) DO NOTHING
		RETURNING id::text`, operationID, workflowID, req.LogicalKey, req.TargetService, req.Action,
		req.RequestFingerprint, req.ExpectedEffect, req.CapabilityProfile, trace).Scan(&insertedID)
	created := true
	if errors.Is(err, pgx.ErrNoRows) {
		created = false
		var existingFingerprint string
		if err := tx.QueryRow(ctx, `SELECT id::text, request_fingerprint FROM operations WHERE workflow_id=$1 AND logical_key=$2`, workflowID, req.LogicalKey).Scan(&insertedID, &existingFingerprint); err != nil {
			return domain.Operation{}, false, unavailable("read existing operation", err)
		}
		if existingFingerprint != req.RequestFingerprint {
			conflict := domain.NewError(domain.ErrorConflict, "operation_fingerprint_conflict", "logical operation already exists with a different fingerprint")
			conflict.Details = map[string]any{"operation_id": insertedID}
			return domain.Operation{}, false, conflict
		}
	} else if err != nil {
		return domain.Operation{}, false, unavailable("insert operation", err)
	}

	if created {
		_, err = tx.Exec(ctx, `INSERT INTO operation_events
			(operation_id,event_type,to_state,operation_version,source,actor,reason_code,details)
			VALUES ($1,'intent_prepared','prepared',1,'api','watchkeeper-api','intent_persisted','{}')`, insertedID)
		if err != nil {
			return domain.Operation{}, false, unavailable("append intent event", err)
		}
	}
	operation, err := getOperationTx(ctx, tx, insertedID)
	if err != nil {
		return domain.Operation{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Operation{}, false, unavailable("commit prepare", err)
	}
	return operation, created, nil
}

func (p *Postgres) ensureWorkflow(ctx context.Context, tx pgx.Tx, req domain.PrepareRequest) (string, error) {
	workflowID, err := newID()
	if err != nil {
		return "", err
	}
	var experiment any
	if req.ExperimentRunID != nil {
		experiment = *req.ExperimentRunID
	}
	_, err = tx.Exec(ctx, `INSERT INTO workflows (id,external_workflow_id,strategy,experiment_run_id)
		VALUES ($1,$2,$3,$4) ON CONFLICT (external_workflow_id) DO NOTHING`, workflowID, req.WorkflowID, req.Strategy, experiment)
	if err != nil {
		return "", unavailable("insert workflow", err)
	}
	var existingStrategy domain.Strategy
	var existingExperiment pgtype.Text
	if err := tx.QueryRow(ctx, `SELECT id::text,strategy,experiment_run_id::text FROM workflows WHERE external_workflow_id=$1`, req.WorkflowID).Scan(&workflowID, &existingStrategy, &existingExperiment); err != nil {
		return "", unavailable("read workflow", err)
	}
	requestedExperiment := ""
	if req.ExperimentRunID != nil {
		requestedExperiment = *req.ExperimentRunID
	}
	if existingStrategy != req.Strategy || existingExperiment.String != requestedExperiment {
		return "", domain.NewError(domain.ErrorConflict, "workflow_metadata_conflict", "workflow already exists with different strategy or experiment run")
	}
	return workflowID, nil
}

func (p *Postgres) Confirm(ctx context.Context, operationID string, req domain.ConfirmRequest) (domain.Operation, error) {
	fingerprint, err := domain.ReceiptFingerprint(req.Receipt)
	if err != nil {
		return domain.Operation{}, validation("invalid_receipt", err.Error())
	}
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return domain.Operation{}, unavailable("begin confirmation transaction", err)
	}
	defer tx.Rollback(ctx)

	operation, err := getOperationForUpdate(ctx, tx, operationID)
	if err != nil {
		return domain.Operation{}, err
	}
	var existingFingerprint, existingServiceReceiptID pgtype.Text
	err = tx.QueryRow(ctx, `SELECT receipt_fingerprint,service_receipt_id FROM receipts WHERE operation_id=$1`, operationID).Scan(&existingFingerprint, &existingServiceReceiptID)
	if err == nil {
		if existingFingerprint.String != fingerprint || existingServiceReceiptID.String != req.ServiceReceiptID {
			return domain.Operation{}, domain.NewError(domain.ErrorConflict, "receipt_conflict", "operation already has a different receipt")
		}
		operation, err = getOperationTx(ctx, tx, operationID)
		if err == nil {
			operation.Receipt, err = getReceiptTx(ctx, tx, operationID)
		}
		if err != nil {
			return domain.Operation{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return domain.Operation{}, unavailable("commit idempotent confirmation", err)
		}
		return operation, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return domain.Operation{}, unavailable("read receipt", err)
	}
	if operation.State != domain.StatePrepared {
		return domain.Operation{}, domain.NewError(domain.ErrorConflict, "operation_state_conflict", "only a prepared operation can be confirmed")
	}
	if operation.Version != req.ExpectedVersion {
		return domain.Operation{}, domain.NewError(domain.ErrorConflict, "operation_version_conflict", "operation version does not match expected_version")
	}
	receiptID, err := newID()
	if err != nil {
		return domain.Operation{}, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO receipts (id,operation_id,service_receipt_id,receipt_fingerprint,payload,received_at)
		VALUES ($1,$2,$3,$4,$5,$6)`, receiptID, operationID, req.ServiceReceiptID, fingerprint, req.Receipt, req.ReceivedAt)
	if err != nil {
		return domain.Operation{}, unavailable("insert receipt", err)
	}
	var newVersion int64
	_, err = tx.Exec(ctx, `UPDATE operations SET state='confirmed',version=version+1,confirmed_at=now(),resolved_at=now(),
		lease_owner=NULL,lease_token_hash=NULL,lease_expires_at=NULL,updated_at=now() WHERE id=$1`, operationID)
	if err != nil {
		return domain.Operation{}, unavailable("confirm operation", err)
	}
	if err := tx.QueryRow(ctx, `SELECT version FROM operations WHERE id=$1`, operationID).Scan(&newVersion); err != nil {
		return domain.Operation{}, unavailable("read confirmed version", err)
	}
	_, err = tx.Exec(ctx, `INSERT INTO operation_events
		(operation_id,event_type,from_state,to_state,operation_version,source,actor,reason_code,details)
		VALUES ($1,'receipt_confirmed','prepared','confirmed',$2,'api','watchkeeper-api','receipt_persisted',jsonb_build_object('receipt_id',$3::text))`, operationID, newVersion, receiptID)
	if err != nil {
		return domain.Operation{}, unavailable("append confirmation event", err)
	}
	operation, err = getOperationTx(ctx, tx, operationID)
	if err == nil {
		operation.Receipt, err = getReceiptTx(ctx, tx, operationID)
	}
	if err != nil {
		return domain.Operation{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Operation{}, unavailable("commit confirmation", err)
	}
	return operation, nil
}

func (p *Postgres) GetOperation(ctx context.Context, operationID string) (domain.Operation, error) {
	operation, err := getOperationTx(ctx, p.pool, operationID)
	if err != nil {
		return domain.Operation{}, err
	}
	receipt, err := getReceiptTx(ctx, p.pool, operationID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return domain.Operation{}, err
	}
	operation.Receipt = receipt
	return operation, nil
}

func (p *Postgres) ListOperations(ctx context.Context, filter domain.OperationFilter) (domain.OperationPage, error) {
	cursor, err := decodeOperationCursor(filter.Cursor)
	if err != nil {
		return domain.OperationPage{}, validation("invalid_cursor", err.Error())
	}
	rows, err := p.pool.Query(ctx, `SELECT `+operationColumns+` FROM operations o JOIN workflows w ON w.id=o.workflow_id
		WHERE ($1='' OR w.external_workflow_id=$1) AND ($2='' OR o.state=$2) AND ($3='' OR o.target_service=$3)
		AND ($4::timestamptz IS NULL OR o.created_at>$4) AND (o.created_at,o.id)>($5,$6::uuid)
		ORDER BY o.created_at,o.id LIMIT $7`, filter.WorkflowID, string(filter.State), filter.TargetService, filter.CreatedAfter, cursor.CreatedAt, cursor.ID, filter.Limit+1)
	if err != nil {
		return domain.OperationPage{}, unavailable("list operations", err)
	}
	defer rows.Close()
	page := domain.OperationPage{Items: []domain.Operation{}}
	for rows.Next() {
		op, err := scanOperation(rows)
		if err != nil {
			return domain.OperationPage{}, unavailable("scan operation", err)
		}
		page.Items = append(page.Items, op)
	}
	if err := rows.Err(); err != nil {
		return domain.OperationPage{}, unavailable("iterate operations", err)
	}
	if len(page.Items) > filter.Limit {
		last := page.Items[filter.Limit-1]
		page.NextCursor = encodeCursor(operationCursor{CreatedAt: last.CreatedAt, ID: last.ID})
		page.Items = page.Items[:filter.Limit]
	}
	return page, nil
}

func (p *Postgres) ListEvents(ctx context.Context, operationID string, limit int, cursorValue string) (domain.EventPage, error) {
	if _, err := p.GetOperation(ctx, operationID); err != nil {
		return domain.EventPage{}, err
	}
	cursor, err := decodeEventCursor(cursorValue)
	if err != nil {
		return domain.EventPage{}, validation("invalid_cursor", err.Error())
	}
	rows, err := p.pool.Query(ctx, `SELECT id,operation_id::text,event_type,from_state,to_state,operation_version,source,actor,reason_code,details,created_at
		FROM operation_events WHERE operation_id=$1 AND id>$2 ORDER BY id LIMIT $3`, operationID, cursor, limit+1)
	if err != nil {
		return domain.EventPage{}, unavailable("list events", err)
	}
	defer rows.Close()
	page := domain.EventPage{Items: []domain.Event{}}
	for rows.Next() {
		var event domain.Event
		var from, to pgtype.Text
		if err := rows.Scan(&event.ID, &event.OperationID, &event.EventType, &from, &to, &event.OperationVersion, &event.Source, &event.Actor, &event.ReasonCode, &event.Details, &event.CreatedAt); err != nil {
			return domain.EventPage{}, unavailable("scan event", err)
		}
		if from.Valid {
			state := domain.OperationState(from.String)
			event.FromState = &state
		}
		if to.Valid {
			state := domain.OperationState(to.String)
			event.ToState = &state
		}
		page.Items = append(page.Items, event)
	}
	if len(page.Items) > limit {
		page.NextCursor = encodeEventCursor(page.Items[limit-1].ID)
		page.Items = page.Items[:limit]
	}
	return page, rows.Err()
}

const operationColumns = `o.id::text,w.external_workflow_id,o.logical_key,o.target_service,o.action,o.request_fingerprint,
	o.expected_effect,o.capability_profile,o.state,o.attempt_count,o.version,o.prepared_at,o.confirmed_at,o.resolved_at,o.created_at,o.updated_at`

type scanner interface{ Scan(...any) error }

func scanOperation(row scanner) (domain.Operation, error) {
	var operation domain.Operation
	var confirmed, resolved pgtype.Timestamptz
	err := row.Scan(&operation.ID, &operation.WorkflowID, &operation.LogicalKey, &operation.TargetService, &operation.Action,
		&operation.RequestFingerprint, &operation.ExpectedEffect, &operation.CapabilityProfile, &operation.State, &operation.AttemptCount,
		&operation.Version, &operation.PreparedAt, &confirmed, &resolved, &operation.CreatedAt, &operation.UpdatedAt)
	if confirmed.Valid {
		value := confirmed.Time
		operation.ConfirmedAt = &value
	}
	if resolved.Valid {
		value := resolved.Time
		operation.ResolvedAt = &value
	}
	return operation, err
}

func getOperationTx(ctx context.Context, query interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, operationID string) (domain.Operation, error) {
	operation, err := scanOperation(query.QueryRow(ctx, `SELECT `+operationColumns+` FROM operations o JOIN workflows w ON w.id=o.workflow_id WHERE o.id=$1`, operationID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Operation{}, domain.NewError(domain.ErrorNotFound, "operation_not_found", "operation was not found")
	}
	if err != nil {
		return domain.Operation{}, unavailable("read operation", err)
	}
	return operation, nil
}

func getOperationForUpdate(ctx context.Context, tx pgx.Tx, operationID string) (domain.Operation, error) {
	operation, err := scanOperation(tx.QueryRow(ctx, `SELECT `+operationColumns+` FROM operations o JOIN workflows w ON w.id=o.workflow_id WHERE o.id=$1 FOR UPDATE OF o`, operationID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Operation{}, domain.NewError(domain.ErrorNotFound, "operation_not_found", "operation was not found")
	}
	if err != nil {
		return domain.Operation{}, unavailable("lock operation", err)
	}
	return operation, nil
}

func getReceiptTx(ctx context.Context, query interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, operationID string) (*domain.Receipt, error) {
	var receipt domain.Receipt
	err := query.QueryRow(ctx, `SELECT id::text,service_receipt_id,receipt_fingerprint,payload,received_at FROM receipts WHERE operation_id=$1`, operationID).
		Scan(&receipt.ID, &receipt.ServiceReceiptID, &receipt.ReceiptFingerprint, &receipt.Payload, &receipt.ReceivedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if err != nil {
		return nil, unavailable("read receipt", err)
	}
	return &receipt, nil
}

func validation(code, message string) error {
	return domain.NewError(domain.ErrorValidation, code, message)
}
func unavailable(action string, err error) error {
	return &domain.Error{Kind: domain.ErrorUnavailable, Code: "database_unavailable", Message: action, Cause: err}
}
