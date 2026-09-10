package recovery

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jerkeyray/watchkeeper/pkg/client"
)

func TestEmailVerifierClassifiesStatus(t *testing.T) {
	cases := []struct {
		count, status int
		outcome       string
		authoritative bool
	}{{0, 200, "unknown", false}, {1, 200, "completed", true}, {2, 200, "contradictory", false}, {0, 503, "transient_error", false}}
	for _, test := range cases {
		t.Run(fmt.Sprintf("count-%d-status-%d", test.count, test.status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer token" {
					t.Error("missing token")
				}
				w.WriteHeader(test.status)
				if test.status == 200 {
					fmt.Fprintf(w, `{"committed_count":%d,"authoritative":true,"operation_id":"op","latest":{"receipt_id":"mail","operation_id":"op","recipient":"a","template":"b","logical_message_key":"c"}}`, test.count)
				}
			}))
			defer server.Close()
			observation, err := EmailVerifier{BaseURL: server.URL, Token: "token"}.Verify(context.Background(), client.Operation{ID: "op", CapabilityProfile: "receipt_status", ExpectedEffect: json.RawMessage(`{"recipient":"a","template":"b","logical_message_key":"c"}`)})
			if err != nil {
				t.Fatal(err)
			}
			if observation.Outcome != test.outcome || observation.Authoritative != test.authoritative {
				t.Fatalf("observation=%+v", observation)
			}
		})
	}
}

func TestEmailVerifierRejectsMismatchedEvidence(t *testing.T) {
	for _, body := range []string{
		`{"operation_id":"wrong","committed_count":1,"authoritative":true,"latest":{"receipt_id":"r","operation_id":"op","recipient":"a","template":"b","logical_message_key":"c"}}`,
		`{"operation_id":"op","committed_count":1,"authoritative":true,"latest":{"receipt_id":"r","operation_id":"op","recipient":"other","template":"b","logical_message_key":"c"}}`,
		`{"operation_id":"op","committed_count":1,"authoritative":true}`,
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) }))
		op := client.Operation{ID: "op", CapabilityProfile: "receipt_status", ExpectedEffect: json.RawMessage(`{"recipient":"a","template":"b","logical_message_key":"c"}`)}
		got, err := (EmailVerifier{BaseURL: server.URL}).Verify(context.Background(), op)
		server.Close()
		if err != nil || got.Outcome != "contradictory" {
			t.Fatalf("%+v %v", got, err)
		}
	}
}
func TestIdempotencyRequiresServiceContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"profile":"none","durable_key_payload_binding":false}`)
	}))
	defer server.Close()
	got, err := (EmailVerifier{BaseURL: server.URL}).Verify(context.Background(), client.Operation{CapabilityProfile: "idempotency_only"})
	if err != nil || got.Authoritative {
		t.Fatalf("%+v %v", got, err)
	}
}
