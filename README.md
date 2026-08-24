<p align="center">
  <picture>
    <source srcset="docs/brand/logo-dark.svg" media="(prefers-color-scheme: dark)">
    <source srcset="docs/brand/logo-light.svg" media="(prefers-color-scheme: light)">
    <img src="docs/brand/logo-light.svg" alt="Watchkeeper" width="420" />
  </picture>
</p>

<p align="center">
  <strong>Crash-consistent recovery for stateful cloud workflows.</strong>
</p>

<p align="center">
  Durable intent · Stable operation identity · Evidence-based reconciliation · Reproducible failure research
</p>

<p align="center">
  <a href="#quickstart">Quickstart</a> ·
  <a href="docs/README.md">Documentation</a> ·
  <a href="api/openapi.yaml">OpenAPI</a> ·
  <a href="docs/roadmap.md">Roadmap</a>
</p>

---

## The workflow must know what happened outside it.

That is the whole problem Watchkeeper tackles.

A workflow asks an external service to send an email, reserve inventory, create a meeting, or update a record. The service commits the effect. Before the worker can save the receipt, it crashes. Recovery now sees incomplete local work, but the external effect may already exist.

Retry blindly and the effect may happen twice. Skip the retry and it may never happen at all.

Watchkeeper closes that uncertain-completion window with three durable facts:

1. **Intent before dispatch.** The operation is stored before the external call is allowed to begin.
2. **One stable identity.** The same operation ID follows the call through dispatch, confirmation, retry, and recovery.
3. **Evidence before decision.** Recovery asks the external service what happened, then reconciles, authorizes retry, or preserves uncertainty.

Watchkeeper does not claim universal exactly-once execution. When the service cannot provide reliable evidence, Watchkeeper says **uncertain** instead of manufacturing confidence.

## Why this exists

```text
workflow log                       external service
     |                                    |
     | persist intent                     |
     |-----------------------------------> |
     | dispatch operation                 |
     |-----------------------------------> | commit effect
     |                                    |
     X worker crashes                     |
                                          |
recovery sees "not confirmed"       effect may already exist
```

Normal workflow replay cannot distinguish "the call never happened" from "the call committed and its response was lost." Watchkeeper makes that disagreement explicit and gives recovery a fixed, auditable procedure.

## What is implemented

- **Durable preparation:** PostgreSQL stores the workflow, intent, stable operation ID, request fingerprint, expected effect, and audit event in one transaction.
- **Immutable confirmation:** receipt identity and payload are persisted with the `Confirmed` transition atomically; conflicting replays return `409 Conflict`.
- **Lease-based recovery:** coordinators claim pending work with expiring, hashed lease tokens and optimistic operation versions.
- **Service verification:** the email adapter queries authoritative status by operation ID instead of redispatching an ambiguous effect.
- **Evidence-based decisions:** authoritative completion becomes `Reconciled`; authoritative absence becomes `Retryable`; insufficient or contradictory evidence becomes `Uncertain`.
- **Manual resolution:** uncertain operations can be rechecked or resolved with an actor, reason, and evidence reference.
- **Append-only history:** preparation, confirmation, claims, observations, decisions, retries, and manual actions produce ordered audit events.
- **Research simulator:** an email service writes to an independent committed-effect ledger that acts as experimental ground truth.
- **Typed Go client and OpenAPI contract:** workflow and coordinator programs use the same versioned HTTP interface.
- **Reproducible local proofs:** Docker smoke flows verify both normal confirmation and post-crash reconciliation.

## Quickstart

Requirements:

- Go 1.27.0. An older Go installation can download the pinned toolchain with `GOTOOLCHAIN=auto`.
- Docker with the Compose plugin.

Run the complete normal-execution proof:

```bash
make smoke
```

Expected result:

```json
{
  "state": "confirmed",
  "audit_events": 2,
  "ground_truth_count": 1,
  "validated": true
}
```

Now simulate the hard case: the email commits, its confirmation is lost, and the coordinator must recover it without sending again.

```bash
make recovery-smoke
```

Expected result:

```json
{
  "state": "reconciled",
  "audit_events": 4,
  "redispatched": false,
  "validated": true
}
```

Run both proofs together:

```bash
make smoke-all
```

Stop project services without deleting the named PostgreSQL volume:

```bash
make compose-down
```

## Core model

```text
workflow worker
  -> prepare durable intent
  -> dispatch stable operation ID
  -> persist returned receipt
  -> Confirmed

worker crashes after external commit
  -> coordinator claims unresolved intent
  -> adapter verifies external state
  -> Reconciled | Retryable | Uncertain
```

### Operation states

| State | Meaning | Automatic next step |
| --- | --- | --- |
| `Prepared` | Intent is durable; completion is not confirmed. | Dispatch normally or verify after the recovery grace period. |
| `Confirmed` | The normal path stored an immutable receipt. | Workflow continues. |
| `Reconciled` | Recovery proved the earlier effect completed. | Workflow continues without redispatch. |
| `Retryable` | Authoritative evidence proved the effect is absent. | Worker may begin another attempt with the same operation ID. |
| `Uncertain` | Evidence cannot safely distinguish completion from absence. | Stop automatic dispatch and request review or later verification. |

Allowed transitions are deliberately narrow:

