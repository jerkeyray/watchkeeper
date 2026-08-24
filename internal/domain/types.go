package domain

import (
	"encoding/json"
	"time"
)

type OperationState string

const (
	StatePrepared   OperationState = "prepared"
	StateConfirmed  OperationState = "confirmed"
	StateReconciled OperationState = "reconciled"
	StateRetryable  OperationState = "retryable"
	StateUncertain  OperationState = "uncertain"
)

type Strategy string

const (
	StrategyBlindRetry     Strategy = "blind_retry"
	StrategyIdempotencyKey Strategy = "idempotency_key_retry"
	StrategyCheckpoint     Strategy = "checkpoint_recovery"
	StrategyWatchkeeper    Strategy = "watchkeeper"
)

type Operation struct {
	ID                 string          `json:"operation_id"`
	WorkflowID         string          `json:"workflow_id"`
	LogicalKey         string          `json:"logical_key"`
	TargetService      string          `json:"target_service"`
	Action             string          `json:"action"`
	RequestFingerprint string          `json:"request_fingerprint"`
	ExpectedEffect     json.RawMessage `json:"expected_effect"`
	CapabilityProfile  string          `json:"capability_profile"`
	State              OperationState  `json:"state"`
	AttemptCount       int             `json:"attempt_count"`
	Version            int64           `json:"version"`
	PreparedAt         time.Time       `json:"prepared_at"`
	ConfirmedAt        *time.Time      `json:"confirmed_at,omitempty"`
	ResolvedAt         *time.Time      `json:"resolved_at,omitempty"`
	CreatedAt          time.Time       `json:"created_at"`
	UpdatedAt          time.Time       `json:"updated_at"`
	Receipt            *Receipt        `json:"receipt,omitempty"`
}

type Receipt struct {
	ID                 string          `json:"receipt_id"`
	ServiceReceiptID   string          `json:"service_receipt_id"`
	ReceiptFingerprint string          `json:"receipt_fingerprint"`
	Payload            json.RawMessage `json:"payload"`
	ReceivedAt         time.Time       `json:"received_at"`
}

type Event struct {
	ID               int64           `json:"id"`
	OperationID      string          `json:"operation_id"`
	EventType        string          `json:"event_type"`
	FromState        *OperationState `json:"from_state,omitempty"`
	ToState          *OperationState `json:"to_state,omitempty"`
	OperationVersion int64           `json:"operation_version"`
	Source           string          `json:"source"`
	Actor            string          `json:"actor"`
	ReasonCode       string          `json:"reason_code"`
	Details          json.RawMessage `json:"details"`
	CreatedAt        time.Time       `json:"created_at"`
}

type PrepareRequest struct {
	WorkflowID         string
	Strategy           Strategy
	ExperimentRunID    *string
	LogicalKey         string
	TargetService      string
	Action             string
	Request            json.RawMessage
	RequestFingerprint string
	ExpectedEffect     json.RawMessage
	CapabilityProfile  string
	TraceContext       json.RawMessage
}

type ConfirmRequest struct {
	ExpectedVersion  int64
	ServiceReceiptID string
	Receipt          json.RawMessage
	ReceivedAt       time.Time
}

type OperationFilter struct {
	WorkflowID    string
	State         OperationState
	TargetService string
	CreatedAfter  *time.Time
	Limit         int
	Cursor        string
}

type OperationPage struct {
	Items      []Operation `json:"items"`
	NextCursor string      `json:"next_cursor,omitempty"`
}

type EventPage struct {
	Items      []Event `json:"items"`
	NextCursor string  `json:"next_cursor,omitempty"`
}

type ObservationOutcome string
type VerificationMechanism string

const (
	OutcomeCompleted          ObservationOutcome    = "completed"
	OutcomeAbsent             ObservationOutcome    = "absent"
	OutcomeUnknown            ObservationOutcome    = "unknown"
	OutcomeTransient          ObservationOutcome    = "transient_error"
	OutcomeContradictory      ObservationOutcome    = "contradictory"
	MechanismReceipt          VerificationMechanism = "receipt_lookup"
	MechanismStatus           VerificationMechanism = "status_lookup"
	MechanismIndirect         VerificationMechanism = "indirect_lookup"
	MechanismIdempotentRepeat VerificationMechanism = "idempotent_repeat"
	MechanismNone             VerificationMechanism = "none"
)

type Observation struct {
	Mechanism         VerificationMechanism `json:"mechanism"`
	Outcome           ObservationOutcome    `json:"outcome"`
	Authoritative     bool                  `json:"authoritative"`
	ExternalReference string                `json:"external_reference,omitempty"`
	Evidence          json.RawMessage       `json:"evidence"`
}

type RecoveryClaim struct {
	Operation      Operation `json:"operation"`
	ClaimToken     string    `json:"claim_token"`
	AttemptID      string    `json:"attempt_id"`
	LeaseExpiresAt time.Time `json:"lease_expires_at"`
	Version        int64     `json:"version"`
}

type RecoveryResult struct {
	ClaimToken      string
	ExpectedVersion int64
	AttemptID       string
	Observation     Observation
}

type ManualResolution struct {
	ExpectedVersion   int64
	Actor             string
	Outcome           string
	Reason            string
	EvidenceReference string
}
