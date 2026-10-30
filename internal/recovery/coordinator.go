package recovery

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jerkeyray/watchkeeper/pkg/client"
)

type Verifier interface {
	Verify(context.Context, client.Operation) (client.Observation, error)
}

type EmailVerifier struct {
	BaseURL    string
	Token      string
	HTTPClient *http.Client
}

func (v EmailVerifier) Verify(ctx context.Context, operation client.Operation) (client.Observation, error) {
	httpClient := v.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 5 * time.Second}
	}
	if operation.CapabilityProfile == "none" {
		return client.Observation{Mechanism: "none", Outcome: "unknown", Evidence: json.RawMessage(`{}`)}, nil
	}
	if operation.CapabilityProfile == "idempotency_only" {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(v.BaseURL, "/")+"/email/capabilities", nil)
		if err != nil {
			return client.Observation{}, err
		}
		req.Header.Set("Authorization", "Bearer "+v.Token)
		res, err := httpClient.Do(req)
		if err != nil {
			return client.Observation{Mechanism: "idempotent_repeat", Outcome: "transient_error", Evidence: json.RawMessage(`{}`)}, nil
		}
		defer res.Body.Close()
		var capability struct {
			Profile    string `json:"profile"`
			Guaranteed bool   `json:"durable_key_payload_binding"`
		}
		if res.StatusCode != 200 || json.NewDecoder(res.Body).Decode(&capability) != nil {
			return client.Observation{Mechanism: "idempotent_repeat", Outcome: "transient_error", Evidence: json.RawMessage(`{}`)}, nil
		}
		return client.Observation{Mechanism: "idempotent_repeat", Outcome: "unknown", Authoritative: capability.Profile == "idempotency_only" && capability.Guaranteed, Evidence: json.RawMessage(`{"contract":"durable_key_payload_binding"}`)}, nil
	}
	endpoint := strings.TrimRight(v.BaseURL, "/") + "/email/operations/" + operation.ID
	if operation.CapabilityProfile == "indirect_unique" {
		var expected map[string]string
		if err := json.Unmarshal(operation.ExpectedEffect, &expected); err != nil {
			return client.Observation{}, err
		}
		values := url.Values{}
		for _, key := range []string{"recipient", "template", "logical_message_key"} {
			values.Set(key, expected[key])
		}
		endpoint = strings.TrimRight(v.BaseURL, "/") + "/email/inspect?" + values.Encode()
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return client.Observation{}, err
	}
	request.Header.Set("Authorization", "Bearer "+v.Token)
	response, err := httpClient.Do(request)
	if err != nil {
		return client.Observation{Mechanism: "status_lookup", Outcome: "transient_error", Authoritative: false, Evidence: json.RawMessage(`{}`)}, nil
	}
	defer response.Body.Close()
	if response.StatusCode >= 500 {
		return client.Observation{Mechanism: "status_lookup", Outcome: "transient_error", Authoritative: false, Evidence: json.RawMessage(`{}`)}, nil
	}
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return client.Observation{}, fmt.Errorf("status lookup returned %d: %s", response.StatusCode, body)
	}
	var status struct {
		OperationID    string `json:"operation_id"`
		CommittedCount int    `json:"committed_count"`
		Authoritative  bool   `json:"authoritative"`
		Latest         *struct {
			ReceiptID         string `json:"receipt_id"`
			OperationID       string `json:"operation_id"`
			Recipient         string `json:"recipient"`
			Template          string `json:"template"`
			LogicalMessageKey string `json:"logical_message_key"`
		} `json:"latest"`
	}
	if err := json.NewDecoder(response.Body).Decode(&status); err != nil {
		return client.Observation{}, err
	}
	evidence, _ := json.Marshal(status)
	observation := client.Observation{Mechanism: "status_lookup", Outcome: "unknown", Evidence: evidence}
	if operation.CapabilityProfile == "indirect_unique" {
		observation.Mechanism = "indirect_lookup"
	}
	if status.CommittedCount > 1 {
		observation.Outcome = "contradictory"
		return observation, nil
	}
	if status.CommittedCount == 1 {
		var expected struct {
			Recipient         string `json:"recipient"`
			Template          string `json:"template"`
			LogicalMessageKey string `json:"logical_message_key"`
		}
		err := json.Unmarshal(operation.ExpectedEffect, &expected)
		valid := err == nil && expected.Recipient != "" && expected.Template != "" && expected.LogicalMessageKey != "" && status.Latest != nil
		if valid {
			valid = status.Latest.ReceiptID != "" && status.Latest.Recipient == expected.Recipient && status.Latest.Template == expected.Template && status.Latest.LogicalMessageKey == expected.LogicalMessageKey
		}
		if valid && operation.CapabilityProfile != "indirect_unique" {
			valid = status.OperationID == operation.ID && status.Latest.OperationID == operation.ID
		}
		if !valid {
			observation.Outcome = "contradictory"
			return observation, nil
		}
		observation.Outcome = "completed"
		observation.Authoritative = status.Authoritative
		observation.ExternalReference = status.Latest.ReceiptID
	}
	return observation, nil
}

type Coordinator struct {
	Hook          func(context.Context, string, client.Operation) error
	Client        *client.Client
	Verifier      Verifier
	WorkerID      string
	BatchSize     int
	LeaseDuration time.Duration
	Logger        *slog.Logger
}

func (c *Coordinator) RunOnce(ctx context.Context) (int, error) {
	claims, err := c.Client.ClaimRecovery(ctx, c.WorkerID, c.BatchSize, c.LeaseDuration)
	if err != nil {
		return 0, err
	}
	processed := 0
	for _, claim := range claims {
		// Reported before verification so that recovery latency can be split
		// into detection delay and verification/resolution time.
		if c.Hook != nil {
			if err := c.Hook(ctx, "recovery_claimed", claim.Operation); err != nil {
				return processed, err
			}
		}
		observation, verifyErr := c.Verifier.Verify(ctx, claim.Operation)
		if verifyErr != nil {
			observation = client.Observation{Mechanism: "status_lookup", Outcome: "transient_error", Evidence: json.RawMessage(`{}`)}
		}
		if c.Hook != nil {
			if err := c.Hook(ctx, "coordinator_before_result", claim.Operation); err != nil {
				return processed, err
			}
		}
		operation, err := c.Client.SubmitRecoveryResult(ctx, claim.Operation.ID, claim, observation)
		if err != nil {
			c.Logger.Error("submit recovery result", "operation_id", claim.Operation.ID, "error", err)
			continue
		}
		if c.Hook != nil {
			if err := c.Hook(ctx, "coordinator_after_result", operation); err != nil {
				return processed, err
			}
		}
		processed++
		c.Logger.Info("operation reconciled", "operation_id", operation.ID, "state", operation.State, "outcome", observation.Outcome)
	}
	return processed, nil
}
