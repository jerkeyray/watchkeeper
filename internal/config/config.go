package config

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"
)

type Config struct {
	HTTPAddr        string
	DatabaseURL     string
	AuthToken       string
	AdminToken      string
	LogLevel        string
	ShutdownTimeout time.Duration
}

func Load(args []string, getenv func(string) string) (Config, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	cfg := Config{
		HTTPAddr:        envOr(getenv, "WK_HTTP_ADDR", ":8080"),
		DatabaseURL:     getenv("WK_DATABASE_URL"),
		AuthToken:       getenv("WK_AUTH_TOKEN"),
		AdminToken:      getenv("WK_ADMIN_TOKEN"),
		LogLevel:        envOr(getenv, "WK_LOG_LEVEL", "info"),
		ShutdownTimeout: 10 * time.Second,
	}

	var authFile, adminFile string
	flags := flag.NewFlagSet("watchkeeper", flag.ContinueOnError)
	flags.StringVar(&cfg.HTTPAddr, "http-addr", cfg.HTTPAddr, "HTTP listen address")
	flags.StringVar(&cfg.DatabaseURL, "database-url", cfg.DatabaseURL, "PostgreSQL connection URL")
	flags.StringVar(&cfg.LogLevel, "log-level", cfg.LogLevel, "log level")
	flags.StringVar(&authFile, "auth-token-file", getenv("WK_AUTH_TOKEN_FILE"), "public API token file")
	flags.StringVar(&adminFile, "admin-token-file", getenv("WK_ADMIN_TOKEN_FILE"), "admin API token file")
	flags.DurationVar(&cfg.ShutdownTimeout, "shutdown-timeout", cfg.ShutdownTimeout, "graceful shutdown timeout")
	if err := flags.Parse(args); err != nil {
		return Config{}, err
	}
	var err error
	if authFile != "" {
		cfg.AuthToken, err = readSecret(authFile)
		if err != nil {
			return Config{}, fmt.Errorf("read auth token: %w", err)
		}
	}
	if adminFile != "" {
		cfg.AdminToken, err = readSecret(adminFile)
		if err != nil {
			return Config{}, fmt.Errorf("read admin token: %w", err)
		}
	}
	if cfg.AdminToken == "" {
		cfg.AdminToken = cfg.AuthToken
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c Config) Validate() error {
	var errs []error
	if c.DatabaseURL == "" {
		errs = append(errs, errors.New("WK_DATABASE_URL is required"))
	}
	if c.AuthToken == "" {
		errs = append(errs, errors.New("public auth token is required"))
	}
	if c.AdminToken == "" {
		errs = append(errs, errors.New("admin auth token is required"))
	}
	switch strings.ToLower(c.LogLevel) {
	case "debug", "info", "warn", "error":
	default:
		errs = append(errs, fmt.Errorf("invalid log level %q", c.LogLevel))
	}
	if c.ShutdownTimeout <= 0 {
		errs = append(errs, errors.New("shutdown timeout must be positive"))
	}
	return errors.Join(errs...)
}

func (c Config) String() string {
	return fmt.Sprintf("http_addr=%s database_url=[redacted] auth_token=[redacted] admin_token=[redacted] log_level=%s shutdown_timeout=%s", c.HTTPAddr, c.LogLevel, c.ShutdownTimeout)
}

func envOr(getenv func(string) string, key, fallback string) string {
	if value := getenv(key); value != "" {
		return value
	}
	return fallback
}

func readSecret(path string) (string, error) {
	value, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	secret := strings.TrimSpace(string(value))
	if secret == "" {
		return "", errors.New("secret file is empty")
	}
	return secret, nil
}
