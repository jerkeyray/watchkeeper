package benchmark

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jerkeyray/watchkeeper/internal/simulator"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

type trial struct {
	eventFile                     *os.File
	persistFault                  func(Event) error
	stepDispatches                map[string]int
	mu                            sync.Mutex
	config                        Config
	result                        Result
	token                         string
	events                        []Event
	worker, coordinator           *exec.Cmd
	workerKills, coordinatorKills int
	triggered                     bool
	triggerAt                     time.Time
	serviceUntil                  time.Time
	proxy                         *dbProxy
	active                        sync.WaitGroup
	dispatches, verifications     int
	err                           error
}

func (t *trial) record(e Event) {
	if e.At.IsZero() {
		e.At = time.Now().UTC()
	}
	t.events = append(t.events, e)
	if t.eventFile != nil {
		raw, err := json.Marshal(e)
		if err == nil {
			_, err = t.eventFile.Write(append(raw, '\n'))
		}
		if err == nil {
			err = t.eventFile.Sync()
		}
		if err != nil {
			t.err = err
		}
	}
	if t.persistFault != nil && e.Barrier == t.result.Scenario && (e.Phase == "triggered" || e.Phase == "released") {
		if err := t.persistFault(e); err != nil {
			t.err = err
		}
	}

}
func (t *trial) control(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+t.token {
		http.Error(w, "unauthorized", 401)
		return
	}
	var e Event
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&e); err != nil {
		http.Error(w, "invalid event", 400)
		return
	}
	if err := t.barrier(r.Context(), e); err != nil {
		http.Error(w, err.Error(), 503)
		return
	}
	w.WriteHeader(200)
}
func (t *trial) barrier(ctx context.Context, e Event) error {
	t.mu.Lock()
	t.record(e)
	for i := range t.result.Steps {
		s := &t.result.Steps[i]
		if s.Key == e.Step {
			if e.Barrier == "before_intent" || e.Barrier == "before_dispatch" || e.Barrier == "accepted_before_commit" {
				s.Attempted = true
			}
			if (e.Barrier == "step_acknowledged" || e.Barrier == "after_persistence") && (t.result.Strategy == "watchkeeper" || t.result.Strategy == "checkpoint_recovery") {
				s.Acknowledged = true
			}
			if e.Barrier == "step_uncertain" {
				s.Uncertain = true
			}
		}
	}
	scenario := t.result.Scenario
	// Coordinator failure requires a pending operation first. Kill the worker after commit,
	// then kill the coordinator at the requested result boundary.
	if strings.HasPrefix(scenario, "coordinator_") && e.Step == "step-2" && e.Barrier == "after_commit" && t.workerKills == 0 {
		if t.worker != nil && t.worker.Process != nil {
			if err := t.worker.Process.Kill(); err != nil {
				t.err = err
			}
			t.workerKills++
			t.record(Event{Phase: "triggered", Barrier: "setup_lost_confirmation", Step: e.Step, Attempt: e.Attempt})
		}
	}
	target := scenario
	switch scenario {
	case "lost_response":
		target = "after_commit"
	case "delayed_commit":
		target = "accepted_before_commit"
	case "service_unavailable":
		target = "before_dispatch"
	case "database_unavailable":
		target = "after_response"
	}
	if e.Step != "step-2" || e.Barrier != target || t.triggered {
		t.mu.Unlock()
		return nil
	}
	t.triggered = true
	t.triggerAt = time.Now()
	t.result.FaultTriggered = true
	t.record(Event{Phase: "triggered", Barrier: scenario, Step: e.Step, OperationID: e.OperationID, Attempt: e.Attempt})
	switch scenario {
	case "lost_response":
		t.record(Event{Phase: "released", Barrier: scenario, Step: e.Step})
		t.mu.Unlock()
		return errors.New("injected lost response")
	case "service_unavailable":
		t.serviceUntil = time.Now().Add(time.Duration(t.config.OutageMS) * time.Millisecond)
		t.mu.Unlock()
		t.releaseLater(scenario, e.Step, time.Duration(t.config.OutageMS)*time.Millisecond)
		return nil
	case "database_unavailable":
		t.proxy.interrupt(time.Duration(t.config.OutageMS) * time.Millisecond)
		t.mu.Unlock()
		t.releaseLater(scenario, e.Step, time.Duration(t.config.OutageMS)*time.Millisecond)
		return nil
	default:
		cmd := t.worker
		if strings.HasPrefix(scenario, "coordinator_") {
			cmd = t.coordinator
		}
		if cmd == nil || cmd.Process == nil {
			t.err = fmt.Errorf("barrier reached without child process")
		} else if err := cmd.Process.Kill(); err != nil {
			t.err = err
		} else if strings.HasPrefix(scenario, "coordinator_") {
			t.coordinatorKills++
		} else {
			t.workerKills++
		}
	}
	delay := time.Duration(0)
	if scenario == "delayed_commit" {
		delay = time.Duration(t.config.DelayMS) * time.Millisecond
	}
	t.mu.Unlock()
	if delay > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
	}
	t.mu.Lock()
	t.record(Event{Phase: "released", Barrier: scenario, Step: e.Step, Attempt: e.Attempt})
	t.mu.Unlock()
	return nil
}
func (t *trial) releaseLater(scenario, step string, d time.Duration) {
	t.active.Add(1)
	go func() {
		defer t.active.Done()
		time.Sleep(d)
		t.mu.Lock()
		t.record(Event{Phase: "released", Barrier: scenario, Step: step})
		t.mu.Unlock()
	}()
}
func (t *trial) simHook(ctx context.Context, name, id string, request simulator.EmailRequest) error {
	key := request.Template
	t.mu.Lock()
	attempt := t.stepDispatches[key]
	t.mu.Unlock()
	return t.barrier(ctx, Event{Phase: "reached", Barrier: name, Step: key, OperationID: id, Attempt: attempt})
}
func (t *trial) simulatorHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.active.Add(1)
		defer t.active.Done()
		t.mu.Lock()
		if r.Method == "POST" && r.URL.Path == "/email/messages" {
			t.dispatches++
		}
		if r.Method == "GET" {
			t.verifications++
		}
		unavailable := time.Now().Before(t.serviceUntil)
		t.mu.Unlock()
		if unavailable {
			http.Error(w, "injected service outage", 503)
			return
		}
		if r.Method == "POST" && r.URL.Path == "/email/messages" {
			raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
			if err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			r.Body.Close()
			r.Body = io.NopCloser(bytes.NewReader(raw))
			var payload simulator.EmailRequest
			_ = json.Unmarshal(raw, &payload)
			t.mu.Lock()
			t.stepDispatches[payload.Template]++
			t.mu.Unlock()
			t.mu.Lock()
			duplicate := t.result.Scenario == "duplicate_delivery" && payload.Template == "step-2" && !t.triggered
			if duplicate {
				t.triggered = true
				t.result.FaultTriggered = true
				t.triggerAt = time.Now()
				t.dispatches++
				t.stepDispatches[payload.Template]++
				t.record(Event{Phase: "triggered", Barrier: "duplicate_delivery", Step: "step-2", Attempt: 1})
			}
			t.mu.Unlock()
			if duplicate {
				copy := r.Clone(r.Context())
				copy.Body = io.NopCloser(bytes.NewReader(raw))
				rec := httptest.NewRecorder()
				next.ServeHTTP(rec, copy)
				t.mu.Lock()
				t.record(Event{Phase: "released", Barrier: "duplicate_delivery", Step: "step-2", Attempt: 1})
				t.mu.Unlock()
			}
		}
		next.ServeHTTP(w, r)
	})
}
