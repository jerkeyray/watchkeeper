package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jerkeyray/watchkeeper/internal/domain"
)

type fakeStore struct {
	readyErr       error
	prepared       domain.Operation
	created        bool
	prepareRequest domain.PrepareRequest
}

func (f *fakeStore) Ping(context.Context) error        { return f.readyErr }
func (f *fakeStore) SchemaReady(context.Context) error { return f.readyErr }
func (f *fakeStore) Prepare(_ context.Context, r domain.PrepareRequest) (domain.Operation, bool, error) {
	f.prepareRequest = r
	return f.prepared, f.created, nil
}
func (f *fakeStore) Confirm(context.Context, string, domain.ConfirmRequest) (domain.Operation, error) {
	return f.prepared, nil
}
func (f *fakeStore) GetOperation(context.Context, string) (domain.Operation, error) {
	return f.prepared, nil
}
func (f *fakeStore) ListOperations(context.Context, domain.OperationFilter) (domain.OperationPage, error) {
	return domain.OperationPage{Items: []domain.Operation{}}, nil
}
func (f *fakeStore) ListEvents(context.Context, string, int, string) (domain.EventPage, error) {
	return domain.EventPage{Items: []domain.Event{}}, nil
}
func (f *fakeStore) BeginRetry(context.Context, string, int64, string) (domain.Operation, error) {
	return f.prepared, nil
}
func (f *fakeStore) RequestReconciliation(context.Context, string, int64, string, string) (domain.Operation, error) {
	return f.prepared, nil
}
func (f *fakeStore) ClaimRecovery(context.Context, string, int, time.Duration, time.Duration) ([]domain.RecoveryClaim, error) {
	return []domain.RecoveryClaim{}, nil
}
func (f *fakeStore) RenewClaim(context.Context, string, string, time.Duration) (time.Time, error) {
	return time.Now(), nil
}
func (f *fakeStore) SubmitRecoveryResult(context.Context, string, domain.RecoveryResult) (domain.Operation, error) {
	return f.prepared, nil
}
func (f *fakeStore) ManualResolve(context.Context, string, domain.ManualResolution) (domain.Operation, error) {
	return f.prepared, nil
}

func testServer(store *fakeStore) *httptest.Server {
	return httptest.NewServer(New(store, slog.New(slog.NewTextHandler(io.Discard, nil)), "public", "admin").Handler())
}
func TestHealthIsPublicAndReadinessChecksStore(t *testing.T) {
	fake := &fakeStore{}
	server := testServer(fake)
	defer server.Close()
	for _, path := range []string{"/health/live", "/health/ready"} {
		response, err := http.Get(server.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("%s: %d", path, response.StatusCode)
		}
	}
	fake.readyErr = errors.New("down")
	response, err := http.Get(server.URL + "/health/ready")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("got %d", response.StatusCode)
	}
}
func TestAPIRequiresBearerToken(t *testing.T) {
	server := testServer(&fakeStore{})
	defer server.Close()
	response, err := http.Get(server.URL + "/v1/operations")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("got %d", response.StatusCode)
	}
}
func TestPrepareCalculatesFingerprint(t *testing.T) {
	operation := domain.Operation{ID: "018f22d8-1951-7c8b-9f6b-dac0d181a9ad", State: domain.StatePrepared, PreparedAt: time.Now()}
	fake := &fakeStore{prepared: operation, created: true}
	server := testServer(fake)
	defer server.Close()
	body := []byte(`{"workflow_id":"wf","strategy":"watchkeeper","logical_key":"mail","target_service":"email","action":"send","request":{"b":2,"a":1},"expected_effect":{"recipient":"a"},"capability_profile":"receipt_status"}`)
	request, _ := http.NewRequest(http.MethodPost, server.URL+"/v1/operations", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer public")
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		raw, _ := io.ReadAll(response.Body)
		t.Fatalf("got %d: %s", response.StatusCode, raw)
	}
	if len(fake.prepareRequest.RequestFingerprint) != 64 {
		t.Fatalf("fingerprint=%q", fake.prepareRequest.RequestFingerprint)
	}
}
func TestPrepareRejectsUnknownFields(t *testing.T) {
	server := testServer(&fakeStore{})
	defer server.Close()
	request, _ := http.NewRequest(http.MethodPost, server.URL+"/v1/operations", bytes.NewBufferString(`{"unknown":true}`))
	request.Header.Set("Authorization", "Bearer public")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		var value any
		_ = json.NewDecoder(response.Body).Decode(&value)
		t.Fatalf("got %d: %+v", response.StatusCode, value)
	}
}

func TestPrepareRejectsUnsupportedOperationAndNonObjectRequest(t *testing.T) {
	server := testServer(&fakeStore{})
	defer server.Close()
	cases := []string{`{"workflow_id":"wf","strategy":"watchkeeper","logical_key":"x","target_service":"calendar","action":"send","request":{},"expected_effect":{},"capability_profile":"receipt_status"}`, `{"workflow_id":"wf","strategy":"watchkeeper","logical_key":"x","target_service":"email","action":"send","request":[],"expected_effect":{},"capability_profile":"receipt_status"}`}
	for _, body := range cases {
		request, _ := http.NewRequest(http.MethodPost, server.URL+"/v1/operations", bytes.NewBufferString(body))
		request.Header.Set("Authorization", "Bearer public")
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusUnprocessableEntity {
			t.Fatalf("got %d for %s", response.StatusCode, body)
		}
	}
}

func TestRecoveryClaimsRequireAdminToken(t *testing.T) {
	server := testServer(&fakeStore{})
	defer server.Close()
	body := []byte(`{"worker_id":"coordinator","limit":1,"lease_duration_ms":30000}`)
	for token, want := range map[string]int{"public": http.StatusUnauthorized, "admin": http.StatusOK} {
		request, _ := http.NewRequest(http.MethodPost, server.URL+"/v1/recovery/claims", bytes.NewReader(body))
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("Content-Type", "application/json")
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != want {
			t.Fatalf("token=%s got=%d want=%d", token, response.StatusCode, want)
		}
	}
}
