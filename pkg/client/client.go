package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Strategy string
type OperationState string

const (
	StrategyBlindRetry     Strategy       = "blind_retry"
	StrategyIdempotencyKey Strategy       = "idempotency_key_retry"
	StrategyCheckpoint     Strategy       = "checkpoint_recovery"
	StrategyWatchkeeper    Strategy       = "watchkeeper"
	StatePrepared          OperationState = "prepared"
	StateConfirmed         OperationState = "confirmed"
	StateReconciled        OperationState = "reconciled"
	StateRetryable         OperationState = "retryable"
	StateUncertain         OperationState = "uncertain"
)

type Receipt struct {
	ID                 string          `json:"receipt_id"`
	ServiceReceiptID   string          `json:"service_receipt_id"`
	ReceiptFingerprint string          `json:"receipt_fingerprint"`
	Payload            json.RawMessage `json:"payload"`
	ReceivedAt         time.Time       `json:"received_at"`
}

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

type Event struct {
	ID               int64     `json:"id"`
	OperationID      string    `json:"operation_id"`
	EventType        string    `json:"event_type"`
	OperationVersion int64     `json:"operation_version"`
	CreatedAt        time.Time `json:"created_at"`
}

type OperationPage struct {
	Items      []Operation `json:"items"`
	NextCursor string      `json:"next_cursor,omitempty"`
}
type EventPage struct {
	Items      []Event `json:"items"`
	NextCursor string  `json:"next_cursor,omitempty"`
}

type Observation struct {
	Mechanism         string          `json:"mechanism"`
	Outcome           string          `json:"outcome"`
	Authoritative     bool            `json:"authoritative"`
	ExternalReference string          `json:"external_reference,omitempty"`
	Evidence          json.RawMessage `json:"evidence"`
}
type RecoveryClaim struct {
	Operation      Operation `json:"operation"`
	ClaimToken     string    `json:"claim_token"`
	AttemptID      string    `json:"attempt_id"`
	LeaseExpiresAt time.Time `json:"lease_expires_at"`
	Version        int64     `json:"version"`
}

type PrepareRequest struct {
	WorkflowID         string          `json:"workflow_id"`
	Strategy           Strategy        `json:"strategy"`
	ExperimentRunID    *string         `json:"experiment_run_id,omitempty"`
	LogicalKey         string          `json:"logical_key"`
	TargetService      string          `json:"target_service"`
	Action             string          `json:"action"`
	Request            json.RawMessage `json:"request"`
	RequestFingerprint string          `json:"request_fingerprint,omitempty"`
	ExpectedEffect     json.RawMessage `json:"expected_effect"`
	CapabilityProfile  string          `json:"capability_profile"`
	TraceContext       json.RawMessage `json:"trace_context,omitempty"`
}

type ConfirmRequest struct {
	ExpectedVersion  int64           `json:"expected_version"`
	ServiceReceiptID string          `json:"service_receipt_id"`
	Receipt          json.RawMessage `json:"receipt"`
	ReceivedAt       time.Time       `json:"received_at"`
}

type ListOptions struct {
	WorkflowID    string
	State         OperationState
	TargetService string
	CreatedAfter  *time.Time
	Limit         int
	Cursor        string
}

type Error struct {
	Status    int
	Code      string
	Message   string
	RequestID string
	Details   map[string]any
}

func (e *Error) Error() string {
	return fmt.Sprintf("watchkeeper API %s (%d): %s", e.Code, e.Status, e.Message)
}
func (e *Error) Conflict() bool { return e.Status == http.StatusConflict }

type Client struct {
	baseURL    string
	token      string
	httpClient *http.Client
}

func New(baseURL, token string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), token: token, httpClient: httpClient}
}

func (c *Client) Prepare(ctx context.Context, request PrepareRequest) (Operation, error) {
	var result Operation
	err := c.do(ctx, http.MethodPost, "/v1/operations", request, &result)
	return result, err
}

func (c *Client) Confirm(ctx context.Context, id string, request ConfirmRequest) (Operation, error) {
	var result Operation
	err := c.do(ctx, http.MethodPost, "/v1/operations/"+url.PathEscape(id)+"/confirmations", request, &result)
	return result, err
}

func (c *Client) GetOperation(ctx context.Context, id string) (Operation, error) {
	var result Operation
	err := c.do(ctx, http.MethodGet, "/v1/operations/"+url.PathEscape(id), nil, &result)
	return result, err
}

