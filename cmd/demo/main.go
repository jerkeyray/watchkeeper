// Command demo runs the same workflow under a real process crash, once with
// ordinary blind retry and once through Watchkeeper (with and without service
// evidence), and reports how many emails each one actually committed at the
// external service. It then repeats the crash many times to show the result is
// not a fluke.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jerkeyray/watchkeeper/pkg/client"
)

const (
	bold = "\033[1m"
	dim  = "\033[2m"
	red  = "\033[31m"
	grn  = "\033[32m"
	ylw  = "\033[33m"
	cyn  = "\033[36m"
	off  = "\033[0m"
)

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

type config struct {
	apiURL, simURL, token, adminToken, simToken, simDSN string
}

func load() config {
	return config{
		apiURL:     env("WK_API_URL", "http://localhost:8080"),
		simURL:     env("SIMULATOR_URL", "http://localhost:8090"),
		token:      env("WK_AUTH_TOKEN", "watchkeeper-dev-token"),
		adminToken: env("WK_ADMIN_TOKEN", "watchkeeper-admin-dev-token"),
		simToken:   env("SIM_AUTH_TOKEN", "simulator-dev-token"),
		simDSN:     env("SIM_DATABASE_URL", "postgres://simulator:simulator_dev@localhost:5432/simulator?sslmode=disable"),
	}
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "worker" {
		if err := worker(); err != nil {
			fmt.Fprintln(os.Stderr, "worker:", err)
			os.Exit(1)
		}
		return
	}
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "\n%sdemo failed: %v%s\n", red, err, off)
		os.Exit(1)
	}
}

const width = 76

func rule(ch string) string { return dim + strings.Repeat(ch, width) + off }

func envInt(key string, fallback int) int {
	if v, err := strconv.Atoi(os.Getenv(key)); err == nil {
		return v
	}
	return fallback
}

// pace slows the narration so an audience can follow each stage as it happens.
// Set DEMO_PACE=0 to run flat out.
func pace() time.Duration { return time.Duration(envInt("DEMO_PACE", 650)) * time.Millisecond }

// event is a barrier reported by a worker, stamped when it arrived so the
// printed offsets are real timings rather than narration delays.
type event struct {
	phase string
	at    time.Time
}

// trial prints one timeline. Offsets are measured from the trial's start. A
// quiet trial prints nothing; it is used for the repeated runs.
type trial struct {
	t0    time.Time
	quiet bool
}

func (t trial) line(at time.Time, mark, text string) {
	if t.quiet {
		return
	}
	stamp := fmt.Sprintf("+%.3fs", at.Sub(t.t0).Seconds())
	fmt.Printf("  %s%8s%s  %s %s\n", dim, stamp, off, mark, text)
	time.Sleep(pace())
}

// wire prints a protocol message under the line it belongs to.
func (t trial) wire(text string) {
	if t.quiet {
		return
	}
	fmt.Printf("  %10s    %s%s%s\n", "", cyn, text, off)
	time.Sleep(pace() / 3)
}

func (t trial) note(text string) {
	if t.quiet {
		return
	}
	fmt.Printf("  %10s    %s%s%s\n", "", dim, text, off)
}

var (
	ok   = grn + "✓" + off
	bad  = red + "✗" + off
	warn = ylw + "!" + off
	info = dim + "·" + off
)

func short(id string) string {
	if len(id) > 8 {
		return id[len(id)-8:]
	}
	return id
}

type outcome struct {
	committed int
	summary   string
	uncertain bool
}

type spec struct {
	strategy, profile, title, blurb string
}

var specs = []spec{
	{"blind_retry", "", "Baseline: blind retry", "no intent log; a restarted worker simply sends again"},
	{"watchkeeper", "receipt_status", "Watchkeeper", "durable intent, single-send authorization, evidence-based recovery"},
	{"watchkeeper", "none", "Watchkeeper, no evidence", "same crash, but the service cannot answer status queries"},
}

