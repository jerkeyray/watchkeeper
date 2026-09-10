# Recovery model

Recovery starts when a prepared operation remains unresolved beyond its grace period or an operator explicitly requests reconciliation. It verifies before deciding; it does not redispatch merely because local confirmation is missing.

## Normal path and recovery path

```text
normal:   prepare -> authorize one send -> dispatch -> receipt -> confirm -> Confirmed

failure:  prepare -> dispatch -> external commit -> response lost
                                              |
recovery: claim -> status lookup -> observation -> decision -> Reconciled
```

The normal and recovery paths converge on the same operation and audit history. Recovery does not create a replacement operation.

## Claim protocol

1. A coordinator requests a bounded batch with its worker ID and lease duration.
2. PostgreSQL selects eligible rows while skipping rows locked or actively leased elsewhere.
3. Watchkeeper creates a reconciliation attempt and returns the operation, attempt ID, expected version, expiry, and one-time claim token.
4. The coordinator may renew the live claim for long verification.
5. A result is accepted only when operation ID, attempt ID, token, version, and lease all match.
6. If the coordinator dies, the lease expires and another coordinator can perform a fresh verification.

Claim tokens are secrets. Only a SHA-256 hash is persisted; tokens should not be logged or stored by callers after result submission.

## Observation classification

The current email verifier calls the simulator status endpoint with the stable operation ID:

| Status evidence | Observation | Decision |
| --- | --- | --- |
| Authoritative count is exactly one | `completed` | `Reconciled` |
| Count is zero after dispatch authorization | `unknown` | `Uncertain` |
| Dispatch was never authorized | no external evidence required | `Retryable`; stale authorization is fenced by version |
| Service guarantees durable keyed replay | `unknown` with explicit idempotency contract | `Retryable` |
| Count is greater than one | `contradictory` | `Uncertain` |
| Service or network is temporarily unavailable | `transient_error` | Keep recoverable and schedule another verification. |
| Evidence is non-authoritative or cannot distinguish outcomes | `unknown` | `Uncertain` |

The raw response is retained as JSON evidence, and an external receipt reference is stored when available.

## Retry behavior

`Retryable` is permission to attempt dispatch again, not a dispatch itself. The worker calls the attempts endpoint with the current version and reason. Watchkeeper transitions `Retryable -> Prepared`, increments the attempt count, preserves the stable operation ID, and appends an event. The worker must then acquire single-send authorization from the dispatches endpoint. A lost authorization response must enter recovery; calling the endpoint again never grants a second send for that attempt.

Transient verification failures use bounded scheduling rather than changing the operation to `Retryable`. This distinction prevents service unavailability from being misread as proof that the business effect is absent.

## Manual resolution

An `Uncertain` operation blocks automatic retry. An administrator can gather evidence outside the adapter and record one of two outcomes:

- `reconciled`: evidence shows the effect completed.
- `retryable`: evidence shows the effect is absent and another attempt is safe.

The request must include the expected version, actor, reason, and evidence reference. Watchkeeper records a manual decision and event; it does not perform business compensation. Compensation can be represented as a research decision in future work, but each simulator must define its own semantics.

## Failure cases

- **Crash before intent persistence:** no operation is durable and dispatch must not start.
- **Crash after intent, before dispatch:** the durable authorization flag proves no compliant worker could have dispatched; recovery invalidates the old version and authorizes a fresh attempt.
- **Crash after external commit:** status lookup finds completion and reconciles without redispatch.
- **Crash while holding a claim:** the lease expires and another coordinator re-verifies.
- **Database interruption during a result:** the transaction commits entirely or rolls back; the operation remains claimable after lease expiry.
- **Stale or duplicate result:** token and version checks reject it without changing state.
- **Permanently unverifiable effect:** the operation becomes `Uncertain`; safety takes priority over completion.

Email status lookup, exact indirect inspection, explicit idempotency-key replay, and deliberately unverifiable profiles are implemented. See [Benchmarks](benchmarks.md) for the process-crash experiment model.

