package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClientSendsTokenAndDecodesOperation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer token" {
			t.Error("missing token")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"operation_id":"op","state":"prepared"}`))
	}))
	defer server.Close()
	operation, err := New(server.URL, "token", nil).GetOperation(context.Background(), "op")
	if err != nil {
		t.Fatal(err)
	}
	if operation.State != StatePrepared {
		t.Fatalf("state=%s", operation.State)
	}
}
func TestClientReturnsTypedConflict(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":{"code":"receipt_conflict","message":"different","request_id":"req"}}`))
	}))
	defer server.Close()
	_, err := New(server.URL, "token", nil).GetOperation(context.Background(), "op")
	apiErr, ok := err.(*Error)
	if !ok || !apiErr.Conflict() || apiErr.Code != "receipt_conflict" {
		t.Fatalf("unexpected error: %#v", err)
	}
}
