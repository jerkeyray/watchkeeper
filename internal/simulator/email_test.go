package simulator

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type fakeEmailStore struct{ effects []EmailEffect }

func (f *fakeEmailStore) Ping(context.Context) error { return nil }

func (f *fakeEmailStore) Commit(_ context.Context, operationID string, request EmailRequest) (EmailEffect, error) {
	effect := EmailEffect{ReceiptID: "018f22d8-1951-7c8b-9f6b-dac0d181a9ae", OperationID: operationID, Recipient: request.Recipient, Template: request.Template, LogicalMessageKey: request.LogicalMessageKey, CommittedAt: time.Now()}
	f.effects = append(f.effects, effect)
	return effect, nil
}
func (f *fakeEmailStore) Status(_ context.Context, operationID string) (EmailStatus, error) {
	status := EmailStatus{OperationID: operationID, Authoritative: true}
	for i := range f.effects {
		if f.effects[i].OperationID == operationID {
			status.CommittedCount++
			copy := f.effects[i]
			status.Latest = &copy
		}
	}
	return status, nil
}
func TestEmailCommitAndStatus(t *testing.T) {
	store := &fakeEmailStore{}
	server := httptest.NewServer(NewServer(store, "public", "admin", slog.New(slog.NewTextHandler(io.Discard, nil))).Handler())
	defer server.Close()
	operationID := "018f22d8-1951-7c8b-9f6b-dac0d181a9ad"
	request, _ := http.NewRequest(http.MethodPost, server.URL+"/email/messages", bytes.NewBufferString(`{"recipient":"a@example.invalid","template":"confirmation","logical_message_key":"mail"}`))
	request.Header.Set("Authorization", "Bearer public")
	request.Header.Set("X-Watchkeeper-Operation-ID", operationID)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("got %d", response.StatusCode)
	}
	request, _ = http.NewRequest(http.MethodGet, server.URL+"/email/operations/"+operationID, nil)
	request.Header.Set("Authorization", "Bearer public")
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status got %d", response.StatusCode)
	}
	if len(store.effects) != 1 {
		t.Fatalf("effects=%d", len(store.effects))
	}
}
func TestEmailRequiresAuth(t *testing.T) {
	server := httptest.NewServer(NewServer(&fakeEmailStore{}, "public", "admin", slog.Default()).Handler())
	defer server.Close()
	response, err := http.Post(server.URL+"/email/messages", "application/json", bytes.NewBufferString(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("got %d", response.StatusCode)
	}
}
