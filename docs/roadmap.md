# Roadmap

Watchkeeper has a completed email prototype and retained comparative evaluation. This roadmap separates delivered behavior from additional research and production work.

## Completed

### Foundation and normal execution

- Go project, versioned OpenAPI contract, configuration, bearer authentication, health/readiness, graceful shutdown, structured logging, Prometheus HTTP metrics, and public client.
- Full baseline PostgreSQL schema with operation identity, immutable receipts, recovery records, audit events, experiment metadata, and injected-fault metadata.
- Idempotent prepare, transactional confirm, read/list APIs, cursor pagination, canonical request fingerprints, optimistic versions, and typed conflicts.
- Independently persisted email simulator, normal workflow worker, Docker Compose environment, CI, and normal smoke proof.

### Receipt-status recovery

- Recovery request, lease claim, renewal, result, retry-attempt, and manual-resolution APIs.
- Concurrent claim exclusion, hashed claim tokens, lease expiry, state/version guards, atomic observation/decision transitions, and audit events.
- Email verifier and coordinator classification for completed, contradictory, unknown, and transient outcomes, with retry justified by non-dispatch or a verified keyed replay contract.
- Lost-confirmation smoke proof demonstrating reconciliation without redispatch.

## Completed email evaluation and demonstrations

- Durable single-send authorization, conservative migration of unresolved intents, safe restart/continuation, and explicit idempotency replay.
- Receipt/status, indirect-unique, idempotency-only, and no-evidence email profiles.
- Three-step workloads with independent blind-retry, idempotency-retry, and checkpoint baselines.
- Real subprocess crashes at acknowledged barriers, delayed commits, duplicate delivery, lost responses, service/database interruptions, and coordinator crashes.
- Isolated Compose setup; versioned seeded matrix configuration; retained executions and resume; independent ground-truth scoring; CSV/Markdown reports with confidence intervals.

See [Email benchmarks](benchmarks.md) for the implemented fault model and measurement limits. The retained research matrix has 15,900 valid executions; the separate overhead batch has 1,300. See [Evaluation](evaluation.md) and [published evidence](evidence/README.md). Smoke checks remain harness checks rather than substitutes for the retained research analysis.

Browser and terminal demonstrations, the comprehensive [final report](final-project-report.md), and source-controlled evidence summaries are delivered. The symbolic policy is implemented; learned verification ordering and LLM integration are future work.

## Next research stages

1. Re-run the retained conditions from a clean final source snapshot and independently reproduce the published analysis.
2. Add calendar creation, inventory reservation, and record-transition workloads with independent ground truth.
3. Extend process-crash experiments to K3s pod and host failures.
4. Add distributed traces and physical database/WAL measurements beyond the current committed application-row counts and relation-size deltas.

## Productionization path

The prototype is not ready for multi-tenant or internet-facing use. Production work includes workload identity and fine-grained authorization, mTLS, managed secrets, high-availability PostgreSQL, backup and restore drills, migration roll-forward/rollback policy, retention and partitioning, audit export, rate limiting, SLOs, alerting, capacity tests, privacy controls, adapter certification, and an operator console.

Exactly-once claims must remain scoped to the evidence a service actually exposes. When evidence cannot establish completion or absence, production Watchkeeper must preserve uncertainty just as the prototype does.