func (c *Client) ListOperations(ctx context.Context, options ListOptions) (OperationPage, error) {
	query := url.Values{}
	if options.WorkflowID != "" {
		query.Set("workflow_id", options.WorkflowID)
	}
	if options.State != "" {
		query.Set("state", string(options.State))
	}
	if options.TargetService != "" {
		query.Set("target_service", options.TargetService)
	}
	if options.CreatedAfter != nil {
		query.Set("created_after", options.CreatedAfter.Format(time.RFC3339))
	}
	if options.Limit > 0 {
		query.Set("limit", strconv.Itoa(options.Limit))
	}
	if options.Cursor != "" {
		query.Set("cursor", options.Cursor)
	}
	var result OperationPage
	err := c.do(ctx, http.MethodGet, "/v1/operations?"+query.Encode(), nil, &result)
	return result, err
}

func (c *Client) ListEvents(ctx context.Context, id string, limit int, cursor string) (EventPage, error) {
	query := url.Values{}
	if limit > 0 {
		query.Set("limit", strconv.Itoa(limit))
	}
	if cursor != "" {
		query.Set("cursor", cursor)
	}
	var result EventPage
	err := c.do(ctx, http.MethodGet, "/v1/operations/"+url.PathEscape(id)+"/events?"+query.Encode(), nil, &result)
	return result, err
}

func (c *Client) BeginRetry(ctx context.Context, id string, expectedVersion int64, reason string) (Operation, error) {
	var result Operation
	err := c.do(ctx, http.MethodPost, "/v1/operations/"+url.PathEscape(id)+"/attempts", map[string]any{"expected_version": expectedVersion, "reason": reason}, &result)
	return result, err
}
func (c *Client) RequestReconciliation(ctx context.Context, id string, expectedVersion int64, actor, reason string) (Operation, error) {
	var result Operation
	err := c.do(ctx, http.MethodPost, "/v1/operations/"+url.PathEscape(id)+"/reconciliation-requests", map[string]any{"expected_version": expectedVersion, "actor": actor, "reason": reason}, &result)
	return result, err
}
func (c *Client) ClaimRecovery(ctx context.Context, workerID string, limit int, lease time.Duration) ([]RecoveryClaim, error) {
	var result struct {
		Claims []RecoveryClaim `json:"claims"`
	}
	err := c.do(ctx, http.MethodPost, "/v1/recovery/claims", map[string]any{"worker_id": workerID, "limit": limit, "lease_duration_ms": lease.Milliseconds()}, &result)
	return result.Claims, err
}
func (c *Client) RenewClaim(ctx context.Context, id, token string, lease time.Duration) (time.Time, error) {
	var result struct {
		Expires time.Time `json:"lease_expires_at"`
	}
	err := c.do(ctx, http.MethodPost, "/v1/recovery/claims/"+url.PathEscape(id)+"/renew", map[string]any{"claim_token": token, "lease_duration_ms": lease.Milliseconds()}, &result)
	return result.Expires, err
}
func (c *Client) SubmitRecoveryResult(ctx context.Context, id string, claim RecoveryClaim, observation Observation) (Operation, error) {
	var result Operation
	err := c.do(ctx, http.MethodPost, "/v1/recovery/claims/"+url.PathEscape(id)+"/results", map[string]any{"claim_token": claim.ClaimToken, "expected_version": claim.Version, "attempt_id": claim.AttemptID, "observation": observation}, &result)
	return result, err
}
func (c *Client) ManualResolve(ctx context.Context, id string, expectedVersion int64, actor, outcome, reason, evidence string) (Operation, error) {
	var result Operation
	err := c.do(ctx, http.MethodPost, "/v1/operations/"+url.PathEscape(id)+"/manual-resolutions", map[string]any{"expected_version": expectedVersion, "actor": actor, "outcome": outcome, "reason": reason, "evidence_reference": evidence}, &result)
	return result, err
}

func (c *Client) do(ctx context.Context, method, path string, body, output any) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var envelope struct {
			Error struct {
				Code      string         `json:"code"`
				Message   string         `json:"message"`
				RequestID string         `json:"request_id"`
				Details   map[string]any `json:"details"`
			} `json:"error"`
		}
		_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&envelope)
		return &Error{Status: resp.StatusCode, Code: envelope.Error.Code, Message: envelope.Error.Message, RequestID: envelope.Error.RequestID, Details: envelope.Error.Details}
	}
	if output != nil {
		if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(output); err != nil {
			return err
		}
	}
	return nil
}
