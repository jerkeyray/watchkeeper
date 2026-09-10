package store

import (
	"context"
	"github.com/jackc/pgx/v5"
	"github.com/jerkeyray/watchkeeper/internal/domain"
)

// AuthorizeDispatch is deliberately not replayable: a lost response is ambiguous.
func (p *Postgres) AuthorizeDispatch(ctx context.Context, id string, version int64) (domain.Operation, error) {
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return domain.Operation{}, unavailable("begin dispatch", err)
	}
	defer tx.Rollback(ctx)
	op, err := getOperationForUpdate(ctx, tx, id)
	if err != nil {
		return op, err
	}
	var claimed bool
	if err = tx.QueryRow(ctx, `SELECT lease_token_hash IS NOT NULL FROM operations WHERE id=$1`, id).Scan(&claimed); err != nil {
		return op, err
	}
	if op.State != domain.StatePrepared || op.Version != version || op.DispatchAuthorized || claimed {
		return domain.Operation{}, domain.NewError(domain.ErrorConflict, "dispatch_not_authorized", "dispatch already issued, recovery claimed, or operation version/state changed")
	}
	_, err = tx.Exec(ctx, `UPDATE operations SET dispatch_authorized=true,version=version+1,updated_at=now() WHERE id=$1`, id)
	if err != nil {
		return op, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO operation_events(operation_id,event_type,from_state,to_state,operation_version,source,actor,reason_code,details) VALUES($1,'dispatch_authorized','prepared','prepared',$2,'worker','workflow-worker','single_send_authorized','{}')`, id, version+1)
	if err != nil {
		return op, err
	}
	op, err = getOperationTx(ctx, tx, id)
	if err != nil {
		return op, err
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.Operation{}, unavailable("commit dispatch", err)
	}
	return op, nil
}