func run() error {
	cfg := load()
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, cfg.simDSN)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err = pool.Ping(ctx); err != nil {
		return fmt.Errorf("cannot read the external service ledger (is `make compose-up` running?): %w", err)
	}

	fmt.Printf("\n  %sWatchkeeper%s %s· crash-consistency experiment%s\n", bold, off, dim, off)
	fmt.Println(rule("━"))
	fmt.Printf("  %-9s one email per trial; the worker process is killed with SIGKILL\n", "Setup")
	fmt.Printf("  %-9s after the service commits, before the worker records the reply.\n", "")
	fmt.Printf("  %-9s rows in the email service's own ledger (simulator.email_effects),\n", "Measure")
	fmt.Printf("  %-9s which none of the strategies can write to or see.\n", "")
	fmt.Println()

	results := make([]outcome, len(specs))
	for i, s := range specs {
		if results[i], err = act(ctx, cfg, pool, s, string(rune('A'+i)), false); err != nil {
			return err
		}
	}

	var stats []tally
	if repeat := envInt("DEMO_REPEAT", 20); repeat > 0 {
		if stats, err = repeated(ctx, cfg, pool, repeat); err != nil {
			return err
		}
	}

	fmt.Println(rule("━"))
	fmt.Printf("  %sResults%s %s(1 email intended per trial)%s\n\n", bold, off, dim, off)
	fmt.Printf("  %s%-29s %-11s %s%s\n", dim, "Trial", "Committed", "Outcome", off)
	for i, s := range specs {
		o := results[i]
		mark, colour := ok, grn
		if o.committed != 1 {
			mark, colour = bad, red
		} else if o.uncertain {
			mark = warn
		}
		fmt.Printf("  %-29s %s%-11d%s %s %s\n", string(rune('A'+i))+"  "+s.title, colour+bold, o.committed, off, mark, o.summary)
	}
	if len(stats) > 0 {
		fmt.Printf("\n  %s%-29s %-11s %s%s\n", dim, "Repeated crashes", "Duplicates", "Lost emails", off)
		for _, st := range stats {
			colour := grn
			if st.dups > 0 {
				colour = red
			}
			fmt.Printf("  %-29s %s%-11s%s %d/%d\n", st.label, colour+bold, fmt.Sprintf("%d/%d", st.dups, st.n), off, st.lost, st.n)
		}
	}
	fmt.Println()
	if results[0].committed == results[1].committed {
		fmt.Printf("  %sNote: both produced the same count; the crash may not have landed in the\n  ambiguous window. Re-run, or check that the coordinator is up.%s\n\n", ylw, off)
	}
	return nil
}

