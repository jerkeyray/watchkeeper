package domain

import "fmt"

var allowedTransitions = map[OperationState]map[OperationState]bool{
	StatePrepared: {
		StateConfirmed:  true,
		StateReconciled: true,
		StateRetryable:  true,
		StateUncertain:  true,
	},
	StateRetryable: {
		StatePrepared: true,
	},
	StateUncertain: {
		StateReconciled: true,
		StateRetryable:  true,
	},
}

func ValidState(s OperationState) bool {
	switch s {
	case StatePrepared, StateConfirmed, StateReconciled, StateRetryable, StateUncertain:
		return true
	default:
		return false
	}
}

func CanTransition(from, to OperationState) bool {
	return allowedTransitions[from][to]
}

func ValidateTransition(from, to OperationState) error {
	if !ValidState(from) || !ValidState(to) {
		return fmt.Errorf("unknown transition %q -> %q", from, to)
	}
	if !CanTransition(from, to) {
		return fmt.Errorf("transition %q -> %q is not allowed", from, to)
	}
	return nil
}

func ValidStrategy(s Strategy) bool {
	switch s {
	case StrategyBlindRetry, StrategyIdempotencyKey, StrategyCheckpoint, StrategyWatchkeeper:
		return true
	default:
		return false
	}
}
