package recovery

import (
	"context"
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
	}{{0, 200, "absent", true}, {1, 200, "completed", true}, {2, 200, "contradictory", true}, {0, 503, "transient_error", false}}
	for _, test := range cases {
		t.Run(fmt.Sprintf("count-%d-status-%d", test.count, test.status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer token" {
					t.Error("missing token")
				}
				w.WriteHeader(test.status)
				if test.status == 200 {
					fmt.Fprintf(w, `{"committed_count":%d,"authoritative":true,"latest":{"receipt_id":"mail"}}`, test.count)
				}
			}))
			defer server.Close()
			observation, err := EmailVerifier{BaseURL: server.URL, Token: "token"}.Verify(context.Background(), client.Operation{ID: "op"})
			if err != nil {
				t.Fatal(err)
			}
			if observation.Outcome != test.outcome || observation.Authoritative != test.authoritative {
				t.Fatalf("observation=%+v", observation)
			}
		})
	}
}
