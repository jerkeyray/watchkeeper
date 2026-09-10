// Package workflow implements the restartable Watchkeeper execution path.
package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jerkeyray/watchkeeper/pkg/client"
)

var ErrUncertain = errors.New("external completion is uncertain")

type Hook func(context.Context, string, client.Operation) error

type Worker struct {
	Client                       *client.Client
	SimulatorURL, SimulatorToken string
	HTTPClient                   *http.Client
	Hook                         Hook
}
type Receipt struct {
	ReceiptID   string    `json:"receipt_id"`
	OperationID string    `json:"operation_id"`
	Status      string    `json:"status"`
	CommittedAt time.Time `json:"committed_at"`
}

func (w *Worker) barrier(ctx context.Context, name string, op client.Operation) error {
	if w.Hook != nil {
		return w.Hook(ctx, name, op)
	}
	return nil
}
func (w *Worker) Execute(ctx context.Context, req client.PrepareRequest) (client.Operation, error) {
	if err := w.barrier(ctx, "before_intent", client.Operation{LogicalKey: req.LogicalKey}); err != nil {
		return client.Operation{}, err
	}
	op, err := w.Client.Prepare(ctx, req)
	if err != nil {
		return op, err
	}
	for {
		switch op.State {
		case client.StateConfirmed, client.StateReconciled:
			return op, nil
		case client.StateUncertain:
			return op, ErrUncertain
		case client.StateRetryable:
			op, err = w.Client.BeginRetry(ctx, op.ID, op.Version, "safe_recovery_retry")
			if err != nil {
				return op, err
			}
		case client.StatePrepared:
			if !op.DispatchAuthorized {
				if err = w.barrier(ctx, "before_dispatch", op); err != nil {
					return op, err
				}
				granted, grantErr := w.Client.AuthorizeDispatch(ctx, op.ID, op.Version)
				if grantErr == nil {
					op = granted
					receipt, sendErr := w.send(ctx, op, req.Request)
					if sendErr == nil {
						if err = w.barrier(ctx, "after_response", op); err != nil {
							return op, err
						}
						confirmed, confirmErr := w.confirm(ctx, op, receipt)
						if confirmErr == nil {
							if err = w.barrier(ctx, "after_persistence", confirmed); err != nil {
								return confirmed, err
							}
							return confirmed, nil
						}
					}
					// Never redispatch a request whose outcome or confirmation is ambiguous.
				}
			}
		default:
			return op, fmt.Errorf("invalid operation state %q", op.State)
		}
		select {
		case <-ctx.Done():
			return op, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
		current, getErr := w.Client.GetOperation(ctx, op.ID)
		if getErr != nil {
			if ctx.Err() != nil {
				return op, ctx.Err()
			}
			continue
		}
		op = current
	}
}
func (w *Worker) send(ctx context.Context, op client.Operation, payload json.RawMessage) (Receipt, error) {
	var receipt Receipt
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, w.SimulatorURL+"/email/messages", bytes.NewReader(payload))
	if err != nil {
		return receipt, err
	}
	r.Header.Set("Authorization", "Bearer "+w.SimulatorToken)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Watchkeeper-Operation-ID", op.ID)
	if op.CapabilityProfile == "idempotency_only" {
		r.Header.Set("Idempotency-Key", op.ID)
	}
	h := w.HTTPClient
	if h == nil {
		h = &http.Client{Timeout: 10 * time.Second}
	}
	res, err := h.Do(r)
	if err != nil {
		return receipt, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusCreated {
		return receipt, fmt.Errorf("dispatch status %d", res.StatusCode)
	}
	err = json.NewDecoder(res.Body).Decode(&receipt)
	if err == nil && (receipt.OperationID != op.ID || receipt.ReceiptID == "" || receipt.Status != "committed") {
		err = errors.New("receipt identity mismatch")
	}
	return receipt, err
}
func (w *Worker) confirm(ctx context.Context, op client.Operation, receipt Receipt) (client.Operation, error) {
	raw, _ := json.Marshal(receipt)
	for i := 0; i < 5; i++ {
		confirmed, err := w.Client.Confirm(ctx, op.ID, client.ConfirmRequest{ExpectedVersion: op.Version, ServiceReceiptID: receipt.ReceiptID, Receipt: raw, ReceivedAt: receipt.CommittedAt})
		if err == nil {
			return confirmed, nil
		}
		current, getErr := w.Client.GetOperation(ctx, op.ID)
		if getErr != nil {
			return op, err
		}
		if current.State == client.StateConfirmed {
			if current.Receipt != nil && current.Receipt.ServiceReceiptID == receipt.ReceiptID {
				return current, nil
			}
			return current, errors.New("conflicting confirmed receipt")
		}
		// Reconciled evidence is checked by the verifier against this operation and payload.
		if current.State == client.StateReconciled {
			return current, nil
		}
		if current.State != client.StatePrepared {
			return current, err
		}
		op = current
	}
	return op, errors.New("confirmation could not be persisted")
}
