package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jerkeyray/watchkeeper/pkg/client"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestLostAuthorizationNeverDispatches(t *testing.T) {
	var sends, grants atomic.Int32
	op := client.Operation{ID: "op", State: client.StatePrepared, Version: 1}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/operations":
			json.NewEncoder(w).Encode(op)
		case "/v1/operations/op/dispatches":
			grants.Add(1)
			op.DispatchAuthorized = true
			op.Version++
			http.Error(w, "response lost after commit", 503)
		case "/v1/operations/op":
			op.State = client.StateUncertain
			json.NewEncoder(w).Encode(op)
		case "/email/messages":
			sends.Add(1)
			http.Error(w, "must never send", 500)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	worker := Worker{Client: client.New(server.URL, "token", nil), SimulatorURL: server.URL}
	_, err := worker.Execute(ctx, client.PrepareRequest{})
	if !errors.Is(err, ErrUncertain) || sends.Load() != 0 || grants.Load() != 1 {
		t.Fatalf("err=%v sends=%d grants=%d", err, sends.Load(), grants.Load())
	}
}
func TestReconciledOperationContinuesWithoutDispatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/operations" {
			t.Errorf("unexpected dispatch/request %s", r.URL.Path)
		}
		json.NewEncoder(w).Encode(client.Operation{ID: "op", State: client.StateReconciled})
	}))
	defer server.Close()
	worker := Worker{Client: client.New(server.URL, "token", nil), SimulatorURL: server.URL}
	op, err := worker.Execute(context.Background(), client.PrepareRequest{})
	if err != nil || op.State != client.StateReconciled {
		t.Fatalf("%+v %v", op, err)
	}
}
