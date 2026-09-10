package simulator

import (
	"context"
	"database/sql"
	"errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"os"
	"sync"
	"testing"
)

func TestConcurrentIdempotencyAndUnkeyedDelivery(t *testing.T) {
	dsn := os.Getenv("SIM_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SIM_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = goose.SetDialect("postgres"); err != nil {
		t.Fatal(err)
	}
	if err = goose.Up(db, "../../migrations/simulator"); err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	s := NewPostgresEmailStore(pool)
	id := uuid.NewString()
	request := EmailRequest{Recipient: "test@example.invalid", Template: "step-1", LogicalMessageKey: id, IdempotencyKey: id}
	var wg sync.WaitGroup
	receipts := make(chan string, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			receipt, err := s.Commit(ctx, id, request)
			if err != nil {
				t.Error(err)
				return
			}
			receipts <- receipt.ReceiptID
		}()
	}
	wg.Wait()
	close(receipts)
	first := ""
	for r := range receipts {
		if first == "" {
			first = r
		}
		if r != first {
			t.Error("idempotency returned different receipt")
		}
	}
	status, err := s.Status(ctx, id)
	if err != nil || status.CommittedCount != 1 {
		t.Fatalf("%+v %v", status, err)
	}
	count, err := s.DispatchCount(ctx, id)
	if err != nil || count != 8 {
		t.Fatalf("dispatches %d %v", count, err)
	}
	changed := request
	changed.Template = "changed"
	if _, err = s.Commit(ctx, id, changed); !errors.Is(err, ErrKeyConflict) {
		t.Fatalf("payload conflict %v", err)
	}
	request.IdempotencyKey = ""
	if _, err = s.Commit(ctx, id, request); err != nil {
		t.Fatal(err)
	}
	status, err = s.Status(ctx, id)
	if err != nil || status.CommittedCount != 2 {
		t.Fatalf("unkeyed call was deduplicated: %+v %v", status, err)
	}
}
