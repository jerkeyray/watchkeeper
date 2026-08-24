package simulator

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type EmailRequest struct {
	Recipient         string  `json:"recipient"`
	Template          string  `json:"template"`
	LogicalMessageKey string  `json:"logical_message_key"`
	ExperimentRunID   *string `json:"experiment_run_id,omitempty"`
}
type EmailEffect struct {
	ReceiptID         string    `json:"receipt_id"`
	OperationID       string    `json:"operation_id"`
	Recipient         string    `json:"recipient"`
	Template          string    `json:"template"`
	LogicalMessageKey string    `json:"logical_message_key"`
	CommittedAt       time.Time `json:"committed_at"`
}
type EmailStatus struct {
	OperationID    string       `json:"operation_id"`
	CommittedCount int          `json:"committed_count"`
	Latest         *EmailEffect `json:"latest,omitempty"`
	Authoritative  bool         `json:"authoritative"`
}

type EmailStore interface {
	Ping(context.Context) error
	Commit(context.Context, string, EmailRequest) (EmailEffect, error)
	Status(context.Context, string) (EmailStatus, error)
}

type PostgresEmailStore struct{ pool *pgxpool.Pool }

func NewPostgresEmailStore(pool *pgxpool.Pool) *PostgresEmailStore {
	return &PostgresEmailStore{pool: pool}
}
func (s *PostgresEmailStore) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }
func (s *PostgresEmailStore) Commit(ctx context.Context, operationID string, request EmailRequest) (EmailEffect, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return EmailEffect{}, err
	}
	effect := EmailEffect{ReceiptID: id.String(), OperationID: operationID, Recipient: request.Recipient, Template: request.Template, LogicalMessageKey: request.LogicalMessageKey}
	err = s.pool.QueryRow(ctx, `INSERT INTO email_effects(id,operation_id,experiment_run_id,recipient,template,logical_message_key) VALUES($1,$2,$3,$4,$5,$6) RETURNING committed_at`, effect.ReceiptID, operationID, request.ExperimentRunID, request.Recipient, request.Template, request.LogicalMessageKey).Scan(&effect.CommittedAt)
	return effect, err
}
func (s *PostgresEmailStore) Status(ctx context.Context, operationID string) (EmailStatus, error) {
	rows, err := s.pool.Query(ctx, `SELECT id::text,operation_id::text,recipient,template,logical_message_key,committed_at FROM email_effects WHERE operation_id=$1 ORDER BY committed_at,id`, operationID)
	if err != nil {
		return EmailStatus{}, err
	}
	defer rows.Close()
	status := EmailStatus{OperationID: operationID, Authoritative: true}
	for rows.Next() {
		var effect EmailEffect
		if err := rows.Scan(&effect.ReceiptID, &effect.OperationID, &effect.Recipient, &effect.Template, &effect.LogicalMessageKey, &effect.CommittedAt); err != nil {
			return EmailStatus{}, err
		}
		status.CommittedCount++
		status.Latest = &effect
	}
	return status, rows.Err()
}

type Server struct {
	store      EmailStore
	token      string
	adminToken string
	logger     *slog.Logger
}

func NewServer(store EmailStore, token, adminToken string, logger *slog.Logger) *Server {
	return &Server{store: store, token: token, adminToken: adminToken, logger: logger}
}
func (s *Server) Handler() http.Handler {
	r := chi.NewRouter()
	r.Get("/health/live", func(w http.ResponseWriter, _ *http.Request) {
		write(w, http.StatusOK, map[string]string{"status": "live"})
	})
	r.Get("/health/ready", s.ready)
	r.With(s.auth(s.token)).Post("/email/messages", s.send)
	r.With(s.auth(s.token)).Get("/email/operations/{operationID}", s.status)
	r.With(s.auth(s.adminToken)).Get("/admin/email/operations/{operationID}", s.status)
	return r
}
func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.store.Ping(ctx); err != nil {
		write(w, http.StatusServiceUnavailable, map[string]string{"status": "not_ready", "reason": "database_unavailable"})
		return
	}
	write(w, http.StatusOK, map[string]string{"status": "ready"})
}
func (s *Server) send(w http.ResponseWriter, r *http.Request) {
	operationID := r.Header.Get("X-Watchkeeper-Operation-ID")
	if _, err := uuid.Parse(operationID); err != nil {
		write(w, http.StatusUnprocessableEntity, map[string]string{"error": "valid X-Watchkeeper-Operation-ID is required"})
		return
	}
	var request EmailRequest
	if !decode(w, r, &request) {
		return
	}
	if request.Recipient == "" || request.Template == "" || request.LogicalMessageKey == "" {
		write(w, http.StatusUnprocessableEntity, map[string]string{"error": "recipient, template, and logical_message_key are required"})
		return
	}
	effect, err := s.store.Commit(r.Context(), operationID, request)
	if err != nil {
		s.logger.Error("commit email effect", "error", err, "operation_id", operationID)
		write(w, http.StatusServiceUnavailable, map[string]string{"error": "simulator database unavailable"})
		return
	}
	write(w, http.StatusCreated, map[string]any{"receipt_id": effect.ReceiptID, "operation_id": operationID, "status": "committed", "committed_at": effect.CommittedAt})
}
func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	operationID := chi.URLParam(r, "operationID")
	if _, err := uuid.Parse(operationID); err != nil {
		write(w, http.StatusUnprocessableEntity, map[string]string{"error": "operation ID must be a UUID"})
		return
	}
	status, err := s.store.Status(r.Context(), operationID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		write(w, http.StatusServiceUnavailable, map[string]string{"error": "simulator database unavailable"})
		return
	}
	write(w, http.StatusOK, status)
}
func (s *Server) auth(expected string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			provided := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if provided == "" || len(provided) != len(expected) || subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) != 1 {
				write(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
func decode(w http.ResponseWriter, r *http.Request, target any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		write(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		write(w, http.StatusBadRequest, map[string]string{"error": "one JSON value is required"})
		return false
	}
	return true
}
func write(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
