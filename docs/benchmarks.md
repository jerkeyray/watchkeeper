# Email benchmark suite

The suite runs three sequential email steps against the real Watchkeeper HTTP handlers and an independently committed PostgreSQL email ledger. Workers and recovery coordinators are separate OS processes. Crash barriers kill those processes with SIGKILL; this is a process-crash experiment, not a K3s pod experiment.

## Run and retain evidence

```sh
make bench-smoke
make bench-email
```

The first command runs a small deterministic validation matrix. The second explicitly starts 100 repetitions of each supported cell in `benchmarks/email.json`; it is a long research run, not an ordinary unit test. Each invocation gets its own Compose project and volume. The ordinary development Compose stack is unaffected. PostgreSQL is retained after completion.

For a stable output name and resumable suite:

```sh
./scripts/benchmark.sh run benchmarks/email.json research-email
./scripts/benchmark.sh resume benchmarks/email.json research-email
```

Resume must use the same configuration, source, and retained database project. Completed and unsupported trials are preserved. Interrupted trials are marked invalid and retained; replacement executions get new IDs. A filesystem lock prevents two controllers from writing the same output. Do not run two suites against the same databases concurrently.

To rebuild summaries without running any experiments:

```sh
go run ./cmd/benchmark summarize -output results/research-email
```

Native execution is also supported with `WK_DATABASE_URL`, `SIM_DATABASE_URL`, and `BASELINE_DATABASE_URL` pointing at dedicated databases and the repository as the working directory. The runner applies Goose migrations to its databases. Never point it at development or production data. Baseline checkpoints use their own database and credentials.

## Strategies and capabilities

- **Blind retry:** restart the entire workflow; do not use per-step checkpoints or idempotency keys.
- **Idempotency-key retry:** restart the workflow with stable per-step keys. Only the idempotency profile supports this strategy.
- **Checkpoint recovery:** persist each successful step receipt and resume the first unconfirmed step; never use status lookup or keyed dispatch.
- **Watchkeeper:** prepare intent, acquire single-send authorization, dispatch, confirm, and verify ambiguous execution before continuing.

All strategies send the same three templates to the same synthetic recipient. Logical message keys are namespaced by execution to prevent cross-trial evidence matches. Step two is the fault target, making duplicate replay of step one observable.

`receipt_status` exposes operation-status lookup. `indirect_unique` exposes matching recipient/template/logical-key fields. A single matching positive effect can prove completion; empty observations cannot prove that an in-flight request will never commit. `idempotency_only` exposes a durable key/payload-binding contract and deduplicates only explicitly keyed sends. `none` exposes no recovery evidence. Ground-truth snapshots are read by the controller, never by workers or verifiers.

## Faults and execution method

The matrix includes a no-failure control; crashes before intent, before dispatch, after request acceptance, after external commit, after response, and after local persistence; lost responses; duplicate delivery; delayed commits; service outages; database connection interruptions; and coordinator crashes before/after result persistence. Coordinator scenarios first create a lost-confirmation operation and then kill the coordinator at the selected barrier. Database outages target local persistence and are unsupported for baselines that do not persist progress.

The controller waits for an acknowledged barrier, records the trigger, and kills the relevant child. Accepted simulator work uses a server-owned context, so it may commit after the caller dies. Database faults terminate real pooled TCP connections and refuse new ones temporarily. Service faults return unavailable responses. These are explicitly scoped fault models, not whole-host failures.

Defaults are one active trial, seeds 1–100, 100 repetitions, a 60-second execution deadline, 2-second outages, 5-second delayed commits, and a further 10-second drain allowance. `prepared_grace_ms` sets the recovery detection delay: the minimum age since preparation or retry start before an unresolved Prepared operation is normally claimable, trading detection latency against coordinators claiming operations that are still legitimately in flight. It is a deployment tuning parameter, not a property of the recovery design, so the suite configures it to 500 ms, records it in the manifest, and reports recovery latency split into detection delay and resolution time. The service default remains 5 s. Strategy order is shuffled from the recorded seed. Draining ends as soon as all accepted requests settle; a drain timeout invalidates the trial rather than treating unfinished ground truth as definitive.

## Outputs and metrics

