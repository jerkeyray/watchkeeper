package benchmark

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jerkeyray/watchkeeper/internal/recovery"
	"github.com/jerkeyray/watchkeeper/internal/workflow"
	"github.com/jerkeyray/watchkeeper/pkg/client"
	"io"
	"log/slog"
	"net/http"
	"os"
	"time"
)

type ChildConfig struct {
	RestartIndex                                                                                     int
	ExecutionID, Strategy, Profile, APIURL, SimulatorURL, ControlURL, Token, AdminToken, BaselineDSN string
	DeadlineMS                                                                                       int
}
type Event struct {
	Phase       string    `json:"phase"`
	Barrier     string    `json:"barrier"`
	Step        string    `json:"step"`
	OperationID string    `json:"operation_id,omitempty"`
	Attempt     int       `json:"attempt"`
	At          time.Time `json:"at"`
}

func (c ChildConfig) emit(ctx context.Context, barrier, step, id string, attempt int) error {
	raw, _ := json.Marshal(Event{Phase: "reached", Barrier: barrier, Step: step, OperationID: id, Attempt: attempt, At: time.Now().UTC()})
	req, err := http.NewRequestWithContext(ctx, "POST", c.ControlURL+"/events", bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return fmt.Errorf("barrier rejected %d", res.StatusCode)
	}
	return nil
}
func Child(role string, input io.Reader) error {
	var c ChildConfig
	if err := json.NewDecoder(input).Decode(&c); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(c.DeadlineMS)*time.Millisecond)
	defer cancel()
	if role == "coordinator" {
		coord := recovery.Coordinator{Client: client.New(c.APIURL, c.AdminToken, nil), Verifier: recovery.EmailVerifier{BaseURL: c.SimulatorURL, Token: c.Token}, WorkerID: uuid.NewString(), BatchSize: 1, LeaseDuration: 5 * time.Second, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
		coord.Hook = func(ctx context.Context, name string, op client.Operation) error {
			return c.emit(ctx, name, op.LogicalKey, op.ID, op.AttemptCount)
		}
		for ctx.Err() == nil {
			_, _ = coord.RunOnce(ctx)
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(100 * time.Millisecond):
			}
		}
		return nil
	}
	var baseline *pgxpool.Pool
	if c.Strategy == "checkpoint_recovery" {
		var err error
		baseline, err = pgxpool.New(ctx, c.BaselineDSN)
		if err != nil {
			return err
		}
		defer baseline.Close()
	}
	for attempt := c.RestartIndex + 1; ctx.Err() == nil; attempt++ {
		err := executeChild(ctx, c, baseline, attempt)
		if err == nil {
			return nil
		}
		if errors.Is(err, workflow.ErrUncertain) {
			return c.emit(ctx, "uncertain", "", "", attempt)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	return ctx.Err()
}
func executeChild(ctx context.Context, c ChildConfig, baseline *pgxpool.Pool, attempt int) error {
	wk := workflow.Worker{Client: client.New(c.APIURL, c.Token, nil), SimulatorURL: c.SimulatorURL, SimulatorToken: c.Token}
	wk.Hook = func(ctx context.Context, name string, op client.Operation) error {
		return c.emit(ctx, name, op.LogicalKey, op.ID, attempt)
	}
	for step := 1; step <= 3; step++ {
		key := fmt.Sprintf("step-%d", step)
		logical := c.ExecutionID + "/" + key
		payload, _ := json.Marshal(map[string]any{"recipient": "researcher@example.invalid", "template": key, "logical_message_key": logical, "experiment_run_id": c.ExecutionID})
		// Expected fields intentionally exclude experimental bookkeeping.
		expected, _ := json.Marshal(map[string]string{"recipient": "researcher@example.invalid", "template": key, "logical_message_key": logical})
		if c.Strategy == "watchkeeper" {
			op, err := wk.Execute(ctx, client.PrepareRequest{WorkflowID: c.ExecutionID, ExperimentRunID: &c.ExecutionID, Strategy: client.StrategyWatchkeeper, LogicalKey: key, TargetService: "email", Action: "send", Request: payload, ExpectedEffect: expected, CapabilityProfile: c.Profile})
			if errors.Is(err, workflow.ErrUncertain) {
				_ = c.emit(ctx, "step_uncertain", key, op.ID, attempt)
			}
			if err != nil {
				return err
			}
			if err = c.emit(ctx, "step_acknowledged", key, op.ID, attempt); err != nil {
				return err
			}
			continue
		}
		if baseline != nil {
			var exists bool
			err := baseline.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM checkpoints WHERE run_id=$1 AND step=$2)`, c.ExecutionID, key).Scan(&exists)
			if err != nil {
				return err
			}
			if exists {
				continue
			}
		}
		if err := c.emit(ctx, "before_intent", key, "", attempt); err != nil {
			return err
		}
		if err := c.emit(ctx, "before_dispatch", key, "", attempt); err != nil {
			return err
		}
		opID := uuid.NewSHA1(uuid.NameSpaceOID, []byte(logical)).String()
		req, err := http.NewRequestWithContext(ctx, "POST", c.SimulatorURL+"/email/messages", bytes.NewReader(payload))
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+c.Token)
		req.Header.Set("X-Watchkeeper-Operation-ID", opID)
		req.Header.Set("Content-Type", "application/json")
		if c.Strategy == "idempotency_key_retry" {
			req.Header.Set("Idempotency-Key", opID)
		}
		h := http.Client{Timeout: 10 * time.Second}
		res, err := h.Do(req)
		if err != nil {
			return err
		}
		var receipt workflow.Receipt
		err = json.NewDecoder(res.Body).Decode(&receipt)
		res.Body.Close()
		if err != nil {
			return err
		}
		if res.StatusCode != 201 {
			return fmt.Errorf("dispatch %d", res.StatusCode)
		}
		if err = c.emit(ctx, "after_response", key, opID, attempt); err != nil {
			return err
		}
		if baseline != nil {
			_, err = baseline.Exec(ctx, `INSERT INTO checkpoints(run_id,step,receipt_id) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, c.ExecutionID, key, receipt.ReceiptID)
			if err != nil {
				return err
			}
		}
		if err = c.emit(ctx, "after_persistence", key, opID, attempt); err != nil {
			return err
		}
		if err = c.emit(ctx, "step_acknowledged", key, opID, attempt); err != nil {
			return err
		}
	}
	return nil
}

// ChildExit is kept at the executable boundary so killed workers cannot run cleanup.
func ChildExit(role string) {
	if err := Child(role, os.Stdin); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
