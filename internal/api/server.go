package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jerkeyray/watchkeeper/internal/domain"
	"github.com/jerkeyray/watchkeeper/internal/store"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type Server struct {
	store      store.Store
	logger     *slog.Logger
	authToken  string
	adminToken string
	now        func() time.Time
	requests   *prometheus.CounterVec
	duration   *prometheus.HistogramVec
	registry   *prometheus.Registry
}

func New(s store.Store, logger *slog.Logger, authToken, adminToken string) *Server {
	registry := prometheus.NewRegistry()
	requests := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "watchkeeper_http_requests_total", Help: "HTTP requests handled."}, []string{"route", "method", "status_class"})
	duration := prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "watchkeeper_http_request_duration_seconds", Help: "HTTP request duration."}, []string{"route", "method"})
	registry.MustRegister(requests, duration)
	return &Server{store: s, logger: logger, authToken: authToken, adminToken: adminToken, now: time.Now, requests: requests, duration: duration, registry: registry}
}

func (s *Server) Handler() http.Handler {
	router := chi.NewRouter()
	router.Use(s.requestID, s.accessLog)
	router.Get("/health/live", s.live)
	router.Get("/health/ready", s.ready)
	router.With(s.authenticate(s.adminToken)).Handle("/metrics", promhttp.HandlerFor(s.registry, promhttp.HandlerOpts{}))
	router.Route("/v1", func(r chi.Router) {
		r.Group(func(r chi.Router) {
			r.Use(s.authenticate(s.authToken))
			r.Post("/operations", s.prepare)
			r.Get("/operations", s.listOperations)
			r.Get("/operations/{operationID}", s.getOperation)
			r.Get("/operations/{operationID}/events", s.listEvents)
			r.Post("/operations/{operationID}/confirmations", s.confirm)
			r.Post("/operations/{operationID}/attempts", s.beginRetry)
		})
		r.Group(func(r chi.Router) {
			r.Use(s.authenticate(s.adminToken))
			r.Post("/operations/{operationID}/reconciliation-requests", s.requestReconciliation)
			r.Post("/operations/{operationID}/manual-resolutions", s.manualResolution)
			r.Post("/recovery/claims", s.claimRecovery)
			r.Post("/recovery/claims/{operationID}/renew", s.renewClaim)
			r.Post("/recovery/claims/{operationID}/results", s.recoveryResult)
		})
	})
	return router
}

type versionReasonPayload struct {
	ExpectedVersion int64  `json:"expected_version"`
	Reason          string `json:"reason"`
	Actor           string `json:"actor,omitempty"`
}

func (s *Server) beginRetry(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "operationID")
	if _, err := uuid.Parse(id); err != nil {
		s.writeError(w, r, domain.NewError(domain.ErrorValidation, "invalid_operation_id", "operation_id must be a UUID"))
		return
	}
	var payload versionReasonPayload
	if !decodeJSON(w, r, &payload) {
		return
	}
	if payload.ExpectedVersion < 1 || payload.Reason == "" {
		s.writeError(w, r, domain.NewError(domain.ErrorValidation, "invalid_retry", "expected_version and reason are required"))
		return
	}
	operation, err := s.store.BeginRetry(r.Context(), id, payload.ExpectedVersion, payload.Reason)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, operation)
}
func (s *Server) requestReconciliation(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "operationID")
	if _, err := uuid.Parse(id); err != nil {
		s.writeError(w, r, domain.NewError(domain.ErrorValidation, "invalid_operation_id", "operation_id must be a UUID"))
		return
	}
	var payload versionReasonPayload
	if !decodeJSON(w, r, &payload) {
		return
	}
	if payload.ExpectedVersion < 1 || payload.Reason == "" {
		s.writeError(w, r, domain.NewError(domain.ErrorValidation, "invalid_reconciliation_request", "expected_version and reason are required"))
		return
	}
	if payload.Actor == "" {
		payload.Actor = "operator"
	}
	operation, err := s.store.RequestReconciliation(r.Context(), id, payload.ExpectedVersion, payload.Actor, payload.Reason)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, operation)
}