// act runs one strategy end to end: start a worker, kill it the instant the
// external effect commits, restart it, and count what reached the service.
func act(ctx context.Context, cfg config, pool *pgxpool.Pool, s spec, letter string, quiet bool) (outcome, error) {
	if !quiet {
		fmt.Println(rule("─"))
		fmt.Printf("  %sTrial %s%s  %s%s%s\n", bold, letter, off, bold, s.title, off)
		fmt.Printf("  %9s%s%s%s\n\n", "", dim, s.blurb, off)
	}

	key := "demo-" + uuid.NewString()
	reached := make(chan event, 32)
	srv, url, err := control(reached)
	if err != nil {
		return outcome{}, err
	}
	defer srv.Close()

	t := trial{t0: time.Now(), quiet: quiet}
	child, err := spawn(cfg, s, key, url, 1)
	if err != nil {
		return outcome{}, err
	}
	done := make(chan error, 1)
	go func() { done <- child.Wait() }()
	t.line(t.t0, info, fmt.Sprintf("worker started %s(pid %d)%s", dim, child.Process.Pid, off))

wait:
	for {
		select {
		case ev := <-reached:
			if t.trace(ev) {
				continue
			}
			switch ev.phase {
			case "prepared":
				t.line(ev.at, ok, "intent persisted before dispatch")
			case "authorized":
				t.line(ev.at, ok, "single-send authorization acquired")
			case "committed":
				t.line(ev.at, ok, "email committed at the service")
				t.wire("← 201 Created")
				break wait
			}
		case err := <-done:
			return outcome{}, fmt.Errorf("worker exited before committing: %v", err)
		case <-time.After(30 * time.Second):
			return outcome{}, fmt.Errorf("timed out waiting for the external commit")
		}
	}

	pid := child.Process.Pid
	killedAt := time.Now()
	_ = child.Process.Signal(syscall.SIGKILL)
	<-done
	// Restart immediately. Recovery runs on its own clock, so a slow narration
	// would otherwise let the coordinator resolve the operation first.
	restart, err := spawn(cfg, s, key, url, 2)
	if err != nil {
		return outcome{}, err
	}
	restartedAt := time.Now()
	finished := make(chan error, 1)
	go func() { finished <- restart.Wait() }()

	t.line(killedAt, red+bold+"✗ kill -9 "+strconv.Itoa(pid)+off, dim+"· worker dies before it can record the reply"+off)
	if !quiet {
		if n, err := count(ctx, pool, key); err == nil {
			t.note(fmt.Sprintf("service ledger already holds %d row; the dead worker never learned it", n))
		}
	}
	t.line(restartedAt, info, fmt.Sprintf("worker restarted %s(pid %d)%s", dim, restart.Process.Pid, off))

	o := outcome{summary: "exactly once"}
	for {
		select {
		case ev := <-reached:
			if t.trace(ev) {
				continue
			}
			switch ev.phase {
			case "committed":
				if s.strategy == "blind_retry" {
					t.line(ev.at, bad, "no record of the first send, so it sends again")
					t.wire("← 201 Created   (a second, unrelated operation ID)")
					o.summary = "duplicate effect at the service"
				} else {
					t.line(ev.at, ok, "email committed at the service")
				}
			case "verifying":
				t.line(ev.at, info, "reconciliation requested; the coordinator takes over")
			case "reconciled":
				t.line(ev.at, ok, "service has the effect: reconciled, no redispatch")
				o.summary = "reconciled from service evidence"
			case "uncertain":
				t.line(ev.at, warn, "no evidence either way: marked Uncertain, dispatch halted")
				t.note("a human resolves it; the system never guesses")
				o.summary, o.uncertain = "halted as Uncertain for review", true
			case "already-reconciled":
				t.line(ev.at, ok, "coordinator had already reconciled it from service evidence")
				o.summary = "reconciled from service evidence"
			case "already-uncertain":
				t.line(ev.at, warn, "coordinator had already marked it Uncertain")
				o.summary, o.uncertain = "halted as Uncertain for review", true
			case "already-confirmed":
				t.line(ev.at, ok, "operation already confirmed, nothing to redispatch")
				o.summary = "confirmed"
			case "retried":
				t.line(ev.at, ok, "proven not dispatched, so a fresh send was safe")
				o.summary = "retried safely"
			default:
				if rest, found := strings.CutPrefix(ev.phase, "refused: "); found {
					t.line(ev.at, ok, "worker asks to send again: server refuses")
					t.wire("← " + refusal(rest))
				}
			}
			continue
		case err := <-finished:
			if err != nil {
				return outcome{}, fmt.Errorf("restarted worker failed: %v", err)
			}
		case <-time.After(45 * time.Second):
			_ = restart.Process.Signal(syscall.SIGKILL)
			return outcome{}, fmt.Errorf("timed out waiting for recovery")
		}
		break
	}
	// Drain anything that arrived alongside the exit, so the audit trail prints.
	for len(reached) > 0 {
		t.trace(<-reached)
	}

	rows, err := effects(ctx, pool, key)
	if err != nil {
		return outcome{}, err
	}
	o.committed = len(rows)
	if !quiet {
		ledger(rows)
	}
	return o, nil
}

// trace prints the protocol-level barriers: requests on the wire, operation
// state changes and the audit trail. It reports whether ev was one of them.
func (t trial) trace(ev event) bool {
	kind, rest, _ := strings.Cut(ev.phase, ": ")
	switch kind {
	case "wire":
		t.wire(rest)
	case "state":
		t.line(ev.at, info, "operation state → "+bold+rest+off)
	case "audit":
		// Wrap the event chain so it stays inside the rule width.
		line := "audit trail: "
		for i, name := range strings.Split(rest, " → ") {
			if i > 0 {
				if len([]rune(line))+len(name) > 58 {
					t.note(line + " →")
					line = "             "
				} else {
					line += " → "
				}
			}
			line += name
		}
		t.note(line)
	default:
		return false
	}
	return true
}

