package benchmark

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/jerkeyray/watchkeeper/internal/api"
	"github.com/jerkeyray/watchkeeper/internal/simulator"
	"github.com/jerkeyray/watchkeeper/internal/store"
	"github.com/pressly/goose/v3"
)

var ErrInvalid = errors.New("invalid trial")

type Environment struct{ WatchkeeperDSN, SimulatorDSN, BaselineDSN string }
type runner struct {
	config            Config
	env               Environment
	wk, sim, baseline *pgxpool.Pool
	output            string
	manifest          map[string]any
}

func EnvironmentFromOS() Environment {
	return Environment{os.Getenv("WK_DATABASE_URL"), os.Getenv("SIM_DATABASE_URL"), os.Getenv("BASELINE_DATABASE_URL")}
}
func migrate(ctx context.Context, dsn, dir string) error {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return err
	}
	defer db.Close()
	return goose.UpContext(ctx, db, dir)
}
func Run(ctx context.Context, c Config, env Environment, output string, resume bool) error {
	if env.WatchkeeperDSN == "" || env.SimulatorDSN == "" || env.BaselineDSN == "" {
		return errors.New("WK_DATABASE_URL, SIM_DATABASE_URL, BASELINE_DATABASE_URL are required; use the isolated benchmark Compose stack")
	}
	if err := os.MkdirAll(output, 0755); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(output, ".lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return fmt.Errorf("output locked: %w", err)
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return fmt.Errorf("suite already running: %w", err)
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	manifestPath := filepath.Join(output, "manifest.json")
	old, err := os.ReadFile(manifestPath)
	if err == nil && !resume {
		return errors.New("output already contains a suite; use resume or a fresh output directory")
	}
	if resume {
		var manifest struct {
			ConfigHash     string `json:"config_hash"`
			SourceHash     string `json:"dirty_diff_hash"`
			SourceRevision string `json:"source_revision"`
		}
		if err != nil {
			return err
		}
		if err = json.Unmarshal(old, &manifest); err != nil {
			return err
		}
		if manifest.ConfigHash != c.Hash() {
			return errors.New("resume configuration differs from original")
		}
		if manifest.SourceHash != metadata("BENCH_DIRTY_DIFF_HASH", SourceHash()) || manifest.SourceRevision != metadata("BENCH_SOURCE_REVISION", commandOutput("git", "rev-parse", "HEAD")) {
			return errors.New("resume source differs from original; use the original source or start a new suite")
		}

	}
	if err = goose.SetDialect("postgres"); err != nil {
		return err
	}
	if err = migrate(ctx, env.WatchkeeperDSN, "migrations/watchkeeper"); err != nil {
		return err
	}
	if err = migrate(ctx, env.SimulatorDSN, "migrations/simulator"); err != nil {
		return err
	}
	r := runner{config: c, env: env, output: output}
	r.wk, err = pgxpool.New(ctx, env.WatchkeeperDSN)
	if err != nil {
		return err
	}
	defer r.wk.Close()
	guard, err := r.wk.Acquire(ctx)
	if err != nil {
		return err
	}
	defer guard.Release()
	var acquired bool
	if err = guard.QueryRow(ctx, `SELECT pg_try_advisory_lock(912301007)`).Scan(&acquired); err != nil {
		return err
	}
	if !acquired {
		return errors.New("another benchmark is using these databases")
	}
	defer guard.Exec(context.Background(), `SELECT pg_advisory_unlock(912301007)`)

	r.sim, err = pgxpool.New(ctx, env.SimulatorDSN)
	if err != nil {
		return err
	}
	defer r.sim.Close()
	r.baseline, err = pgxpool.New(ctx, env.BaselineDSN)
	if err != nil {
		return err
	}
	defer r.baseline.Close()
	if _, err = r.baseline.Exec(ctx, `CREATE TABLE IF NOT EXISTS checkpoints(run_id uuid NOT NULL,step text NOT NULL,receipt_id text NOT NULL,PRIMARY KEY(run_id,step))`); err != nil {
		return err
	}
	r.manifest = map[string]any{"version": 1, "config": c, "config_hash": c.Hash(), "source_revision": metadata("BENCH_SOURCE_REVISION", commandOutput("git", "rev-parse", "HEAD")), "dirty_diff_hash": metadata("BENCH_DIRTY_DIFF_HASH", SourceHash()), "image_digests": os.Getenv("BENCH_IMAGE_DIGESTS"), "host": runtime.GOOS + "/" + runtime.GOARCH, "cpus": runtime.NumCPU(), "go": runtime.Version(), "schema_versions": map[string]int{"watchkeeper": 3, "simulator": 2}, "created_at": time.Now().UTC(), "latency_scope": "workflow execution including child startup and shared fault-observation HTTP hooks; excludes service startup", "writes_scope": "committed application row changes, reconstructed from durable state and audit events", "storage_scope": "physical relation growth; includes allocator noise, report in aggregate"}
	if !resume {
		if err = writeJSONFile(manifestPath, r.manifest); err != nil {
			return err
		}
	} else {
		if err = writeJSONFile(filepath.Join(output, "resume-"+uuid.NewString()+".json"), r.manifest); err != nil {
			return err
		}
	}
	completed := map[string]bool{}
	if resume {
		previous, err := ReadResults(output)
		if err != nil {
			return err
		}
		for _, p := range previous {
			if p.Status == "completed" || p.Status == "unsupported" {
				completed[trialKey(p)] = true
			}
		}
		entries, _ := filepath.Glob(filepath.Join(output, "runs", "*", "running.json"))
		for _, path := range entries {
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			var p Result
			if err = json.Unmarshal(raw, &p); err != nil {
				return err
			}
			if _, err = os.Stat(filepath.Join(filepath.Dir(path), "result.json")); err == nil {
				os.Remove(path)
				continue
			}
			p.Status = "invalid"
			p.Error = "controller interrupted before trial completion"
			if err = r.saveResult(ctx, p); err != nil {
				return err
			}
			os.Remove(path)
		}
	}
	failed := false
	for repetition := 1; repetition <= c.Repetitions; repetition++ {
		seed := c.Seed + int64(repetition-1)
		for _, profile := range c.Profiles {
			for _, scenario := range c.Scenarios {
				order := append([]string(nil), c.Strategies...)
				rng := rand.New(rand.NewSource(seed))
				rng.Shuffle(len(order), func(i, j int) { order[i], order[j] = order[j], order[i] })
				for _, strategy := range order {
					if ctx.Err() != nil {
						return ctx.Err()
					}
					p := Result{ExecutionID: uuid.NewString(), Cell: strategy + "/" + profile + "/" + scenario, Strategy: strategy, Profile: profile, Scenario: scenario, Repetition: repetition, Seed: seed, Status: "running"}
					if completed[trialKey(p)] {
						continue
					}
					if !Supported(strategy, profile, scenario) {
						p.Status = "unsupported"
						p.Error = "strategy requires an unavailable capability or fault boundary"
						if err = r.saveResult(ctx, p); err != nil {
							return err
						}
						continue
					}
					fmt.Fprintf(os.Stderr, "benchmark %s repetition=%d seed=%d\n", p.Cell, repetition, seed)
					p, err = r.runTrial(ctx, p)
					if err != nil {
						p.Status = "failed"
						if errors.Is(err, ErrInvalid) {
							p.Status = "invalid"
						}
						p.Error = err.Error()
						failed = true
					}
					if err = r.saveResult(ctx, p); err != nil {
						return err
					}
					os.Remove(filepath.Join(output, "runs", p.ExecutionID, "running.json"))
				}
			}
		}
	}
	if err = Summarize(output); err != nil {
		return err
	}
	if failed {
		return errors.New("benchmark contains harness failures; inspect results")
	}
	final, err := ReadResults(output)
	if err != nil {
		return err
	}
	return CheckAssertions(c, final)
}
func trialKey(r Result) string { return fmt.Sprintf("%s/%d/%d", r.Cell, r.Repetition, r.Seed) }
func commandOutput(name string, args ...string) string {
	b, err := exec.Command(name, args...).Output()
	if err != nil {
		return "unavailable"
	}
	return strings.TrimSpace(string(b))
}
func SourceHash() string {
	b, err := exec.Command("git", "diff", "HEAD", "--binary").Output()
	if err != nil {
		return "unavailable"
	}
	paths, _ := exec.Command("git", "ls-files", "--others", "--exclude-standard").Output()
	for _, p := range strings.Split(strings.TrimSpace(string(paths)), "\n") {
		if p != "" {
			raw, _ := os.ReadFile(p)
			b = append(b, []byte(p)...)
			b = append(b, raw...)
		}
	}
	return fmt.Sprintf("%x", sha256.Sum256(b))
}
func writeJSONFile(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err = os.WriteFile(tmp, raw, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
func (r *runner) saveResult(ctx context.Context, p Result) error {
	if err := writeJSONFile(filepath.Join(r.output, "runs", p.ExecutionID, "result.json"), p); err != nil {
		return err
	}
	raw, _ := json.Marshal(p)
	f, err := os.OpenFile(filepath.Join(r.output, "raw.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return err
	}
	_, err = f.Write(append(raw, '\n'))
	if err == nil {
		err = f.Sync()
	}
	f.Close()
	if err != nil {
		return err
	}
	if p.Status != "unsupported" {
		_, err = r.wk.Exec(ctx, `UPDATE experiment_runs SET status=$2,finished_at=now(),outcome=$3 WHERE id=$1`, p.ExecutionID, p.Status, raw)
	}
	return err
}
func (r *runner) runTrial(parent context.Context, p Result) (Result, error) {
	ctx, cancel := context.WithTimeout(parent, time.Duration(r.config.DeadlineMS+r.config.DrainMS+15000)*time.Millisecond)
	defer cancel()
	p.StartedAt = time.Now().UTC()
	for i := 1; i <= 3; i++ {
		p.Steps = append(p.Steps, Step{Key: fmt.Sprintf("step-%d", i)})
	}
	if err := writeJSONFile(filepath.Join(r.output, "runs", p.ExecutionID, "running.json"), p); err != nil {
		return p, err
	}
	_, err := r.wk.Exec(ctx, `INSERT INTO experiment_runs(id,matrix_cell_id,strategy,workload,capability_profile,failure_scenario,repetition,seed,config_hash,source_revision,image_digests,schema_version,status,started_at) VALUES($1,$2,$3,'email_three_step',$4,$5,$6,$7,$8,$9,'{}','wk3/sim2','running',now())`, p.ExecutionID, p.Cell, p.Strategy, p.Profile, p.Scenario, p.Repetition, p.Seed, r.config.Hash(), r.manifest["source_revision"])
	if err != nil {
		return p, err
	}
	if p.Scenario != "none" {
		_, err = r.wk.Exec(ctx, `INSERT INTO injected_faults(id,experiment_run_id,fault_type,barrier_name,target,scheduled_at) VALUES($1,$2,$3,$3,'step-2',now())`, uuid.NewString(), p.ExecutionID, p.Scenario)
		if err != nil {
			return p, err
		}
	}
	proxyDSN := r.env.WatchkeeperDSN
	if p.Strategy == "checkpoint_recovery" {
		proxyDSN = r.env.BaselineDSN
	}
	proxy, proxied, err := newDBProxy(proxyDSN)
	if err != nil {
		return p, err
	}
	defer proxy.close()
	apiDSN := r.env.WatchkeeperDSN
	if p.Strategy == "watchkeeper" {
		apiDSN = proxied
	}
	pool, err := pgxpool.New(ctx, apiDSN)
	if err != nil {
		return p, err
	}
	defer pool.Close()
	t := &trial{stepDispatches: map[string]int{}, config: r.config, result: p, token: uuid.NewString(), proxy: proxy}
	t.eventFile, err = os.OpenFile(filepath.Join(r.output, "runs", p.ExecutionID, "events.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return p, err
	}
	defer t.eventFile.Close()
	t.persistFault = func(e Event) error {
		journalCtx, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		if e.Phase == "triggered" {
			_, err := r.wk.Exec(journalCtx, `UPDATE injected_faults SET triggered_at=$2 WHERE experiment_run_id=$1`, p.ExecutionID, e.At)
			return err
		}
		_, err := r.wk.Exec(journalCtx, `UPDATE injected_faults SET released_at=$2 WHERE experiment_run_id=$1`, p.ExecutionID, e.At)
		return err
	}

	if p.Scenario != "none" {
		t.record(Event{Phase: "scheduled", Barrier: p.Scenario, Step: "step-2"})
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	admin := uuid.NewString()
	apiHandler := api.New(scopedStore{Postgres: store.NewPostgres(pool), workflowID: p.ExecutionID}, log, t.token, admin)
	apiHandler.SetPreparedGrace(time.Duration(r.config.PreparedGraceMS) * time.Millisecond)
	apiServer := httptest.NewServer(apiHandler.Handler())
	defer apiServer.Close()
	sim := simulator.NewServer(simulator.NewPostgresEmailStore(r.sim), t.token, uuid.NewString(), log)
	sim.Profile = p.Profile
	sim.Hook = t.simHook
	simServer := httptest.NewServer(t.simulatorHandler(sim.Handler()))
	defer simServer.Close()
	control := httptest.NewServer(http.HandlerFunc(t.control))
	defer control.Close()
	c := ChildConfig{ExecutionID: p.ExecutionID, Strategy: p.Strategy, Profile: p.Profile, APIURL: apiServer.URL, SimulatorURL: simServer.URL, ControlURL: control.URL, Token: t.token, AdminToken: admin, DeadlineMS: r.config.DeadlineMS}
	if p.Strategy == "checkpoint_recovery" {
		c.BaselineDSN = proxied
	}
	before, err := r.storage(ctx)
	if err != nil {
		return p, err
	}
	started := time.Now()
	workCtx, stop := context.WithTimeout(ctx, time.Duration(r.config.DeadlineMS)*time.Millisecond)
	defer stop()
	coordDone := make(chan error, 1)
	if p.Strategy == "watchkeeper" {
		go func() { coordDone <- r.supervise(workCtx, t, c, "coordinator") }()
	}
	workErr := r.supervise(workCtx, t, c, "worker")
	elapsed := time.Since(started).Seconds() * 1000
	if workErr == nil && (p.Strategy == "blind_retry" || p.Strategy == "idempotency_key_retry") {
		t.mu.Lock()
		for i := range t.result.Steps {
			t.result.Steps[i].Acknowledged = true
		}
		t.mu.Unlock()
	}
	if workCtx.Err() != nil {
		t.mu.Lock()
		t.result.Censored = true
		t.mu.Unlock()
	}
	stop()
	if p.Strategy == "watchkeeper" {
		<-coordDone
	}
	// Deadline truth is captured before draining accepted requests that outlived workers.
	if err = r.readTruth(ctx, t, true); err != nil {
		return p, err
	}
	simServer.CloseClientConnections()
	drained := make(chan struct{})
	go func() { t.active.Wait(); close(drained) }()
	select {
	case <-ctx.Done():
		return p, ctx.Err()
	case <-drained:
	case <-time.After(time.Duration(r.config.DrainMS) * time.Millisecond):
		return p, fmt.Errorf("%w: service work did not drain within configured allowance", ErrInvalid)
	}
	if err = r.readTruth(ctx, t, false); err != nil {
		return p, err
	}
	after, err := r.storage(ctx)
	if err != nil {
		return p, err
	}
	t.mu.Lock()
	p = t.result
	p.DurationMS = elapsed
	p.Dispatches = t.dispatches
	for _, count := range t.stepDispatches {
		p.Retries += max(0, count-1)
	}
	p.Verifications = t.verifications
	p.StorageBytes = after - before
	events := append([]Event(nil), t.events...)
	if !t.triggerAt.IsZero() {
		end := started.Add(time.Duration(elapsed * float64(time.Millisecond)))
		v := float64(end.Sub(t.triggerAt)) / float64(time.Millisecond)
		p.RecoveryMS = &v
		// Detection delay is dominated by the configured prepared grace; the
		// remainder is the verification and resolution work the design controls.
		for _, e := range events {
			if e.Barrier != "recovery_claimed" || e.At.Before(t.triggerAt) {
				continue
			}
			detection := float64(e.At.Sub(t.triggerAt)) / float64(time.Millisecond)
			resolution := float64(end.Sub(e.At)) / float64(time.Millisecond)
			p.DetectionMS, p.ResolutionMS = &detection, &resolution
			break
		}
	}
	faultErr := t.err
	t.mu.Unlock()
	if p.Strategy == "watchkeeper" {
		p.Writes, err = r.watchkeeperWrites(ctx, p.ExecutionID)
		if err != nil {
			return p, err
		}
	} else if p.Strategy == "checkpoint_recovery" {
		if err = r.baseline.QueryRow(ctx, `SELECT count(*) FROM checkpoints WHERE run_id=$1`, p.ExecutionID).Scan(&p.Writes); err != nil {
			return p, err
		}
	}
	// Service ledger writes include accepted dispatch records, effects and durable keys.
	var serviceWrites int64
	if err = r.sim.QueryRow(ctx, `SELECT (SELECT count(*) FROM email_dispatches WHERE experiment_run_id=$1)+(SELECT count(*) FROM email_effects WHERE experiment_run_id=$1)+(SELECT count(*) FROM email_keys k JOIN email_effects e ON e.id=k.receipt_id WHERE e.experiment_run_id=$1)`, p.ExecutionID).Scan(&serviceWrites); err != nil {
		return p, err
	}
	p.Writes += serviceWrites
	p.Status = "completed"
	Score(&p)
	dir := filepath.Join(r.output, "runs", p.ExecutionID)
	if err = writeJSONFile(filepath.Join(dir, "events.json"), events); err != nil {
		return p, err
	}
	if err = r.saveAudit(ctx, p, dir); err != nil {
		return p, err
	}
	if faultErr != nil {
		return p, faultErr
	}
	if p.Scenario != "none" && !p.FaultTriggered {
		return p, fmt.Errorf("%w: scheduled fault did not trigger", ErrInvalid)
	}
	if workErr != nil && !p.Censored {
		return p, workErr
	}
	if p.Scenario == "none" && (!p.Completed || p.Duplicate || p.Missing != 0) {
		return p, errors.New("no-failure invariant violated")
	}
	if p.FalseCompletion > 0 {
		return p, errors.New("false workflow completion")
	}
	return p, nil
}
func (r *runner) supervise(ctx context.Context, t *trial, c ChildConfig, role string) error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	restarts := 0
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		child := c
		child.RestartIndex = restarts
		if role == "worker" {
			child.AdminToken = ""
		}
		raw, _ := json.Marshal(child)
		cmd := exec.CommandContext(ctx, executable, role)
		cmd.Stdin = bytes.NewReader(raw)
		// Prevent workers inheriting controller database credentials from the parent.
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.TempDir()}
		f, err := os.OpenFile(filepath.Join(r.output, "runs", c.ExecutionID, role+".log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			return err
		}
		cmd.Stdout = f
		cmd.Stderr = f
		t.mu.Lock()
		if role == "worker" {
			t.worker = cmd
		} else {
			t.coordinator = cmd
		}
		err = cmd.Start()
		t.mu.Unlock()
		if err != nil {
			f.Close()
			return err
		}
		err = cmd.Wait()
		f.Close()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err == nil {
			return nil
		}
		t.mu.Lock()
		kills := t.workerKills
		if role == "coordinator" {
			kills = t.coordinatorKills
		}
		t.mu.Unlock()
		if restarts >= kills {
			return fmt.Errorf("%s exited unexpectedly: %w", role, err)
		}
		restarts++
	}
}

func metadata(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
