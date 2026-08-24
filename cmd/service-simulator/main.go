package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jerkeyray/watchkeeper/internal/simulator"
)

func main() {
	addr := env("SIM_HTTP_ADDR", ":8090")
	databaseURL := os.Getenv("SIM_DATABASE_URL")
	token := os.Getenv("SIM_AUTH_TOKEN")
	adminToken := env("SIM_ADMIN_TOKEN", token)
	if databaseURL == "" || token == "" {
		slog.Error("SIM_DATABASE_URL and SIM_AUTH_TOKEN are required")
		os.Exit(2)
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		logger.Error("create database pool", "error", err)
		os.Exit(1)
	}
	defer pool.Close()
	server := &http.Server{Addr: addr, Handler: simulator.NewServer(simulator.NewPostgresEmailStore(pool), token, adminToken, logger).Handler(), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		_ = server.Shutdown(shutdown)
	}()
	logger.Info("starting service simulator", "addr", addr)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.Error("simulator stopped", "error", err)
		os.Exit(1)
	}
}
func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
