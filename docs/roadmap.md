# Roadmap

Watchkeeper is being built as a research prototype in increments. The ordering below keeps correctness mechanisms executable before adding experiment scale.

## Completed

### Foundation and normal execution

- Go project, versioned OpenAPI contract, configuration, bearer authentication, health/readiness, graceful shutdown, structured logging, Prometheus HTTP metrics, and public client.
- Full baseline PostgreSQL schema with operation identity, immutable receipts, recovery records, audit events, experiment metadata, and injected-fault metadata.
- Idempotent prepare, transactional confirm, read/list APIs, cursor pagination, canonical request fingerprints, optimistic versions, and typed conflicts.
- Independently persisted email simulator, normal workflow worker, Docker Compose environment, CI, and normal smoke proof.

### Receipt-status recovery

- Recovery request, lease claim, renewal, result, retry-attempt, and manual-resolution APIs.
- Concurrent claim exclusion, hashed claim tokens, lease expiry, state/version guards, atomic observation/decision transitions, and audit events.
- Email status verifier and coordinator classification for completed, absent, contradictory, unknown, and transient outcomes.
- Lost-confirmation smoke proof demonstrating reconciliation without redispatch.

## Next stages

### 1. Complete service capability profiles

Implement durable receipt lookup, idempotency-key repeat/deduplication, indirect effect inspection, and a deliberately no-evidence adapter. Define precedence when multiple mechanisms disagree and prove that insufficient evidence ends in `Uncertain`.

### 2. Add the remaining workloads

Build calendar creation, inventory reservation, and externally visible record-transition simulators. Each simulator needs independent committed-effect ground truth, configurable capability profiles, status/inspection endpoints, and workload-specific optional compensation hooks. No generic automatic compensation semantics will be invented.

### 3. Deterministic failure injection

Add seeded barriers before intent persistence, after persistence, during dispatch, after external commit but before confirmation, and during recovery. Model lost acknowledgements, duplicate delivery, delayed commits, service unavailability, and database interruption. Persist scheduled, triggered, and released faults.

### 4. Observability and research metrics

Add OpenTelemetry traces and domain metrics for duplicates, losses, orphans, external-state inconsistency, completion, uncertainty, recovery latency, writes, storage, retries, and overhead. Correlate workflow, operation, attempt, experiment, and fault identifiers without exposing secrets.

### 5. K3s and experiment automation

Create binding K3s manifests for pod-level crash experiments while retaining Compose for development. Build a seeded matrix runner across blind retry, idempotency-key retry, checkpoint recovery, and Watchkeeper reconciliation; all workloads; all capability profiles; and all failure points.

### 6. Evaluation and reproducibility

Run 100 repetitions per configured cell by default, preserve raw run records, calculate rates with 95% confidence intervals, and produce complete comparative tables. Validate from a clean environment using pinned source, images, schema, configuration, and seeds.

## Productionization path

The prototype is not ready for multi-tenant or internet-facing use. Production work includes workload identity and fine-grained authorization, mTLS, managed secrets, high-availability PostgreSQL, backup and restore drills, migration roll-forward/rollback policy, retention and partitioning, audit export, rate limiting, SLOs, alerting, capacity tests, privacy controls, adapter certification, and an operator console.

Exactly-once claims must remain scoped to the evidence a service actually exposes. When evidence cannot establish completion or absence, production Watchkeeper must preserve uncertainty just as the prototype does.

