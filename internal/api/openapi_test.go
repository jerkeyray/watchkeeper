package api

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
)

func TestOpenAPIContainsImplementedRoutes(t *testing.T) {
	raw, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	contract := string(raw)
	for _, route := range []string{"/health/live:", "/health/ready:", "/metrics:", "/v1/operations:", "/v1/operations/{operation_id}:", "/v1/operations/{operation_id}/confirmations:", "/v1/operations/{operation_id}/events:", "/v1/recovery/claims:", "/v1/recovery/claims/{operation_id}/results:"} {
		if !strings.Contains(contract, route) {
			t.Errorf("missing route %s", route)
		}
	}
}

func TestOpenAPIParsesAndValidates(t *testing.T) {
	document, err := openapi3.NewLoader().LoadFromFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if err := document.Validate(context.Background()); err != nil {
		t.Fatal(err)
	}
}