type claimPayload struct {
	WorkerID        string `json:"worker_id"`
	Limit           int    `json:"limit"`
	LeaseDurationMS int    `json:"lease_duration_ms"`
}

func (s *Server) claimRecovery(w http.ResponseWriter, r *http.Request) {
	var payload claimPayload
	if !decodeJSON(w, r, &payload) {
		return
	}
	if payload.WorkerID == "" {
		s.writeError(w, r, domain.NewError(domain.ErrorValidation, "invalid_claim", "worker_id is required"))
		return
	}
	if payload.Limit == 0 {
		payload.Limit = 20
	}
	if payload.Limit < 1 || payload.Limit > 100 {
		s.writeError(w, r, domain.NewError(domain.ErrorValidation, "invalid_claim", "limit must be between 1 and 100"))
		return
	}
	if payload.LeaseDurationMS == 0 {
		payload.LeaseDurationMS = 30000
	}
	if payload.LeaseDurationMS < 5000 || payload.LeaseDurationMS > 300000 {
		s.writeError(w, r, domain.NewError(domain.ErrorValidation, "invalid_claim", "lease_duration_ms must be between 5000 and 300000"))
		return
	}
	claims, err := s.store.ClaimRecovery(r.Context(), payload.WorkerID, payload.Limit, time.Duration(payload.LeaseDurationMS)*time.Millisecond, 5*time.Second)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"claims": claims})
}

type renewPayload struct {
	ClaimToken      string `json:"claim_token"`
	LeaseDurationMS int    `json:"lease_duration_ms"`
}

func (s *Server) renewClaim(w http.ResponseWriter, r *http.Request) {
	var payload renewPayload
	if !decodeJSON(w, r, &payload) {
		return
	}
	if payload.ClaimToken == "" {
		s.writeError(w, r, domain.NewError(domain.ErrorValidation, "invalid_renewal", "claim_token is required"))
		return
	}
	if payload.LeaseDurationMS == 0 {
		payload.LeaseDurationMS = 30000
	}
	if payload.LeaseDurationMS < 5000 || payload.LeaseDurationMS > 300000 {
		s.writeError(w, r, domain.NewError(domain.ErrorValidation, "invalid_renewal", "lease_duration_ms must be between 5000 and 300000"))
		return
	}
	if _, err := uuid.Parse(chi.URLParam(r, "operationID")); err != nil {
		s.writeError(w, r, domain.NewError(domain.ErrorValidation, "invalid_operation_id", "operation_id must be a UUID"))
		return
	}
	expires, err := s.store.RenewClaim(r.Context(), chi.URLParam(r, "operationID"), payload.ClaimToken, time.Duration(payload.LeaseDurationMS)*time.Millisecond)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"lease_expires_at": expires})
}

type recoveryResultPayload struct {
	ClaimToken      string             `json:"claim_token"`
	ExpectedVersion int64              `json:"expected_version"`
	AttemptID       string             `json:"attempt_id"`
	Observation     domain.Observation `json:"observation"`
}

func (s *Server) recoveryResult(w http.ResponseWriter, r *http.Request) {
	var payload recoveryResultPayload
	if !decodeJSON(w, r, &payload) {
		return
	}
	if payload.ClaimToken == "" || payload.ExpectedVersion < 1 || payload.AttemptID == "" {
		s.writeError(w, r, domain.NewError(domain.ErrorValidation, "invalid_recovery_result", "claim_token, expected_version, and attempt_id are required"))
		return
	}
	if _, err := uuid.Parse(chi.URLParam(r, "operationID")); err != nil {
		s.writeError(w, r, domain.NewError(domain.ErrorValidation, "invalid_operation_id", "operation_id must be a UUID"))
		return
	}
	if _, err := uuid.Parse(payload.AttemptID); err != nil {
		s.writeError(w, r, domain.NewError(domain.ErrorValidation, "invalid_attempt_id", "attempt_id must be a UUID"))
		return
	}
	operation, err := s.store.SubmitRecoveryResult(r.Context(), chi.URLParam(r, "operationID"), domain.RecoveryResult{ClaimToken: payload.ClaimToken, ExpectedVersion: payload.ExpectedVersion, AttemptID: payload.AttemptID, Observation: payload.Observation})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, operation)
}