```text
Prepared  -> Confirmed | Reconciled | Retryable | Uncertain
Retryable -> Prepared
Uncertain -> Reconciled | Retryable
```

## Normal execution

Prepare the operation before calling the service:

```bash
curl -sS http://localhost:8080/v1/operations \
  -H 'Authorization: Bearer watchkeeper-dev-token' \
  -H 'Content-Type: application/json' \
  -d '{
    "workflow_id": "workflow-42",
    "strategy": "watchkeeper",
    "logical_key": "send-confirmation",
    "target_service": "email",
    "action": "send",
    "request": {
      "recipient": "researcher@example.invalid",
      "template": "confirmation",
      "logical_message_key": "confirmation"
    },
    "expected_effect": {
      "recipient": "researcher@example.invalid",
      "template": "confirmation",
      "logical_message_key": "confirmation"
    },
    "capability_profile": "receipt_status"
  }'
```

The response contains the stable `operation_id`, canonical request fingerprint, state, and optimistic version. The worker sends that ID in `X-Watchkeeper-Operation-ID`, then confirms the returned receipt.

Identical prepare and confirmation requests are idempotent. Reusing the same logical operation with a different fingerprint, changing the receipt identity, or writing with a stale version is rejected.

## Recovery

The coordinator follows one evidence-driven path:

```text
claim pending intent
  -> verify by operation ID
     -> effect exists       -> Reconciled
     -> effect absent       -> Retryable
     -> temporary failure   -> schedule another verification
     -> evidence ambiguous  -> Uncertain
```

A claim contains a one-time lease token, attempt ID, lease expiry, and operation version. Only the live lease holder can submit a result. A crashed coordinator loses its lease; another coordinator can safely re-verify after expiry.

The current email adapter supports authoritative receipt-status lookup. The database schema and state machine already cover future indirect, idempotency, and no-evidence adapters, but those service profiles are not implemented yet.

## Architecture

```text
                         +----------------------+
                         | recovery coordinator |
                         +----------+-----------+
                                    |
                                    | claim / result
                                    v
+-----------------+      +----------+-----------+      +------------------+
| workflow worker | ---> |   Watchkeeper API    | ---> | Watchkeeper DB   |
+--------+--------+      +----------------------+      +------------------+
         |                           |
         | dispatch operation ID     | verify through adapter
         v                           v
+------------------+      +----------------------+
| email simulator  | <--- | email status adapter |
+--------+---------+      +----------------------+
         |
         v
+------------------------+
| independent effect DB  |
+------------------------+
```

The Watchkeeper and simulator databases use separate credentials and schemas even when they run on one PostgreSQL server. Watchkeeper never reads the simulator's ground-truth tables directly; the coordinator must use the same external status interface a real integration would expose.

See [Architecture](docs/architecture.md) for package boundaries, storage ownership, transactions, and process behavior.

## Documentation

- [Documentation index](docs/README.md) - where to start and how the guides fit together.
- [Getting started](docs/getting-started.md) - toolchain, local stack, direct binary execution, and first API call.
- [Mental model](docs/mental-model.md) - intent, identity, receipts, evidence, states, and guarantees.
- [Architecture](docs/architecture.md) - components, data ownership, transaction boundaries, and repository layout.
- [Recovery model](docs/recovery.md) - claims, leases, observations, decisions, retries, and failure behavior.
- [API guide](docs/api.md) - authentication, endpoint groups, idempotency, errors, and examples.
- [Testing and experiments](docs/testing.md) - unit, integration, smoke, ground truth, and reproducibility.
- [Roadmap](docs/roadmap.md) - implemented scope and the remaining research stages.

The machine-readable contract is [api/openapi.yaml](api/openapi.yaml).

## Development

```bash
make bootstrap
make fmt-check
make test
make test-race
make vet
make build
```

PostgreSQL integration tests run when `WK_TEST_DATABASE_URL` points to a dedicated test database:

```bash
WK_TEST_DATABASE_URL='postgres://watchkeeper:watchkeeper_dev@localhost:5432/watchkeeper?sslmode=disable' \
  go test -race ./internal/store
```

The test suite covers state transitions, fingerprint canonicalization, API authentication and validation, receipt conflicts, transaction rollback, concurrent preparation, lease exclusion, lease renewal, authoritative completion and absence, uncertainty, manual resolution, adapter classification, and OpenAPI validation.

## Project status

Watchkeeper is a research prototype. Today it supports normal execution and receipt-status recovery for the email workload. It is not yet a production workflow engine or a general exactly-once layer.

Next stages:

1. Idempotency-only, indirect-verification, and no-evidence capability profiles.
2. Calendar, inventory, and record-transition workloads.
3. Deterministic failure injection and K3s deployment.
4. OpenTelemetry, Prometheus research metrics, and experiment automation.
5. The full comparative evaluation against blind retry, idempotency retry, and checkpoint recovery.

See the detailed [roadmap](docs/roadmap.md).

## Security boundary

The current deployment is designed for an isolated development or research environment. It uses separate bearer tokens for public, administrative, simulator, and coordinator operations; non-root containers; request-size limits; explicit service/action validation; and separate database credentials. It does not yet provide production workload identity, mTLS, multi-tenancy, or internet-facing hardening.

## License

[MIT](LICENSE) © 2026 Aditya Srivastava
