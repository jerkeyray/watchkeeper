package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/jerkeyray/watchkeeper/internal/recovery"
	"github.com/jerkeyray/watchkeeper/pkg/client"
)

func main() {
	apiURL := env("WK_API_URL", "http://localhost:8080")
	adminToken := os.Getenv("WK_ADMIN_TOKEN")
	simulatorURL := env("SIMULATOR_URL", "http://localhost:8090")
	simulatorToken := os.Getenv("SIM_AUTH_TOKEN")
	if adminToken == "" || simulatorToken == "" {
		slog.Error("WK_ADMIN_TOKEN and SIM_AUTH_TOKEN are required")
		os.Exit(2)
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	workerID := env("WK_COORDINATOR_ID", "coordinator-"+uuid.NewString())
	batch := envInt("WK_CLAIM_BATCH_SIZE", 20)
	lease := time.Duration(envInt("WK_LEASE_DURATION_MS", 30000)) * time.Millisecond
	poll := time.Duration(envInt("WK_COORDINATOR_POLL_MS", 1000)) * time.Millisecond
	coordinator := &recovery.Coordinator{Client: client.New(apiURL, adminToken, nil), Verifier: recovery.EmailVerifier{BaseURL: simulatorURL, Token: simulatorToken}, WorkerID: workerID, BatchSize: batch, LeaseDuration: lease, Logger: logger}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	for {
		if _, err := coordinator.RunOnce(ctx); err != nil && ctx.Err() == nil {
			logger.Error("recovery cycle failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
func envInt(key string, fallback int) int {
	if value := os.Getenv(key); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil {
			return parsed
		}
	}
	return fallback
}