type manualPayload struct {
	ExpectedVersion   int64  `json:"expected_version"`
	Actor             string `json:"actor"`
	Outcome           string `json:"outcome"`
	Reason            string `json:"reason"`
	EvidenceReference string `json:"evidence_reference"`
}

func (s *Server) manualResolution(w http.ResponseWriter, r *http.Request) {
	var payload manualPayload
	if !decodeJSON(w, r, &payload) {
		return
	}
	if payload.ExpectedVersion < 1 || payload.Actor == "" || payload.Outcome == "" || payload.Reason == "" || payload.EvidenceReference == "" {
		s.writeError(w, r, domain.NewError(domain.ErrorValidation, "invalid_manual_resolution", "all manual resolution fields are required"))
		return
	}
	if _, err := uuid.Parse(chi.URLParam(r, "operationID")); err != nil {
		s.writeError(w, r, domain.NewError(domain.ErrorValidation, "invalid_operation_id", "operation_id must be a UUID"))
		return
	}
	operation, err := s.store.ManualResolve(r.Context(), chi.URLParam(r, "operationID"), domain.ManualResolution{ExpectedVersion: payload.ExpectedVersion, Actor: payload.Actor, Outcome: payload.Outcome, Reason: payload.Reason, EvidenceReference: payload.EvidenceReference})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, operation)
}

type preparePayload struct {
	WorkflowID         string          `json:"workflow_id"`
	Strategy           domain.Strategy `json:"strategy"`
	ExperimentRunID    *string         `json:"experiment_run_id,omitempty"`
	LogicalKey         string          `json:"logical_key"`
	TargetService      string          `json:"target_service"`
	Action             string          `json:"action"`
	Request            json.RawMessage `json:"request"`
	RequestFingerprint string          `json:"request_fingerprint,omitempty"`
	ExpectedEffect     json.RawMessage `json:"expected_effect"`
	CapabilityProfile  string          `json:"capability_profile"`
	TraceContext       json.RawMessage `json:"trace_context,omitempty"`
}

func (s *Server) prepare(w http.ResponseWriter, r *http.Request) {
	var payload preparePayload
	if !decodeJSON(w, r, &payload) {
		return
	}
	if err := validatePrepare(payload); err != nil {
		s.writeError(w, r, err)
		return
	}
	fingerprint, err := domain.Fingerprint(payload.TargetService, payload.Action, payload.Request)
	if err != nil {
		s.writeError(w, r, domain.NewError(domain.ErrorValidation, "invalid_request", err.Error()))
		return
	}
	if payload.RequestFingerprint != "" && !strings.EqualFold(payload.RequestFingerprint, fingerprint) {
		s.writeError(w, r, domain.NewError(domain.ErrorValidation, "request_fingerprint_mismatch", "request_fingerprint does not match the canonical request"))
		return
	}
	operation, created, err := s.store.Prepare(r.Context(), domain.PrepareRequest{
		WorkflowID: payload.WorkflowID, Strategy: payload.Strategy, ExperimentRunID: payload.ExperimentRunID,
		LogicalKey: payload.LogicalKey, TargetService: payload.TargetService, Action: payload.Action, Request: payload.Request,
		RequestFingerprint: fingerprint, ExpectedEffect: payload.ExpectedEffect, CapabilityProfile: payload.CapabilityProfile, TraceContext: payload.TraceContext,
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, operation)
}

type confirmPayload struct {
	ExpectedVersion  int64           `json:"expected_version"`
	ServiceReceiptID string          `json:"service_receipt_id"`
	Receipt          json.RawMessage `json:"receipt"`
	ReceivedAt       time.Time       `json:"received_at"`
}

func (s *Server) confirm(w http.ResponseWriter, r *http.Request) {
	operationID := chi.URLParam(r, "operationID")
	if _, err := uuid.Parse(operationID); err != nil {
		s.writeError(w, r, domain.NewError(domain.ErrorValidation, "invalid_operation_id", "operation_id must be a UUID"))
		return
	}
	var payload confirmPayload
	if !decodeJSON(w, r, &payload) {
		return
	}
	if payload.ExpectedVersion < 1 || payload.ServiceReceiptID == "" || len(payload.Receipt) == 0 {
		s.writeError(w, r, domain.NewError(domain.ErrorValidation, "invalid_confirmation", "expected_version, service_receipt_id, and receipt are required"))
		return
	}
	if payload.ReceivedAt.IsZero() {
		payload.ReceivedAt = s.now().UTC()
	}
	operation, err := s.store.Confirm(r.Context(), operationID, domain.ConfirmRequest{ExpectedVersion: payload.ExpectedVersion, ServiceReceiptID: payload.ServiceReceiptID, Receipt: payload.Receipt, ReceivedAt: payload.ReceivedAt})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, operation)
}

