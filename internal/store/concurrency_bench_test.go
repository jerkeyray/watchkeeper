package store

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
)

// TestDispatchSingleWinnerAtScale quantifies the single-send guarantee for the
// report: across many operations, concurrent authorization must elect exactly
// one sender every time.
func TestDispatchSingleWinnerAtScale(t *testing.T) {
	if testing.Short() {
		t.Skip("scale run")
	}
	s, shutdown := integrationStore(t)
	defer shutdown()
	ctx := context.Background()
	const operations, racers = 500, 12
	exactlyOne := 0
	for i := 0; i < operations; i++ {
		op, created, err := s.Prepare(ctx, prepareFixture(fmt.Sprintf("scale-%d", i)))
		if err != nil || !created {
			t.Fatalf("prepare created=%v err=%v", created, err)
		}
		var wins atomic.Int32
		var wg sync.WaitGroup
		start := make(chan struct{})
		for j := 0; j < racers; j++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				if _, err := s.AuthorizeDispatch(ctx, op.ID, op.Version); err == nil {
					wins.Add(1)
				}
			}()
		}
		close(start)
		wg.Wait()
		if wins.Load() == 1 {
			exactlyOne++
		}
	}
	t.Logf("RESULT exactly-one-sender %d/%d operations (%d concurrent authorizers each)", exactlyOne, operations, racers)
	if exactlyOne != operations {
		t.Fatalf("single-send violated: %d/%d", exactlyOne, operations)
	}
}
