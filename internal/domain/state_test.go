package domain

import "testing"

func TestTransitions(t *testing.T) {
	states := []OperationState{StatePrepared, StateConfirmed, StateReconciled, StateRetryable, StateUncertain}
	allowed := map[[2]OperationState]bool{
		{StatePrepared, StateConfirmed}: true, {StatePrepared, StateReconciled}: true, {StatePrepared, StateRetryable}: true, {StatePrepared, StateUncertain}: true,
		{StateRetryable, StatePrepared}: true, {StateUncertain, StateReconciled}: true, {StateUncertain, StateRetryable}: true,
	}
	for _, from := range states {
		for _, to := range states {
			err := ValidateTransition(from, to)
			if allowed[[2]OperationState{from, to}] && err != nil {
				t.Errorf("expected %s -> %s: %v", from, to, err)
			}
			if !allowed[[2]OperationState{from, to}] && err == nil {
				t.Errorf("unexpected %s -> %s", from, to)
			}
		}
	}
}

func TestUnknownTransition(t *testing.T) {
	if ValidateTransition("bogus", StatePrepared) == nil {
		t.Fatal("expected unknown state error")
	}
}
