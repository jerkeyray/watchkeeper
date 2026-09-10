package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jerkeyray/watchkeeper/internal/domain"
	"github.com/pressly/goose/v3"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestDispatchSingleWinnerAndConservativeAbsence(t *testing.T) {
	s, close := integrationStore(t)
	defer close()
	ctx := context.Background()
	op, _, err := s.Prepare(ctx, prepareFixture("dispatch"))
	if err != nil {
		t.Fatal(err)
	}
	var wins atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.AuthorizeDispatch(ctx, op.ID, op.Version); err == nil {
				wins.Add(1)
			}
		}()
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("dispatch winners %d", wins.Load())
	}
	op, err = s.GetOperation(ctx, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.AuthorizeDispatch(ctx, op.ID, op.Version); err == nil {
		t.Fatal("replay granted")
	}
	claims, err := s.ClaimRecovery(ctx, "coordinator", 1, time.Minute, 0)
	if err != nil || len(claims) != 1 {
		t.Fatalf("claims %v %v", claims, err)
	}
	c := claims[0]
	op, err = s.SubmitRecoveryResult(ctx, op.ID, domain.RecoveryResult{ClaimToken: c.ClaimToken, AttemptID: c.AttemptID, ExpectedVersion: c.Version, Observation: domain.Observation{Mechanism: domain.MechanismStatus, Outcome: domain.OutcomeAbsent, Authoritative: true, Evidence: json.RawMessage(`{}`)}})
	if err != nil || op.State != domain.StateUncertain {
		t.Fatalf("late commit unsafe: %+v %v", op, err)
	}
}
func TestRecoveryFencesNeverDispatchedAttempt(t *testing.T) {
	s, close := integrationStore(t)
	defer close()
	ctx := context.Background()
	op, _, err := s.Prepare(ctx, prepareFixture("never-dispatched"))
	if err != nil {
		t.Fatal(err)
	}
	claims, err := s.ClaimRecovery(ctx, "coordinator", 1, time.Minute, 0)
	if err != nil {
		t.Fatal(err)
	}
	c := claims[0]
	if _, err = s.AuthorizeDispatch(ctx, op.ID, op.Version); err == nil {
		t.Fatal("old worker authorized after recovery claim")
	}
	next, err := s.SubmitRecoveryResult(ctx, op.ID, domain.RecoveryResult{ClaimToken: c.ClaimToken, AttemptID: c.AttemptID, ExpectedVersion: c.Version, Observation: domain.Observation{Mechanism: domain.MechanismNone, Outcome: domain.OutcomeUnknown}})
	if err != nil || next.State != domain.StateRetryable {
		t.Fatalf("%+v %v", next, err)
	}
	retry, err := s.BeginRetry(ctx, op.ID, next.Version, "never_dispatched")
	if err != nil {
		t.Fatal(err)
	}
	if retry.ID != op.ID || retry.DispatchAuthorized {
		t.Fatal("retry identity/authorization")
	}
	if _, err = s.AuthorizeDispatch(ctx, retry.ID, retry.Version); err != nil {
		t.Fatal(err)
	}
}
func TestRecoveryExcludesBaselines(t *testing.T) {
	s, close := integrationStore(t)
	defer close()
	req := prepareFixture("baseline")
	req.Strategy = domain.StrategyBlindRetry
	if _, _, err := s.Prepare(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	claims, err := s.ClaimRecovery(context.Background(), "c", 10, time.Minute, 0)
	if err != nil || len(claims) != 0 {
		t.Fatalf("%v %v", claims, err)
	}
}

func TestLegacyUnresolvedMigrationIsConservative(t *testing.T) {
	dsn := os.Getenv("WK_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("WK_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	schema := "migration_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err = pool.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer pool.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	db, err := sql.Open("pgx", u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = goose.SetDialect("postgres"); err != nil {
		t.Fatal(err)
	}
	if err = goose.UpTo(db, "../../migrations/watchkeeper", 1); err != nil {
		t.Fatal(err)
	}
	wf, id := uuid.NewString(), uuid.NewString()
	if _, err = db.Exec(`INSERT INTO workflows(id,external_workflow_id,strategy) VALUES($1,'legacy','watchkeeper')`, wf); err != nil {
		t.Fatal(err)
	}
	insert := `INSERT INTO operations(id,workflow_id,logical_key,target_service,action,request_fingerprint,expected_effect,capability_profile,state) VALUES($1,$2,$3,'email','send',$4,'{}','receipt_status','prepared')`
	if _, err = db.Exec(insert, id, wf, "old", strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	if err = goose.Up(db, "../../migrations/watchkeeper"); err != nil {
		t.Fatal(err)
	}
	var authorized bool
	if err = db.QueryRow(`SELECT dispatch_authorized FROM operations WHERE id=$1`, id).Scan(&authorized); err != nil || !authorized {
		t.Fatalf("legacy authorization %v %v", authorized, err)
	}
	newID := uuid.NewString()
	if _, err = db.Exec(insert, newID, wf, "new", strings.Repeat("b", 64)); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT dispatch_authorized FROM operations WHERE id=$1`, newID).Scan(&authorized); err != nil || authorized {
		t.Fatalf("new authorization %v %v", authorized, err)
	}
}
