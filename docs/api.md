# API guide

The Watchkeeper HTTP API is versioned under `/v1`. The complete schemas and response definitions live in [`api/openapi.yaml`](../api/openapi.yaml).

## Addresses and authentication

The Compose defaults are:

- Watchkeeper: `http://localhost:8080`
- Email simulator: `http://localhost:8090`

Send `Authorization: Bearer <token>` on protected requests. Operation prepare, read, list, confirm, and retry use the public token. Reconciliation requests, claims, claim renewal/results, manual resolution, and metrics use the administrative token. Liveness and readiness are public.

## Endpoint summary

| Method and path | Role | Purpose |
| --- | --- | --- |
| `GET /health/live` | Public | Process liveness. |
| `GET /health/ready` | Public | Database and schema readiness. |
| `GET /metrics` | Admin | Prometheus HTTP metrics. |
| `POST /v1/operations` | Public token | Prepare durable intent. |
| `GET /v1/operations` | Public token | Filter and cursor-page operations. |
| `GET /v1/operations/{id}` | Public token | Read current operation and receipt. |
| `GET /v1/operations/{id}/events` | Public token | Cursor-page its audit events. |
| `POST /v1/operations/{id}/confirmations` | Public token | Atomically store receipt and confirm. |
| `POST /v1/operations/{id}/attempts` | Public token | Begin an authorized retry. |
| `POST /v1/operations/{id}/reconciliation-requests` | Admin token | Ask for immediate re-verification. |
| `POST /v1/operations/{id}/manual-resolutions` | Admin token | Resolve an uncertain operation. |
| `POST /v1/recovery/claims` | Admin token | Lease pending recovery work. |
| `POST /v1/recovery/claims/{id}/renew` | Admin token | Extend a live lease. |
| `POST /v1/recovery/claims/{id}/results` | Admin token | Atomically record observation and decision. |

## Prepare

```bash
curl -sS http://localhost:8080/v1/operations \
  -H 'Authorization: Bearer watchkeeper-dev-token' \
  -H 'Content-Type: application/json' \
  -d '{
    "workflow_id":"workflow-42",
    "strategy":"watchkeeper",
    "logical_key":"send-confirmation",
    "target_service":"email",
    "action":"send",
    "request":{"recipient":"researcher@example.invalid","template":"confirmation"},
    "expected_effect":{"recipient":"researcher@example.invalid","template":"confirmation"},
    "capability_profile":"receipt_status"
  }'
```

The first request returns `201 Created`. An identical natural-key replay returns the existing operation with `200 OK`. A replay whose canonical request fingerprint differs returns `409 Conflict`. The current vertical slice accepts only `email` / `send` and validates the supported capability profile.

## Confirm

Send the current operation version and the simulator's durable receipt:

```bash
curl -sS http://localhost:8080/v1/operations/OPERATION_ID/confirmations \
  -H 'Authorization: Bearer watchkeeper-dev-token' \
  -H 'Content-Type: application/json' \
  -d '{
    "expected_version":1,
    "service_receipt_id":"SERVICE_RECEIPT_ID",
    "receipt":{"status":"committed"}
  }'
```

Receipt insertion, state/version update, and event append are one transaction. An identical confirmation replay returns the confirmed operation. A different receipt identity or payload conflicts. If the confirmation response is ambiguous, the client must read the operation before deciding whether resubmission is needed; it must not blindly submit a different receipt.

## Recovery requests and claims

An administrator can make a prepared or uncertain operation eligible for verification:

```bash
curl -sS http://localhost:8080/v1/operations/OPERATION_ID/reconciliation-requests \
  -H 'Authorization: Bearer watchkeeper-admin-dev-token' \
  -H 'Content-Type: application/json' \
  -d '{"expected_version":1,"actor":"operator","reason":"confirmation response lost"}'
```

Coordinators claim work with a worker ID, batch limit from 1 to 100, and lease duration from 5,000 to 300,000 milliseconds. Results include the claim token, expected version, attempt ID, and observation. Treat the returned claim token as a one-time secret.

## Optimistic concurrency and idempotency

Mutation requests that can race carry `expected_version`. On success, the version advances. A stale write returns a conflict and includes the current state where the contract permits. Callers should re-read and decide; they should not overwrite or synthesize a new operation.

Prepare is idempotent by workflow/logical key plus canonical fingerprint. Confirmation is idempotent only for the identical receipt. Recovery result replay is constrained by the one-time lease and operation version.

## Lists and cursors

`GET /v1/operations` supports filters documented in OpenAPI, including workflow, state, target service, creation time, limit, and cursor. Event lists use an operation-scoped cursor. Responses contain `items` and an optional opaque `next_cursor`.

Do not parse or construct cursor contents. Pass `next_cursor` unchanged. Invalid or mismatched cursors return a validation error.

## Errors and status behavior

Errors use a stable JSON envelope with a machine-readable code, human-readable message, and request ID. Common statuses are:

- `400 Bad Request` for malformed JSON, invalid fields, or bad cursors.
- `401 Unauthorized` for missing or incorrect bearer tokens.
- `404 Not Found` for unknown resources.
- `409 Conflict` for fingerprint, receipt, state, lease, or version conflicts.
- `413 Request Entity Too Large` when the request body exceeds the configured limit.
- `500 Internal Server Error` for unexpected failures without exposing database or secret details.

The handwritten [`pkg/client`](../pkg/client/) exposes typed operations and conflict errors. It may retry an identical prepare request; confirmation ambiguity must be resolved with a read.