Each output directory contains a versioned manifest, append-only `raw.jsonl`, `summary.csv`, `summary.md`, and per-execution records. Records retain worker/coordinator logs, barrier events, operation/audit history, observations, decisions, dispatch records, and effect-ledger snapshots. The manifest captures source revision, a hash including untracked source, image identities, schema versions, configuration, runtime, and host information.

Validation configurations may declare `assert_no_duplicate_effects` naming strategies that must finish with no duplicate or excess effects; `benchmarks/smoke.json` asserts this for Watchkeeper and keyed retry, so a real crash after external commit that produced a second effect would fail CI rather than being recorded silently. The assertion also fails when an asserted strategy produced no completed trials. It excludes the `duplicate_delivery` scenario, where the second effect is created inside the service by duplicated delivery of an accepted request; no client-side protocol prevents that, so it is measured rather than asserted. Research configurations deliberately omit it: measured duplication must be recorded, not assumed away.

Score each logical step by effect count and acknowledgment. Checkpoint and Watchkeeper acknowledgments come from durable step progress; blind/idempotency-retry workflows are acknowledged only when the entire child completes and the controller records completion. One logical acknowledgment covers one effect; additional effects are both duplicates and unacknowledged excess effects. Report duplicates and excess effects, missing effects at observation end, unattempted downstream steps, uncertain missing effects, terminal abandonment, unacknowledged effects, false completion, uncertainty, and workflow completion. An observation deadline miss is not automatically a permanent loss. Expected baseline duplicates are valid experimental outcomes; missing fault triggers, unexpected process exits, broken controls, and unreadable evidence are harness failures.

Latency includes child-process startup and shared observation hooks, and excludes API/simulator startup. Recovery latency ends when the workflow completes or stops uncertain, rather than when the first verification request returns. Censored trials are reported separately and omitted from latency quantiles; this can bias those quantiles toward shorter executions. Report the censored fraction alongside them.

Application writes count committed row changes reconstructed from the known transactions and independent service ledger; they do not estimate WAL, fsyncs, or physical writes. Physical relation-size deltas include allocator noise. Compare dedicated aggregate suites and raw values, not individual zero-byte deltas. CSV includes ratios to matched no-failure controls.

Proportion intervals use Wilson 95% confidence intervals. Median and p95 intervals use 2,000 seeded bootstrap resamples; fewer than two samples produces `NA`. One repetition is a harness check, not a research conclusion. Zero observed failures does not establish a zero failure probability.

## Assumptions and limitations

Single-send authorization controls compliant workers. It cannot revoke a request already accepted by an external service. Status absence after authorization therefore preserves uncertainty, unless the adapter verifies an explicit idempotency guarantee. Existing unresolved operations migrated from the old schema are conservatively treated as potentially dispatched. Replay under idempotency keeps the same operation ID and payload; key retention covers the whole experiment.

An ordinary API without deduplication can still create duplicate effects under duplicated delivery. Watchkeeper does not claim to prevent that service behavior. Indirect positive evidence depends on exact matching and the declared uniqueness of the logical message key. The simulator is not a real email provider, the three databases share a PostgreSQL server, and the harness introduces shared observation overhead. Other workloads, service fencing, K3s, and full distributed tracing remain future work.

A dedicated no-failure storage/overhead batch is available as `./scripts/benchmark.sh run benchmarks/storage.json storage-email`. Use its aggregate relation growth and matched raw latency/write counts rather than mixing storage deltas from fault scenarios into baseline overhead.

## Completed evaluation

The retained main matrix has 15,900 valid executions and 6,500 unsupported combinations. The dedicated overhead batch has 1,300 valid executions and 300 unsupported combinations. [Evaluation](evaluation.md) reports the measured outcomes; [the final report](final-project-report.md) adds architecture, contribution, and discussion.

[Source-controlled evidence](evidence/README.md) includes compressed raw records, original manifests, CSV summaries, checksums, and two full representative decision traces. Historical source revision and dirty hash are preserved rather than relabeled as the current documentation revision.

All 159 supported main cells have invariant correctness outcomes across repetitions. Generated Wilson intervals remain in CSV for transparency, but they do not establish a population failure probability for this deterministic matrix. Timing samples vary and should be analyzed separately.

The grace threshold uses `operations.prepared_at`, not the dispatch-authorization timestamp. An explicit reconciliation schedule can make an operation eligible earlier. A new retry attempt resets `prepared_at`.
