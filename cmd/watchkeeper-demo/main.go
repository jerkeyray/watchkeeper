// Watchkeeper's local presentation console. Uses the real API and email ledger.
package main

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/jerkeyray/watchkeeper/pkg/client"
)

//go:embed web/*
var assets embed.FS

type ledger struct {
	Effects    int `json:"committed_count"`
	Dispatches int `json:"dispatch_count"`
}
type beat struct {
	At     time.Time `json:"at"`
	Title  string    `json:"title"`
	Detail string    `json:"detail"`
	Kind   string    `json:"kind"`
}
type run struct {
	ID         string            `json:"id"`
	Scenario   string            `json:"scenario"`
	Phase      string            `json:"phase"`
	Started    time.Time         `json:"started"`
	Done       bool              `json:"done"`
	Error      string            `json:"error,omitempty"`
	Operation  *client.Operation `json:"operation,omitempty"`
	BaselineID string            `json:"baseline_id"`
	Protected  ledger            `json:"protected"`
	Baseline   ledger            `json:"baseline"`
	Events     []client.Event    `json:"events"`
	Timeline   []beat            `json:"timeline"`
}
type console struct {
	mu                                 sync.Mutex
	runs                               map[string]*run
	order                              []string
	active                             bool
	public                             *client.Client
	http                               *http.Client
	apiURL, simURL, simToken, simAdmin string
	ctx                                context.Context
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	h := &http.Client{Timeout: 5 * time.Second}
	c := &console{ctx: ctx, runs: map[string]*run{}, http: h, apiURL: env("WK_API_URL", "http://localhost:8080"), simURL: env("SIMULATOR_URL", "http://localhost:8090"), simToken: env("SIM_AUTH_TOKEN", "simulator-dev-token"), simAdmin: env("SIM_ADMIN_TOKEN", "simulator-admin-dev-token")}
	c.public = client.New(c.apiURL, env("WK_AUTH_TOKEN", "watchkeeper-dev-token"), h)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /demo/status", c.status)
	mux.HandleFunc("POST /demo/runs", c.start)
	mux.HandleFunc("GET /demo/runs/{id}", c.get)
	web, _ := fs.Sub(assets, "web")
	mux.Handle("/", http.FileServer(http.FS(web)))
	srv := &http.Server{Addr: env("DEMO_HTTP_ADDR", "127.0.0.1:8070"), Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	log.Printf("Watchkeeper demo: http://%s", srv.Addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
func write(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func (c *console) status(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	check := func(base string) bool {
		req, err := http.NewRequestWithContext(ctx, "GET", base+"/health/ready", nil)
		if err != nil {
			return false
		}
		res, err := c.http.Do(req)
		if err != nil {
			return false
		}
		defer res.Body.Close()
		return res.StatusCode == 200
	}
	api, sim := check(c.apiURL), check(c.simURL)
	c.mu.Lock()
	defer c.mu.Unlock()
	write(w, 200, map[string]any{"api": api, "simulator": sim, "active": c.active})
}
func (c *console) start(w http.ResponseWriter, r *http.Request) {
	// Keep local demo actions same-origin; credentials never enter the browser.
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		if err != nil || u.Host != r.Host {
			write(w, 403, map[string]string{"error": "Use the local demo page to start a run."})
			return
		}
	}
	var input struct {
		Scenario string `json:"scenario"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2048)).Decode(&input); err != nil {
		write(w, 400, map[string]string{"error": "Choose a demo scenario."})
		return
	}
	if input.Scenario != "normal" && input.Scenario != "lost_confirmation" && input.Scenario != "no_evidence" {
		write(w, 400, map[string]string{"error": "Unknown scenario."})
		return
	}
	c.mu.Lock()
	if c.active {
		c.mu.Unlock()
		write(w, 409, map[string]string{"error": "A demo is already running. Wait for it to finish."})
		return
	}
	id := uuid.NewString()
	x := &run{ID: id, Scenario: input.Scenario, Phase: "preparing", Started: time.Now(), BaselineID: uuid.NewString(), Timeline: []beat{}, Events: []client.Event{}}
	c.runs[id] = x
	c.order = append(c.order, id)
	c.active = true
	if len(c.order) > 20 {
		delete(c.runs, c.order[0])
		c.order = c.order[1:]
	}
	c.mu.Unlock()
	write(w, 202, map[string]string{"id": id})
	go c.execute(x)
}
func (c *console) get(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	x, ok := c.runs[r.PathValue("id")]
	if !ok {
		write(w, 404, map[string]string{"error": "Demo run not found. Start a new run."})
		return
	}
	write(w, 200, x)
}
func (c *console) update(x *run, phase, title, detail, kind string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	x.Phase = phase
	x.Timeline = append(x.Timeline, beat{time.Now(), title, detail, kind})
}
func (c *console) capture(ctx context.Context, x *run, id string) error {
	op, err := c.public.GetOperation(ctx, id)
	if err != nil {
		return err
	}
	events, err := c.public.ListEvents(ctx, id, 100, "")
	if err != nil {
		return err
	}
	p, err := c.truth(ctx, id)
	if err != nil {
		return err
	}
	b, err := c.truth(ctx, x.BaselineID)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	x.Operation = &op
	x.Events = events.Items
	x.Protected = p
	x.Baseline = b
	return nil
}
func (c *console) truth(ctx context.Context, id string) (ledger, error) {
	var result ledger
	req, err := http.NewRequestWithContext(ctx, "GET", c.simURL+"/admin/email/operations/"+id, nil)
	if err != nil {
		return result, err
	}
	req.Header.Set("Authorization", "Bearer "+c.simAdmin)
	res, err := c.http.Do(req)
	if err != nil {
		return result, err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return result, fmt.Errorf("email ledger returned %d", res.StatusCode)
	}
	err = json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&result)
	return result, err
}
func (c *console) send(ctx context.Context, id string, payload []byte) (json.RawMessage, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", c.simURL+"/email/messages", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.simToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Watchkeeper-Operation-ID", id)
	res, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != 201 {
		return nil, fmt.Errorf("email send returned %d", res.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	var receipt struct {
		ID          string `json:"receipt_id"`
		OperationID string `json:"operation_id"`
		Status      string `json:"status"`
	}
	if err = json.Unmarshal(raw, &receipt); err != nil {
		return nil, err
	}
	if receipt.ID == "" || receipt.OperationID != id || receipt.Status != "committed" {
		return nil, fmt.Errorf("email service returned an invalid receipt")
	}
	return raw, nil
}
func pause(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}
func (c *console) execute(x *run) {
	ctx, cancel := context.WithTimeout(c.ctx, 40*time.Second)
	defer cancel()
	defer func() { c.mu.Lock(); x.Done = true; c.active = false; c.mu.Unlock() }()
	fail := func(err error) { c.mu.Lock(); x.Error = err.Error(); x.Phase = "error"; c.mu.Unlock() }
	profile := "receipt_status"
	if x.Scenario == "no_evidence" {
		profile = "none"
	}
	payload, _ := json.Marshal(map[string]string{"recipient": "alex@example.invalid", "template": "order-confirmation", "logical_message_key": "demo-" + x.ID})
	baselinePayload, _ := json.Marshal(map[string]string{"recipient": "alex@example.invalid", "template": "order-confirmation", "logical_message_key": "baseline-" + x.ID})
	op, err := c.public.Prepare(ctx, client.PrepareRequest{WorkflowID: "demo-" + x.ID, Strategy: client.StrategyWatchkeeper, LogicalKey: "order-confirmation", TargetService: "email", Action: "send", Request: payload, ExpectedEffect: payload, CapabilityProfile: profile})
	if err != nil {
		fail(err)
		return
	}
	c.update(x, "prepared", "Intent saved before sending", "Watchkeeper stores the request and its stable operation identity in PostgreSQL.", "info")
	if err = c.capture(ctx, x, op.ID); err != nil {
		fail(err)
		return
	}
	if err = pause(ctx, 1200*time.Millisecond); err != nil {
		fail(err)
		return
	}
	op, err = c.public.AuthorizeDispatch(ctx, op.ID, op.Version)
	if err != nil {
		fail(err)
		return
	}
	receipt, err := c.send(ctx, op.ID, payload)
	if err != nil {
		fail(err)
		return
	}
	if _, err = c.send(ctx, x.BaselineID, baselinePayload); err != nil {
		fail(err)
		return
	}
	c.update(x, "sent", "The email service commits both sends", "Each approach has produced one email effect in the independent service ledger.", "info")
	if err = c.capture(ctx, x, op.ID); err != nil {
		fail(err)
		return
	}
	if err = pause(ctx, 1000*time.Millisecond); err != nil {
		fail(err)
		return
	}
	if x.Scenario == "normal" {
		var rec struct {
			ID string    `json:"receipt_id"`
			At time.Time `json:"committed_at"`
		}
		_ = json.Unmarshal(receipt, &rec)
		_, err = c.public.Confirm(ctx, op.ID, client.ConfirmRequest{ExpectedVersion: op.Version, ServiceReceiptID: rec.ID, Receipt: receipt, ReceivedAt: rec.At})
		if err != nil {
			fail(err)
			return
		}
		if err = c.capture(ctx, x, op.ID); err != nil {
			fail(err)
			return
		}
		c.update(x, "complete", "Confirmation stored. Workflow complete.", "With no failure, both approaches produce one email effect.", "success")
		return
	}
	c.update(x, "lost", "Confirmation is deliberately lost", "The demo omits the local confirmation after the service commits. This reproduces the recovery gap; no process is killed.", "warning")
	if err = pause(ctx, 1200*time.Millisecond); err != nil {
		fail(err)
		return
	}
	if _, err = c.send(ctx, x.BaselineID, baselinePayload); err != nil {
		fail(err)
		return
	}
	if err = c.capture(ctx, x, op.ID); err != nil {
		fail(err)
		return
	}
	c.update(x, "verifying", "Blind retry sends again. Watchkeeper waits for evidence.", "The baseline creates a second email effect. The real recovery coordinator resolves the unfinished Watchkeeper operation.", "warning")
	// Leave the operation pending: the running coordinator discovers it normally.
	for {
		if err = c.capture(ctx, x, op.ID); err != nil {
			fail(err)
			return
		}
		c.mu.Lock()
		state := x.Operation.State
		c.mu.Unlock()
		if state == client.StateReconciled {
			c.update(x, "complete", "Recovered without sending again", "The coordinator found authoritative completion evidence and reconciled the original operation.", "success")
			return
		}
		if state == client.StateUncertain {
			c.update(x, "complete", "Evidence unavailable. Automatic sending stopped.", "Watchkeeper preserves uncertainty instead of risking a duplicate. The ledger is visible to this demo, but not used by recovery.", "warning")
			return
		}
		if state != client.StatePrepared {
			fail(fmt.Errorf("unexpected recovery state: %s", state))
			return
		}
		if err = pause(ctx, 250*time.Millisecond); err != nil {
			fail(fmt.Errorf("recovery did not finish: check that the coordinator is running: %w", err))
			return
		}
	}
}
