package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/jerkeyray/watchkeeper/internal/domain"
	"github.com/pressly/goose/v3"
)

func integrationStore(t *testing.T) (*Postgres, func()) {
	t.Helper()
	dsn := os.Getenv("WK_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("WK_TEST_DATABASE_URL is not set")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatal(err)
	}
	if err := goose.Up(db, "../../migrations/watchkeeper"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	tables := `injected_faults,recovery_decisions,operation_events,observations,reconciliation_attempts,receipts,operations,workflows,experiment_runs`
	if _, err := pool.Exec(context.Background(), "TRUNCATE "+tables+" CASCADE"); err != nil {
		pool.Close()
		t.Fatal(err)
	}
	return NewPostgres(pool), pool.Close
}

func prepareFixture(workflow string) domain.PrepareRequest {
	return domain.PrepareRequest{WorkflowID: workflow, Strategy: domain.StrategyWatchkeeper, LogicalKey: "mail", TargetService: "email", Action: "send", Request: json.RawMessage(`{"recipient":"a@example.invalid"}`), RequestFingerprint: "22d8aa1111111111111111111111111111111111111111111111111111111111", ExpectedEffect: json.RawMessage(`{"recipient":"a@example.invalid"}`), CapabilityProfile: "receipt_status"}
}

func TestPostgresPrepareConfirmAndReplay(t *testing.T) {
	store, cleanup := integrationStore(t)
	defer cleanup()
	ctx := context.Background()
	request := prepareFixture("wf-one")
	operation, created, err := store.Prepare(ctx, request)
	if err != nil || !created {
		t.Fatalf("prepare created=%v err=%v", created, err)
	}
	replayed, created, err := store.Prepare(ctx, request)
	if err != nil || created || replayed.ID != operation.ID {
		t.Fatalf("replay created=%v id=%s err=%v", created, replayed.ID, err)
	}
	reportedReceiptTime := time.Now().UTC().Add(time.Hour)
	confirmed, err := store.Confirm(ctx, operation.ID, domain.ConfirmRequest{ExpectedVersion: operation.Version, ServiceReceiptID: "mail-1", Receipt: json.RawMessage(`{"status":"committed","id":"mail-1"}`), ReceivedAt: reportedReceiptTime})
	if err != nil || confirmed.State != domain.StateConfirmed || confirmed.Receipt == nil {
		t.Fatalf("confirm state=%s err=%v", confirmed.State, err)
	}
	if confirmed.ConfirmedAt == nil || !confirmed.ConfirmedAt.Before(reportedReceiptTime) || !confirmed.Receipt.ReceivedAt.Equal(reportedReceiptTime) {
		t.Fatalf("transition time must be database-controlled while receipt time is preserved")
	}
	again, err := store.Confirm(ctx, operation.ID, domain.ConfirmRequest{ExpectedVersion: 1, ServiceReceiptID: "mail-1", Receipt: json.RawMessage(`{"id":"mail-1","status":"committed"}`), ReceivedAt: time.Now().UTC()})
	if err != nil || again.ID != operation.ID {
		t.Fatalf("idempotent confirm err=%v", err)
	}
	if _, err := store.Confirm(ctx, operation.ID, domain.ConfirmRequest{ExpectedVersion: 1, ServiceReceiptID: "different-mail-id", Receipt: json.RawMessage(`{"id":"mail-1","status":"committed"}`), ReceivedAt: time.Now()}); err == nil {
		t.Fatal("expected service receipt identity conflict")
	}
	events, err := store.ListEvents(ctx, operation.ID, 10, "")
	if err != nil || len(events.Items) != 2 || events.Items[0].EventType != "intent_prepared" || events.Items[1].EventType != "receipt_confirmed" {
		t.Fatalf("events=%+v err=%v", events, err)
	}
}

func TestPostgresConflictsAndRollback(t *testing.T) {
	store, cleanup := integrationStore(t)
	defer cleanup()
	ctx := context.Background()
	request := prepareFixture("wf-conflict")
	operation, _, err := store.Prepare(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	changed := request
	changed.RequestFingerprint = "33d8aa1111111111111111111111111111111111111111111111111111111111"
	if _, _, err := store.Prepare(ctx, changed); err == nil {
		t.Fatal("expected fingerprint conflict")
	}
	_, err = store.Confirm(ctx, operation.ID, domain.ConfirmRequest{ExpectedVersion: 99, ServiceReceiptID: "mail", Receipt: json.RawMessage(`{"status":"committed"}`), ReceivedAt: time.Now()})
	if err == nil {
		t.Fatal("expected version conflict")
	}
	invalid := prepareFixture("wf-rollback")
	invalid.ExpectedEffect = json.RawMessage(`[]`)
	if _, _, err := store.Prepare(ctx, invalid); err == nil {
		t.Fatal("expected constraint error")
	}
	invalid.ExpectedEffect = json.RawMessage(`{"recipient":"a"}`)
	if _, created, err := store.Prepare(ctx, invalid); err != nil || !created {
		t.Fatalf("rollback left partial data: created=%v err=%v", created, err)
	}
}

func TestConcurrentPrepareCreatesOneOperation(t *testing.T) {
	store, cleanup := integrationStore(t)
	defer cleanup()
	ctx := context.Background()
	request := prepareFixture("wf-concurrent")
	var created atomic.Int32
	var failures atomic.Int32
	var wg sync.WaitGroup
	ids := make(chan string, 10)
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			operation, isCreated, err := store.Prepare(ctx, request)
			if err != nil {
				failures.Add(1)
				return
			}
			if isCreated {
				created.Add(1)
			}
			ids <- operation.ID
		}()
	}
	wg.Wait()
	close(ids)
	if failures.Load() != 0 || created.Load() != 1 {
		t.Fatalf("created=%d failures=%d", created.Load(), failures.Load())
	}
	first := ""
	for id := range ids {
		if first == "" {
			first = id
		}
		if id != first {
			t.Fatalf("different operation IDs: %s %s", first, id)
		}
	}
}