// refusal turns "watchkeeper API code (409): detail" into "HTTP 409 code".
func refusal(msg string) string {
	head, _, _ := strings.Cut(strings.TrimPrefix(msg, "watchkeeper API "), ":")
	if code, status, found := strings.Cut(head, " ("); found {
		return "HTTP " + strings.TrimSuffix(status, ")") + " " + code
	}
	return head
}

type tally struct {
	label         string
	n, dups, lost int
}

// repeated crashes each strategy n times in parallel batches and counts
// duplicates and lost emails from the service ledger.
func repeated(ctx context.Context, cfg config, pool *pgxpool.Pool, n int) ([]tally, error) {
	const parallel = 5
	fmt.Println(rule("─"))
	fmt.Printf("  %sRepeated crashes%s  %s%d SIGKILLs per strategy, %d at a time%s\n\n", bold, off, dim, n, parallel, off)
	out := []tally{{label: "Blind retry", n: n}, {label: "Watchkeeper", n: n}}
	for i, s := range specs[:2] {
		st := &out[i]
		var mu sync.Mutex
		var firstErr error
		finished := 0
		draw := func() {
			fill := finished * 28 / n
			bar := strings.Repeat("█", fill) + dim + strings.Repeat("░", 28-fill) + off
			colour := grn
			if st.dups > 0 {
				colour = red
			}
			fmt.Printf("\r  %-13s %s  %3d/%-3d  %sduplicates %d%s ", st.label, bar, finished, n, colour, st.dups, off)
		}
		draw()
		sem := make(chan struct{}, parallel)
		var wg sync.WaitGroup
		for j := 0; j < n; j++ {
			wg.Add(1)
			sem <- struct{}{}
			go func() {
				defer wg.Done()
				defer func() { <-sem }()
				o, err := act(ctx, cfg, pool, s, "", true)
				mu.Lock()
				defer mu.Unlock()
				if err != nil && firstErr == nil {
					firstErr = err
				}
				finished++
				if o.committed > 1 {
					st.dups++
				}
				if err == nil && o.committed == 0 {
					st.lost++
				}
				draw()
			}()
		}
		wg.Wait()
		fmt.Println()
		if firstErr != nil {
			return nil, firstErr
		}
	}
	fmt.Println()
	return out, nil
}

type effect struct{ id, op, at string }

// ledger prints the committed rows exactly as the service stored them.
func ledger(rows []effect) {
	fmt.Printf("\n  %sService ledger%s %s· read directly from simulator.email_effects%s\n", bold, off, dim, off)
	fmt.Printf("  %s  #   %-10s %-12s %-14s%s\n", dim, "effect", "operation", "committed_at", off)
	for i, r := range rows {
		tag := ""
		if i > 0 {
			tag = red + "duplicate" + off
		}
		fmt.Printf("    %d   %-10s …%-11s %-14s %s\n", i+1, short(r.id), short(r.op), r.at, tag)
		time.Sleep(pace() / 2)
	}
	colour := grn
	if len(rows) != 1 {
		colour = red
	}
	noun := "effects"
	if len(rows) == 1 {
		noun = "effect"
	}
	fmt.Printf("\n  %s%s%d %s committed%s\n\n", colour, bold, len(rows), noun, off)
}