func (s *Server) getOperation(w http.ResponseWriter, r *http.Request) {
	operationID := chi.URLParam(r, "operationID")
	if _, err := uuid.Parse(operationID); err != nil {
		s.writeError(w, r, domain.NewError(domain.ErrorValidation, "invalid_operation_id", "operation_id must be a UUID"))
		return
	}
	operation, err := s.store.GetOperation(r.Context(), operationID)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, operation)
}

func (s *Server) listOperations(w http.ResponseWriter, r *http.Request) {
	limit, err := parseLimit(r.URL.Query().Get("limit"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	filter := domain.OperationFilter{WorkflowID: r.URL.Query().Get("workflow_id"), State: domain.OperationState(r.URL.Query().Get("state")), TargetService: r.URL.Query().Get("target_service"), Limit: limit, Cursor: r.URL.Query().Get("cursor")}
	if filter.State != "" && !domain.ValidState(filter.State) {
		s.writeError(w, r, domain.NewError(domain.ErrorValidation, "invalid_state", "state filter is invalid"))
		return
	}
	if raw := r.URL.Query().Get("created_after"); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			s.writeError(w, r, domain.NewError(domain.ErrorValidation, "invalid_created_after", "created_after must use RFC3339"))
			return
		}
		filter.CreatedAfter = &parsed
	}
	page, err := s.store.ListOperations(r.Context(), filter)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (s *Server) listEvents(w http.ResponseWriter, r *http.Request) {
	operationID := chi.URLParam(r, "operationID")
	if _, err := uuid.Parse(operationID); err != nil {
		s.writeError(w, r, domain.NewError(domain.ErrorValidation, "invalid_operation_id", "operation_id must be a UUID"))
		return
	}
	limit, err := parseLimit(r.URL.Query().Get("limit"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	page, err := s.store.ListEvents(r.Context(), operationID, limit, r.URL.Query().Get("cursor"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (s *Server) live(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "live"})
}

func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.store.Ping(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "not_ready", "reason": "database_unavailable"})
		return
	}
	if err := s.store.SchemaReady(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "not_ready", "reason": "schema_not_ready"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func validatePrepare(payload preparePayload) error {
	if payload.WorkflowID == "" || payload.LogicalKey == "" || payload.TargetService == "" || payload.Action == "" {
		return domain.NewError(domain.ErrorValidation, "missing_required_field", "workflow_id, logical_key, target_service, and action are required")
	}
	if !domain.ValidStrategy(payload.Strategy) {
		return domain.NewError(domain.ErrorValidation, "invalid_strategy", "strategy is invalid")
	}
	if len(payload.Request) == 0 || len(payload.ExpectedEffect) == 0 {
		return domain.NewError(domain.ErrorValidation, "missing_required_field", "request and expected_effect are required")
	}
	var requestObject map[string]any
	if err := json.Unmarshal(payload.Request, &requestObject); err != nil || requestObject == nil {
		return domain.NewError(domain.ErrorValidation, "invalid_request", "request must be a JSON object")
	}
	if payload.TargetService != "email" || payload.Action != "send" {
		return domain.NewError(domain.ErrorValidation, "unsupported_operation", "this implementation supports only email/send")
	}
	var effect map[string]any
	if err := json.Unmarshal(payload.ExpectedEffect, &effect); err != nil || effect == nil {
		return domain.NewError(domain.ErrorValidation, "invalid_expected_effect", "expected_effect must be a JSON object")
	}
	switch payload.CapabilityProfile {
	case "receipt_status", "idempotency_only", "indirect_unique", "none":
	default:
		return domain.NewError(domain.ErrorValidation, "invalid_capability_profile", "capability_profile is invalid")
	}
	if payload.ExperimentRunID != nil {
		if _, err := uuid.Parse(*payload.ExperimentRunID); err != nil {
			return domain.NewError(domain.ErrorValidation, "invalid_experiment_run_id", "experiment_run_id must be a UUID")
		}
	}
	if len(payload.TraceContext) > 0 {
		var trace map[string]any
		if err := json.Unmarshal(payload.TraceContext, &trace); err != nil || trace == nil {
			return domain.NewError(domain.ErrorValidation, "invalid_trace_context", "trace_context must be a JSON object")
		}
	}
	return nil
}

func parseLimit(raw string) (int, error) {
	if raw == "" {
		return 50, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 || value > 200 {
		return 0, domain.NewError(domain.ErrorValidation, "invalid_limit", "limit must be between 1 and 200")
	}
	return value, nil
}

type responseRecorder struct {
	http.ResponseWriter
	status int
}

func (w *responseRecorder) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (s *Server) accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		recorder := &responseRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(recorder, r)
		route := chi.RouteContext(r.Context()).RoutePattern()
		if route == "" {
			route = "unmatched"
		}
		class := fmt.Sprintf("%dxx", recorder.status/100)
		s.requests.WithLabelValues(route, r.Method, class).Inc()
		s.duration.WithLabelValues(route, r.Method).Observe(time.Since(start).Seconds())
		s.logger.Info("http request", "request_id", requestID(r.Context()), "method", r.Method, "route", route, "status", recorder.status, "duration", time.Since(start))
	})
}

type contextKey string

const requestIDKey contextKey = "request_id"

func (s *Server) requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if id == "" {
			id = uuid.NewString()
		}
		w.Header().Set("X-Request-ID", id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey, id)))
	})
}
func requestID(ctx context.Context) string {
	value, _ := ctx.Value(requestIDKey).(string)
	return value
}

