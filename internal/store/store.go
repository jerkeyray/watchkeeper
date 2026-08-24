package store

import (
	"context"
	"time"

	"github.com/jerkeyray/watchkeeper/internal/domain"
)

type Store interface {
	Ping(context.Context) error
	SchemaReady(context.Context) error
	Prepare(context.Context, domain.PrepareRequest) (domain.Operation, bool, error)
	Confirm(context.Context, string, domain.ConfirmRequest) (domain.Operation, error)
	GetOperation(context.Context, string) (domain.Operation, error)
	ListOperations(context.Context, domain.OperationFilter) (domain.OperationPage, error)
	ListEvents(context.Context, string, int, string) (domain.EventPage, error)
	BeginRetry(context.Context, string, int64, string) (domain.Operation, error)
	RequestReconciliation(context.Context, string, int64, string, string) (domain.Operation, error)
	ClaimRecovery(context.Context, string, int, time.Duration, time.Duration) ([]domain.RecoveryClaim, error)
	RenewClaim(context.Context, string, string, time.Duration) (time.Time, error)
	SubmitRecoveryResult(context.Context, string, domain.RecoveryResult) (domain.Operation, error)
	ManualResolve(context.Context, string, domain.ManualResolution) (domain.Operation, error)
}

type Readiness struct {
	Store Store
	Now   func() time.Time
}
