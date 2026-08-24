package recovery

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
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
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(v.BaseURL, "/")+"/email/operations/"+operation.ID, nil)
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
		CommittedCount int  `json:"committed_count"`
		Authoritative  bool `json:"authoritative"`
		Latest         *struct {
			ReceiptID string `json:"receipt_id"`
		} `json:"latest"`
	}
	if err := json.NewDecoder(response.Body).Decode(&status); err != nil {
		return client.Observation{}, err
	}
	evidence, _ := json.Marshal(status)
	outcome := "absent"
	external := ""
	if status.CommittedCount > 0 {
		outcome = "completed"
		if status.Latest != nil {
			external = status.Latest.ReceiptID
		}
	}
	if status.CommittedCount > 1 {
		outcome = "contradictory"
	}
	return client.Observation{Mechanism: "status_lookup", Outcome: outcome, Authoritative: status.Authoritative, ExternalReference: external, Evidence: evidence}, nil
}

type Coordinator struct {
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
		observation, verifyErr := c.Verifier.Verify(ctx, claim.Operation)
		if verifyErr != nil {
			observation = client.Observation{Mechanism: "status_lookup", Outcome: "transient_error", Evidence: json.RawMessage(`{}`)}
		}
		operation, err := c.Client.SubmitRecoveryResult(ctx, claim.Operation.ID, claim, observation)
		if err != nil {
			c.Logger.Error("submit recovery result", "operation_id", claim.Operation.ID, "error", err)
			continue
		}
		processed++
		c.Logger.Info("operation reconciled", "operation_id", operation.ID, "state", operation.State, "outcome", observation.Outcome)
	}
	return processed, nil
}