func (s *Server) authenticate(expected string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			provided := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if provided == "" || len(provided) != len(expected) || subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) != 1 {
				s.writeError(w, r, domain.NewError(domain.ErrorValidation, "unauthorized", "missing or invalid bearer token"))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("invalid_json", err.Error(), requestID(r.Context()), nil))
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("invalid_json", "request body must contain one JSON value", requestID(r.Context()), nil))
		return false
	}
	return true
}

func (s *Server) writeError(w http.ResponseWriter, r *http.Request, err error) {
	status := http.StatusInternalServerError
	code := "internal_error"
	message := "internal server error"
	var details map[string]any
	var domainErr *domain.Error
	if errors.As(err, &domainErr) {
		code = domainErr.Code
		message = domainErr.Message
		details = domainErr.Details
		switch domainErr.Kind {
		case domain.ErrorValidation:
			status = http.StatusUnprocessableEntity
			if code == "unauthorized" {
				status = http.StatusUnauthorized
			}
		case domain.ErrorNotFound:
			status = http.StatusNotFound
		case domain.ErrorConflict:
			status = http.StatusConflict
		case domain.ErrorUnavailable:
			status = http.StatusServiceUnavailable
		}
	} else {
		s.logger.Error("unhandled request error", "error", err, "request_id", requestID(r.Context()))
	}
	writeJSON(w, status, errorEnvelope(code, message, requestID(r.Context()), details))
}

func errorEnvelope(code, message, requestID string, details map[string]any) map[string]any {
	return map[string]any{"error": map[string]any{"code": code, "message": message, "request_id": requestID, "details": details}}
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