// effects returns the committed rows so the counts above can be checked by eye.
func effects(ctx context.Context, pool *pgxpool.Pool, key string) ([]effect, error) {
	rows, err := pool.Query(ctx, `SELECT id::text, operation_id::text, to_char(committed_at,'HH24:MI:SS.MS') FROM email_effects WHERE logical_message_key=$1 ORDER BY committed_at`, key)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []effect
	for rows.Next() {
		var e effect
		if err := rows.Scan(&e.id, &e.op, &e.at); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func count(ctx context.Context, pool *pgxpool.Pool, key string) (int, error) {
	var n int
	err := pool.QueryRow(ctx, `SELECT count(*) FROM email_effects WHERE logical_message_key=$1`, key).Scan(&n)
	return n, err
}

func control(reached chan<- event) (*http.Server, string, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, "", err
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		at := time.Now()
		body, _ := io.ReadAll(io.LimitReader(r.Body, 512))
		reached <- event{strings.TrimSpace(string(body)), at}
		w.WriteHeader(200)
	})}
	go srv.Serve(ln)
	return srv, "http://" + ln.Addr().String(), nil
}

func spawn(cfg config, s spec, key, control string, attempt int) (*exec.Cmd, error) {
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(self, "worker")
	cmd.Env = append(os.Environ(),
		"DEMO_STRATEGY="+s.strategy, "DEMO_PROFILE="+s.profile, "DEMO_KEY="+key, "DEMO_CONTROL="+control,
		fmt.Sprintf("DEMO_ATTEMPT=%d", attempt),
		"WK_API_URL="+cfg.apiURL, "SIMULATOR_URL="+cfg.simURL,
		"WK_AUTH_TOKEN="+cfg.token, "WK_ADMIN_TOKEN="+cfg.adminToken, "SIM_AUTH_TOKEN="+cfg.simToken)
	cmd.Stderr = os.Stderr
	return cmd, cmd.Start()
}

// ---- the worker process: this is what gets killed and restarted.

func report(url, phase string) {
	if resp, err := http.Post(url, "text/plain", strings.NewReader(phase)); err == nil {
		resp.Body.Close()
	}
}

func version(op client.Operation) string {
	return "operation …" + short(op.ID) + "  state=" + string(op.State) + "  v" + strconv.FormatInt(op.Version, 10)
}

