# Architecture

## Components and ownership

```text
workflow worker ---- prepare / confirm ----> Watchkeeper API ----> Watchkeeper DB
       |                                      ^       ^
       | dispatch stable operation ID         |       | claim / result
       v                                      |       |
email simulator <---- status verifier --------+   recovery coordinator
       |
       v
simulator effect DB
```

- **Workflow worker:** prepares intent, dispatches with the returned operation ID, and confirms the service receipt.
- **Watchkeeper API:** validates HTTP input, authenticates callers, and delegates to store interfaces. Handlers do not execute SQL.
- **Recovery coordinator:** claims unresolved operations, invokes a service-specific verifier, and submits observations.
- **Email simulator:** commits an email effect and exposes status by operation ID. It behaves as an external system.
- **Watchkeeper PostgreSQL database:** sole durable source of truth for intents, state, receipts, recovery attempts, decisions, and audit history.
- **Simulator PostgreSQL database:** independent committed-effect ledger used as experimental ground truth.

Watchkeeper never reads simulator ledger tables directly. Recovery must use the external status endpoint, preserving the same transaction boundary that exists with a real service.

## Repository layout

| Path | Responsibility |
| --- | --- |
| `cmd/watchkeeper-api` | API process, configuration, database pool, lifecycle. |
| `cmd/workflow-worker` | Runnable normal email workflow. |
| `cmd/recovery-coordinator` | Polling recovery process. |
| `cmd/service-simulator` | Email simulator HTTP process. |
| `cmd/recovery-smoke` | Lost-confirmation proof and assertions. |
| `cmd/migrate` | Goose migration runner. |
| `internal/domain` | States, transitions, fingerprints, types, and typed errors. |
| `internal/api` | Chi routes, middleware, validation, serialization, and metrics. |
| `internal/store` | PostgreSQL transactions and cursor encoding. |
| `internal/recovery` | Verifier contract and coordinator logic. |
| `internal/simulator` | Independent email effect ledger and API. |
| `pkg/client` | Public handwritten Go client and typed conflict errors. |
| `migrations` | Watchkeeper and simulator schema histories. |
| `api/openapi.yaml` | Versioned HTTP contract. |
| `deploy/compose` | Local multi-process environment and database initialization. |

## Durable model

The baseline migration creates the full research schema so later phases do not need to reshape core operation identity:

- `workflows` groups operations under an external workflow identity and strategy.
- `operations` stores fingerprint, expected effect, capability, state, version, attempt count, scheduling, and lease fields.
- `receipts` stores at most one immutable receipt per operation.
- `reconciliation_attempts` records claims and their result class.
- `observations` stores verification mechanism, outcome, authority, reference, and evidence.
- `operation_events` is the ordered append-only audit stream.
- `recovery_decisions` records the evidence-backed transition at each operation version.
- `experiment_runs` and `injected_faults` reserve durable research metadata for later automation.

## Transaction boundaries

Preparation creates or finds the workflow, inserts the operation, and appends the prepared event atomically. The unique workflow/logical-key constraint arbitrates concurrent identical prepares; the fingerprint distinguishes safe replay from conflict.

Confirmation locks the operation, checks the expected version and legal transition, inserts the receipt, updates state and timestamps, and appends the audit event in one transaction. Any failure rolls back all three changes.

A recovery result validates the live lease token, attempt ID, expected version, observation, and permitted state transition in one transaction. It then persists the observation and decision, completes the attempt, updates the operation, clears the lease, and appends the event atomically.

Database-generated timestamps define transition order. The API does not trust caller clocks for operation state times.

## Concurrency and leases

Recovery claims use row locking with skip-locked selection so concurrent coordinators receive disjoint work. The caller receives a one-time claim token; only its hash is stored. Every write also checks the operation version. A lease can be renewed while live, and expired work can be claimed by another coordinator.

This gives coordinator crash tolerance without assuming process affinity. Verification may be repeated after lease expiry, so adapters must treat lookup as safe and side-effect free.

## Runtime and security boundary

Containers run as non-root processes. Watchkeeper and simulator databases have different users and databases, even on the same PostgreSQL server. Public workflow routes, administrative recovery routes, simulator routes, and coordinator access use separate development bearer tokens. Health endpoints are unauthenticated; metrics are administrative.

This is an isolated research boundary, not internet-facing hardening. Productionization needs workload identity, secret management, transport security, authorization policy, audit export, backup/restore procedures, and multi-tenant isolation.

