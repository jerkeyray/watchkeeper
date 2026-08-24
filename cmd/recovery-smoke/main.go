package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/jerkeyray/watchkeeper/pkg/client"
)

func main() {
	apiURL := env("WK_API_URL", "http://localhost:8080")
	publicToken := os.Getenv("WK_AUTH_TOKEN")
	adminToken := os.Getenv("WK_ADMIN_TOKEN")
	simURL := env("SIMULATOR_URL", "http://localhost:8090")
	simToken := os.Getenv("SIM_AUTH_TOKEN")
	if publicToken == "" || adminToken == "" || simToken == "" {
		fatal("WK_AUTH_TOKEN, WK_ADMIN_TOKEN, and SIM_AUTH_TOKEN are required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	public := client.New(apiURL, publicToken, nil)
	admin := client.New(apiURL, adminToken, nil)
	workflowID := uuid.NewString()
	payload, _ := json.Marshal(map[string]string{"recipient": "recovery@example.invalid", "template": "confirmation", "logical_message_key": "recovery-smoke"})
	operation, err := public.Prepare(ctx, client.PrepareRequest{WorkflowID: workflowID, Strategy: client.StrategyWatchkeeper, LogicalKey: "send-confirmation", TargetService: "email", Action: "send", Request: payload, ExpectedEffect: payload, CapabilityProfile: "receipt_status"})
	if err != nil {
		fatal("prepare: %v", err)
	}
	if err := dispatch(ctx, simURL, simToken, operation.ID, payload); err != nil {
		fatal("dispatch: %v", err)
	}
	queued, err := admin.RequestReconciliation(ctx, operation.ID, operation.Version, "recovery-smoke", "simulate lost receipt")
	if err != nil {
		fatal("queue reconciliation: %v", err)
	}
	current := queued
	for current.State != client.StateReconciled {
		select {
		case <-ctx.Done():
			fatal("timed out waiting for reconciliation")
		case <-time.After(200 * time.Millisecond):
		}
		current, err = public.GetOperation(ctx, operation.ID)
		if err != nil {
			fatal("get operation: %v", err)
		}
		if current.State == client.StateRetryable || current.State == client.StateUncertain {
			fatal("unexpected recovery state %s", current.State)
		}
	}
	events, err := public.ListEvents(ctx, operation.ID, 20, "")
	if err != nil {
		fatal("events: %v", err)
	}
	if len(events.Items) != 4 || events.Items[0].EventType != "intent_prepared" || events.Items[1].EventType != "reconciliation_requested" || events.Items[2].EventType != "recovery_claimed" || events.Items[3].EventType != "recovery_result" {
		fatal("unexpected audit events: %+v", events.Items)
	}
	json.NewEncoder(os.Stdout).Encode(map[string]any{"workflow_id": workflowID, "operation_id": operation.ID, "state": current.State, "audit_events": len(events.Items), "redispatched": false, "validated": true})
}
func dispatch(ctx context.Context, baseURL, token, operationID string, payload []byte) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/email/messages", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Watchkeeper-Operation-ID", operationID)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return fmt.Errorf("simulator returned %d: %s", response.StatusCode, body)
	}
	return nil
}
func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
func fatal(format string, args ...any) { fmt.Fprintf(os.Stderr, format+"\n", args...); os.Exit(1) }
