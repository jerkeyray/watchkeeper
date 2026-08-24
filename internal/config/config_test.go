package config

import (
	"strings"
	"testing"
)

func TestLoadAndRedact(t *testing.T) {
	values := map[string]string{"WK_DATABASE_URL": "postgres://secret", "WK_AUTH_TOKEN": "public-secret", "WK_ADMIN_TOKEN": "admin-secret", "WK_HTTP_ADDR": ":9000"}
	cfg, err := Load([]string{"-log-level", "debug"}, func(key string) string { return values[key] })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTPAddr != ":9000" || cfg.LogLevel != "debug" {
		t.Fatalf("unexpected config: %+v", cfg)
	}
	rendered := cfg.String()
	for _, secret := range []string{"postgres://secret", "public-secret", "admin-secret"} {
		if strings.Contains(rendered, secret) {
			t.Fatalf("secret leaked: %s", rendered)
		}
	}
}
func TestLoadRequiresDurabilityAndAuth(t *testing.T) {
	_, err := Load(nil, func(string) string { return "" })
	if err == nil {
		t.Fatal("expected validation error")
	}
	for _, part := range []string{"WK_DATABASE_URL", "public auth token"} {
		if !strings.Contains(err.Error(), part) {
			t.Errorf("missing %q in %v", part, err)
		}
	}
}
func TestFlagOverridesEnvironment(t *testing.T) {
	values := map[string]string{"WK_DATABASE_URL": "postgres://env", "WK_AUTH_TOKEN": "token"}
	cfg, err := Load([]string{"-database-url", "postgres://flag"}, func(key string) string { return values[key] })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DatabaseURL != "postgres://flag" {
		t.Fatal("flag did not override environment")
	}
}
