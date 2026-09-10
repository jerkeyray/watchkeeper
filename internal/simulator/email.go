package simulator

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
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
	IdempotencyKey    string  `json:"-"`
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
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return effect, err
	}
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, `INSERT INTO email_dispatches(operation_id,experiment_run_id,logical_message_key,idempotency_key) VALUES($1,$2,$3,$4)`, operationID, request.ExperimentRunID, request.LogicalMessageKey, request.IdempotencyKey)
	if err != nil {
		return effect, err
	}
	raw, _ := json.Marshal(request)
	fingerprint := fmt.Sprintf("%x", sha256.Sum256(raw))
	if request.IdempotencyKey != "" {
		if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, request.IdempotencyKey); err != nil {
			return effect, err
		}
		var previous, previousOperation, receipt string
		err = tx.QueryRow(ctx, `SELECT fingerprint,operation_id::text,receipt_id::text FROM email_keys WHERE key=$1`, request.IdempotencyKey).Scan(&previous, &previousOperation, &receipt)
		if err == nil {
			if previous != fingerprint || previousOperation != operationID {
				return effect, ErrKeyConflict
			}
			err = tx.QueryRow(ctx, `SELECT id::text,operation_id::text,recipient,template,logical_message_key,committed_at FROM email_effects WHERE id=$1`, receipt).Scan(&effect.ReceiptID, &effect.OperationID, &effect.Recipient, &effect.Template, &effect.LogicalMessageKey, &effect.CommittedAt)
			if err != nil {
				return effect, err
			}
			return effect, tx.Commit(ctx)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return effect, err
		}
	}
	err = tx.QueryRow(ctx, `INSERT INTO email_effects(id,operation_id,experiment_run_id,recipient,template,logical_message_key) VALUES($1,$2,$3,$4,$5,$6) RETURNING committed_at`, effect.ReceiptID, operationID, request.ExperimentRunID, request.Recipient, request.Template, request.LogicalMessageKey).Scan(&effect.CommittedAt)
	if err != nil {
		return effect, err
	}
	if request.IdempotencyKey != "" {
		_, err = tx.Exec(ctx, `INSERT INTO email_keys(key,operation_id,fingerprint,receipt_id) VALUES($1,$2,$3,$4)`, request.IdempotencyKey, operationID, fingerprint, effect.ReceiptID)
		if err != nil {
			return effect, err
		}
	}
	return effect, tx.Commit(ctx)
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

var ErrKeyConflict = errors.New("idempotency key reused with different request")

type Server struct {
	Profile    string
	Hook       func(context.Context, string, string, EmailRequest) error
	store      EmailStore
	token      string
	adminToken string
	logger     *slog.Logger
}

func NewServer(store EmailStore, token, adminToken string, logger *slog.Logger) *Server {
	return &Server{Profile: "receipt_status", store: store, token: token, adminToken: adminToken, logger: logger}
}
func (s *Server) Handler() http.Handler {
	r := chi.NewRouter()
	r.Get("/health/live", func(w http.ResponseWriter, _ *http.Request) {
		write(w, http.StatusOK, map[string]string{"status": "live"})
	})
	r.Get("/health/ready", s.ready)
	r.With(s.auth(s.token)).Post("/email/messages", s.send)
	if s.Profile == "receipt_status" {
		r.With(s.auth(s.token)).Get("/email/operations/{operationID}", s.status)
	}
	if s.Profile == "indirect_unique" {
		r.With(s.auth(s.token)).Get("/email/inspect", s.inspect)
	}
	r.With(s.auth(s.token)).Get("/email/capabilities", func(w http.ResponseWriter, r *http.Request) {
		write(w, 200, map[string]any{"profile": s.Profile, "durable_key_payload_binding": s.Profile == "idempotency_only"})
	})
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
	if s.Profile == "idempotency_only" {
		request.IdempotencyKey = r.Header.Get("Idempotency-Key")
	}
	// Server work can outlive the caller. This models accepted external work, including lost acknowledgements.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 30*time.Second)
	defer cancel()
	if s.Hook != nil {
		if err := s.Hook(ctx, "accepted_before_commit", operationID, request); err != nil {
			write(w, 503, map[string]string{"error": err.Error()})
			return
		}
	}
	effect, err := s.store.Commit(ctx, operationID, request)
	if err != nil {
		s.logger.Error("commit email effect", "error", err, "operation_id", operationID)
		write(w, http.StatusServiceUnavailable, map[string]string{"error": "simulator database unavailable"})
		return
	}
	if s.Hook != nil {
		if err := s.Hook(ctx, "after_commit", operationID, request); err != nil {
			write(w, 503, map[string]string{"error": err.Error()})
			return
		}
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
	if strings.HasPrefix(r.URL.Path, "/admin/") {
		counter, ok := s.store.(interface {
			DispatchCount(context.Context, string) (int, error)
		})
		if !ok {
			write(w, 503, map[string]string{"error": "dispatch ledger unavailable"})
			return
		}
		count, err := counter.DispatchCount(r.Context(), operationID)
		if err != nil {
			write(w, 503, map[string]string{"error": "dispatch ledger unavailable"})
			return
		}
		write(w, http.StatusOK, map[string]any{"operation_id": operationID, "committed_count": status.CommittedCount, "dispatch_count": count, "latest": status.Latest, "authoritative": status.Authoritative})
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

func (s *PostgresEmailStore) Inspect(ctx context.Context, recipient, template, key string) (EmailStatus, error) {
	rows, err := s.pool.Query(ctx, `SELECT id::text,recipient,template,logical_message_key,committed_at FROM email_effects WHERE recipient=$1 AND template=$2 AND logical_message_key=$3 ORDER BY committed_at,id`, recipient, template, key)
	if err != nil {
		return EmailStatus{}, err
	}
	defer rows.Close()
	status := EmailStatus{Authoritative: true}
	for rows.Next() {
		var e EmailEffect
		if err = rows.Scan(&e.ReceiptID, &e.Recipient, &e.Template, &e.LogicalMessageKey, &e.CommittedAt); err != nil {
			return status, err
		}
		status.CommittedCount++
		status.Latest = &e
	}
	return status, rows.Err()
}
func (s *Server) inspect(w http.ResponseWriter, r *http.Request) {
	inspector, ok := s.store.(interface {
		Inspect(context.Context, string, string, string) (EmailStatus, error)
	})
	if !ok {
		write(w, 503, map[string]string{"error": "inspection unavailable"})
		return
	}
	q := r.URL.Query()
	if q.Get("recipient") == "" || q.Get("template") == "" || q.Get("logical_message_key") == "" {
		write(w, 422, map[string]string{"error": "all match fields required"})
		return
	}
	result, err := inspector.Inspect(r.Context(), q.Get("recipient"), q.Get("template"), q.Get("logical_message_key"))
	if err != nil {
		write(w, 503, map[string]string{"error": "inspection unavailable"})
		return
	}
	write(w, 200, result)
}

func (s *PostgresEmailStore) DispatchCount(ctx context.Context, id string) (int, error) {
	var count int
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM email_dispatches WHERE operation_id=$1`, id).Scan(&count)
	return count, err
}