func worker() error {
	cfg := load()
	strategy := os.Getenv("DEMO_STRATEGY")
	profile := os.Getenv("DEMO_PROFILE")
	key := os.Getenv("DEMO_KEY")
	ctrl := os.Getenv("DEMO_CONTROL")
	first := os.Getenv("DEMO_ATTEMPT") == "1"
	ctx := context.Background()
	payload, _ := json.Marshal(map[string]string{
		"recipient": "student@example.invalid", "template": "confirmation", "logical_message_key": key})

	if strategy == "blind_retry" {
		// No durable record of the attempt, so a restart has nothing to consult.
		id := uuid.NewString()
		report(ctrl, "wire: → POST /email/messages   X-Watchkeeper-Operation-ID: …"+short(id))
		if err := send(ctx, cfg, id, payload); err != nil {
			return err
		}
		report(ctrl, "committed")
		if first {
			select {}
		}
		return nil
	}

	public := client.New(cfg.apiURL, cfg.token, nil)
	admin := client.New(cfg.apiURL, cfg.adminToken, nil)
	if first {
		report(ctrl, "wire: → POST /v1/operations   intent + expected effect, profile="+profile)
	} else {
		report(ctrl, "wire: → POST /v1/operations   same workflow and key as before")
	}
	op, err := public.Prepare(ctx, client.PrepareRequest{
		WorkflowID: key, Strategy: client.StrategyWatchkeeper, LogicalKey: "send-confirmation",
		TargetService: "email", Action: "send", Request: payload, ExpectedEffect: payload,
		CapabilityProfile: profile})
	if err != nil {
		return fmt.Errorf("prepare: %w", err)
	}

	if first {
		report(ctrl, "wire: ← "+version(op))
		report(ctrl, "prepared")
		report(ctrl, "wire: → POST /v1/operations/…"+short(op.ID)+"/authorize-dispatch")
		if op, err = public.AuthorizeDispatch(ctx, op.ID, op.Version); err != nil {
			return fmt.Errorf("authorize: %w", err)
		}
		report(ctrl, "wire: ← 200 OK   dispatch granted once")
		report(ctrl, "authorized")
		report(ctrl, "wire: → POST /email/messages   X-Watchkeeper-Operation-ID: …"+short(op.ID))
		if err = send(ctx, cfg, op.ID, payload); err != nil {
			return err
		}
		report(ctrl, "committed")
		select {} // wait to be killed before the receipt can be confirmed
	}

	// Restarted. Recovery may already have resolved this without us: the
	// coordinator runs independently and does not need a worker to be alive.
	report(ctrl, "wire: ← existing "+version(op))
	switch op.State {
	case client.StateReconciled:
		report(ctrl, "already-reconciled")
		audit(ctx, public, ctrl, op.ID)
		return nil
	case client.StateConfirmed:
		report(ctrl, "already-confirmed")
		return nil
	case client.StateUncertain:
		report(ctrl, "already-uncertain")
		audit(ctx, public, ctrl, op.ID)
		return nil
	case client.StateRetryable:
		// Proven not dispatched, so a fresh attempt is safe.
		if op, err = public.AuthorizeDispatch(ctx, op.ID, op.Version); err != nil {
			return fmt.Errorf("authorize: %w", err)
		}
		if err = send(ctx, cfg, op.ID, payload); err != nil {
			return err
		}
		report(ctrl, "retried")
		return nil
	}

	// Deliberately make no client-side decision: ask for permission to send
	// exactly as a fresh attempt would. The server is what refuses.
	if op.State != client.StatePrepared {
		return fmt.Errorf("unexpected state %q on restart", op.State)
	}
	report(ctrl, "wire: → POST /v1/operations/…"+short(op.ID)+"/authorize-dispatch")
	if _, authErr := public.AuthorizeDispatch(ctx, op.ID, op.Version); authErr == nil {
		// Permission granted, so nothing was ever dispatched: sending is safe.
		if err = send(ctx, cfg, op.ID, payload); err != nil {
			return err
		}
		report(ctrl, "committed")
		return nil
	} else {
		report(ctrl, "refused: "+oneLine(authErr))
	}
	if _, err = admin.RequestReconciliation(ctx, op.ID, op.Version, "demo", "worker restarted after dispatch"); err != nil {
		return fmt.Errorf("queue reconciliation: %w", err)
	}
	report(ctrl, "verifying")
	if profile == "none" {
		report(ctrl, "wire: coordinator: profile=none, the service offers no way to ask")
	} else {
		report(ctrl, "wire: coordinator → GET /email/operations/…"+short(op.ID))
	}
	seen := op.State
	deadline := time.Now().Add(40 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
		current, err := public.GetOperation(ctx, op.ID)
		if err != nil {
			continue
		}
		if current.State != seen {
			seen = current.State
			if seen == client.StateReconciled && profile != "none" {
				report(ctrl, "wire: ← 200 committed   effect found for this operation ID")
			}
			report(ctrl, "state: "+string(seen))
		}
		switch current.State {
		case client.StateReconciled:
			report(ctrl, "reconciled")
			audit(ctx, public, ctrl, op.ID)
			return nil
		case client.StateUncertain:
			report(ctrl, "uncertain")
			audit(ctx, public, ctrl, op.ID)
			return nil
		case client.StateRetryable:
			return fmt.Errorf("unexpected retryable outcome in this demo")
		}
	}
	return fmt.Errorf("recovery did not complete in time (is the coordinator running?)")
}

// audit reports the operation's event log as recorded by the API.
func audit(ctx context.Context, c *client.Client, ctrl, id string) {
	page, err := c.ListEvents(ctx, id, 50, "")
	if err != nil {
		return
	}
	var names []string
	for _, e := range page.Items {
		names = append(names, strings.ReplaceAll(e.EventType, "_", " "))
	}
	report(ctrl, "audit: "+strings.Join(names, " → "))
}

func oneLine(err error) string {
	return strings.Join(strings.Fields(err.Error()), " ")
}

func send(ctx context.Context, cfg config, operationID string, payload []byte) error {
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.simURL+"/email/messages", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	r.Header.Set("Authorization", "Bearer "+cfg.simToken)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Watchkeeper-Operation-ID", operationID)
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("simulator returned %d: %s", resp.StatusCode, body)
	}
	return nil
}
