package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/jerkeyray/watchkeeper/pkg/client"
)

type simulatorReceipt struct {
	ReceiptID   string    `json:"receipt_id"`
	OperationID string    `json:"operation_id"`
	Status      string    `json:"status"`
	CommittedAt time.Time `json:"committed_at"`
}
type result struct {
	WorkflowID        string                `json:"workflow_id"`
	OperationID       string                `json:"operation_id"`
	State             client.OperationState `json:"state"`
	EffectCommittedAt time.Time             `json:"effect_committed_at"`
	ConfirmedAt       *time.Time            `json:"confirmed_at,omitempty"`
	AuditEvents       int                   `json:"audit_events"`
	GroundTruthCount  int                   `json:"ground_truth_count"`
	Validated         bool                  `json:"validated"`
}

type simulatorStatus struct {
	OperationID    string `json:"operation_id"`
	CommittedCount int    `json:"committed_count"`
	Authoritative  bool   `json:"authoritative"`
}

func main() {
	apiURL := flag.String("api-url", env("WK_API_URL", "http://localhost:8080"), "Watchkeeper API URL")
	apiToken := flag.String("api-token", os.Getenv("WK_AUTH_TOKEN"), "Watchkeeper API token")
	simulatorURL := flag.String("simulator-url", env("SIMULATOR_URL", "http://localhost:8090"), "simulator URL")
	simulatorToken := flag.String("simulator-token", os.Getenv("SIM_AUTH_TOKEN"), "simulator token")
	workflowID := flag.String("workflow-id", uuid.NewString(), "workflow ID")
	recipient := flag.String("recipient", "researcher@example.invalid", "recipient")
	template := flag.String("template", "confirmation", "template")
	flag.Parse()
	if *apiToken == "" || *simulatorToken == "" {
		fatal("api-token and simulator-token are required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	wk := client.New(*apiURL, *apiToken, nil)
	request, _ := json.Marshal(map[string]string{"recipient": *recipient, "template": *template, "logical_message_key": "confirmation"})
	operation, err := wk.Prepare(ctx, client.PrepareRequest{WorkflowID: *workflowID, Strategy: client.StrategyWatchkeeper, LogicalKey: "send-confirmation", TargetService: "email", Action: "send", Request: request, ExpectedEffect: request, CapabilityProfile: "receipt_status"})
	if err != nil {
		fatal("prepare: %v", err)
	}
	if operation.State == client.StateConfirmed {
		emit(result{WorkflowID: *workflowID, OperationID: operation.ID, State: operation.State, ConfirmedAt: operation.ConfirmedAt})
		return
	}
	if operation.State != client.StatePrepared {
		fatal("operation is %s; dispatch is not allowed", operation.State)
	}
	receipt, err := dispatch(ctx, *simulatorURL, *simulatorToken, operation.ID, request)
	if err != nil {
		fatal("dispatch: %v", err)
	}
	receiptJSON, _ := json.Marshal(receipt)
	confirmed, err := wk.Confirm(ctx, operation.ID, client.ConfirmRequest{ExpectedVersion: operation.Version, ServiceReceiptID: receipt.ReceiptID, Receipt: receiptJSON, ReceivedAt: receipt.CommittedAt})
	if err != nil {
		current, getErr := wk.GetOperation(ctx, operation.ID)
		if getErr == nil && current.State == client.StateConfirmed {
			confirmed = current
		} else {
			fatal("confirm: %v", err)
		}
	}
	events, err := wk.ListEvents(ctx, operation.ID, 10, "")
	if err != nil {
		fatal("read audit events: %v", err)
	}
	status, err := fetchStatus(ctx, *simulatorURL, *simulatorToken, operation.ID)
	if err != nil {
		fatal("read simulator ground truth: %v", err)
	}
	if confirmed.State != client.StateConfirmed || confirmed.ConfirmedAt == nil || len(events.Items) != 2 || events.Items[0].EventType != "intent_prepared" || events.Items[1].EventType != "receipt_confirmed" || status.CommittedCount != 1 || !status.Authoritative || operation.PreparedAt.After(receipt.CommittedAt) || confirmed.ConfirmedAt.Before(receipt.CommittedAt) {
		fatal("smoke invariants failed: state=%s events=%d effects=%d", confirmed.State, len(events.Items), status.CommittedCount)
	}
	emit(result{WorkflowID: *workflowID, OperationID: operation.ID, State: confirmed.State, EffectCommittedAt: receipt.CommittedAt, ConfirmedAt: confirmed.ConfirmedAt, AuditEvents: len(events.Items), GroundTruthCount: status.CommittedCount, Validated: true})
}
func dispatch(ctx context.Context, baseURL, token, operationID string, payload []byte) (simulatorReceipt, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/email/messages", bytes.NewReader(payload))
	if err != nil {
		return simulatorReceipt{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Watchkeeper-Operation-ID", operationID)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return simulatorReceipt{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return simulatorReceipt{}, fmt.Errorf("simulator returned %d: %s", resp.StatusCode, body)
	}
	var receipt simulatorReceipt
	err = json.NewDecoder(resp.Body).Decode(&receipt)
	return receipt, err
}
func fetchStatus(ctx context.Context, baseURL, token, operationID string) (simulatorStatus, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/email/operations/"+operationID, nil)
	if err != nil {
		return simulatorStatus{}, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return simulatorStatus{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return simulatorStatus{}, fmt.Errorf("status endpoint returned %d", response.StatusCode)
	}
	var status simulatorStatus
	err = json.NewDecoder(response.Body).Decode(&status)
	return status, err
}
func emit(value any) {
	if err := json.NewEncoder(os.Stdout).Encode(value); err != nil {
		fatal("encode result: %v", err)
	}
}
func fatal(format string, args ...any) { fmt.Fprintf(os.Stderr, format+"\n", args...); os.Exit(1) }
func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