func TestRecoveryAbsenceAllowsSameIDRetry(t *testing.T) {
	store, cleanup := integrationStore(t)
	defer cleanup()
	ctx := context.Background()
	operation, _, err := store.Prepare(ctx, prepareFixture("wf-recovery-absent"))
	if err != nil {
		t.Fatal(err)
	}
	claims, err := store.ClaimRecovery(ctx, "coordinator-a", 10, 30*time.Second, 0)
	if err != nil || len(claims) != 1 {
		t.Fatalf("claims=%d err=%v", len(claims), err)
	}
	claim := claims[0]
	resolved, err := store.SubmitRecoveryResult(ctx, operation.ID, domain.RecoveryResult{ClaimToken: claim.ClaimToken, ExpectedVersion: claim.Version, AttemptID: claim.AttemptID, Observation: domain.Observation{Mechanism: domain.MechanismStatus, Outcome: domain.OutcomeAbsent, Authoritative: true, Evidence: json.RawMessage(`{"committed_count":0}`)}})
	if err != nil || resolved.State != domain.StateRetryable {
		t.Fatalf("state=%s err=%v", resolved.State, err)
	}
	retried, err := store.BeginRetry(ctx, operation.ID, resolved.Version, "authoritative absence")
	if err != nil || retried.State != domain.StatePrepared || retried.AttemptCount != 2 || retried.ID != operation.ID {
		t.Fatalf("retry=%+v err=%v", retried, err)
	}
}

func TestRecoveryCompletionReconcilesAndLeaseIsExclusive(t *testing.T) {
	store, cleanup := integrationStore(t)
	defer cleanup()
	ctx := context.Background()
	operation, _, err := store.Prepare(ctx, prepareFixture("wf-recovery-complete"))
	if err != nil {
		t.Fatal(err)
	}
	claims, err := store.ClaimRecovery(ctx, "coordinator-a", 10, 30*time.Second, 0)
	if err != nil || len(claims) != 1 {
		t.Fatalf("claims=%d err=%v", len(claims), err)
	}
	other, err := store.ClaimRecovery(ctx, "coordinator-b", 10, 30*time.Second, 0)
	if err != nil || len(other) != 0 {
		t.Fatalf("second claims=%d err=%v", len(other), err)
	}
	claim := claims[0]
	if _, err := store.SubmitRecoveryResult(ctx, operation.ID, domain.RecoveryResult{ClaimToken: "wrong", ExpectedVersion: claim.Version, AttemptID: claim.AttemptID, Observation: domain.Observation{Mechanism: domain.MechanismStatus, Outcome: domain.OutcomeCompleted, Authoritative: true, Evidence: json.RawMessage(`{}`)}}); err == nil {
		t.Fatal("expected lost lease conflict")
	}
	if _, err := store.RenewClaim(ctx, operation.ID, claim.ClaimToken, 30*time.Second); err != nil {
		t.Fatal(err)
	}
	resolved, err := store.SubmitRecoveryResult(ctx, operation.ID, domain.RecoveryResult{ClaimToken: claim.ClaimToken, ExpectedVersion: claim.Version, AttemptID: claim.AttemptID, Observation: domain.Observation{Mechanism: domain.MechanismStatus, Outcome: domain.OutcomeCompleted, Authoritative: true, ExternalReference: "mail-1", Evidence: json.RawMessage(`{"committed_count":1}`)}})
	if err != nil || resolved.State != domain.StateReconciled {
		t.Fatalf("state=%s err=%v", resolved.State, err)
	}
}

func TestUnknownRecoveryBecomesUncertainAndCanBeResolved(t *testing.T) {
	store, cleanup := integrationStore(t)
	defer cleanup()
	ctx := context.Background()
	operation, _, err := store.Prepare(ctx, prepareFixture("wf-recovery-unknown"))
	if err != nil {
		t.Fatal(err)
	}
	claims, err := store.ClaimRecovery(ctx, "coordinator", 1, 30*time.Second, 0)
	if err != nil || len(claims) != 1 {
		t.Fatalf("claims=%d err=%v", len(claims), err)
	}
	claim := claims[0]
	uncertain, err := store.SubmitRecoveryResult(ctx, operation.ID, domain.RecoveryResult{ClaimToken: claim.ClaimToken, ExpectedVersion: claim.Version, AttemptID: claim.AttemptID, Observation: domain.Observation{Mechanism: domain.MechanismNone, Outcome: domain.OutcomeUnknown, Authoritative: false, Evidence: json.RawMessage(`{}`)}})
	if err != nil || uncertain.State != domain.StateUncertain {
		t.Fatalf("state=%s err=%v", uncertain.State, err)
	}
	resolved, err := store.ManualResolve(ctx, operation.ID, domain.ManualResolution{ExpectedVersion: uncertain.Version, Actor: "reviewer", Outcome: "completed", Reason: "ledger inspected", EvidenceReference: "ledger/1"})
	if err != nil || resolved.State != domain.StateReconciled {
		t.Fatalf("state=%s err=%v", resolved.State, err)
	}
}
