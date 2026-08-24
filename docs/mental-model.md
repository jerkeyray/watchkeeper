# Mental model

## The uncertain-completion window

A database transaction can make local workflow state atomic. It cannot atomically include an unrelated email, calendar, inventory, or record service. If that service commits and the worker crashes before persisting its response, local state says “unfinished” while external state says “done.”

Neither ordinary choice is universally safe:

- Blind retry can duplicate a committed effect.
- Skipping the call can lose an effect that never committed.

Watchkeeper records the uncertainty and uses service evidence to narrow it. It does not turn an unverifiable service into an exactly-once service.

## The durable facts

### Intent

An intent is written before dispatch. It names the workflow, logical operation, target service and action, expected effect, service capability, and stable operation ID. If intent persistence fails, dispatch must not occur.

### Stable operation identity

The operation ID is generated once and sent to the external service. A retry is another attempt of that operation, not a new logical operation. The natural key `(workflow_id, logical_key)` makes prepare replay idempotent.

### Request fingerprint

Watchkeeper canonicalizes the request JSON and hashes it with SHA-256. Replaying the same natural key and fingerprint returns the existing operation. Reusing the key for different content is a conflict, preventing accidental identity reuse.

### Expected effect

The expected-effect document describes what verification should find. It is stored separately from the original request so an adapter can compare observed external state with intended state without reconstructing intent from transient worker memory.

### Receipt

A receipt is durable evidence returned by the service on the normal path. Its service receipt ID and payload are immutable after confirmation. An identical confirmation replay succeeds; a different receipt for the same operation conflicts.

### Observation and decision

Recovery records what mechanism it used, the observed outcome, whether the evidence is authoritative, an external reference, and raw JSON evidence. A separate decision maps that observation to a state transition. Keeping them separate makes later analysis auditable.

## Lifecycle

```text
Prepared  -> Confirmed
Prepared  -> Reconciled | Retryable | Uncertain
Retryable -> Prepared
Uncertain -> Reconciled | Retryable
```

| State | Interpretation |
| --- | --- |
| `Prepared` | Intent is durable, but Watchkeeper has no durable completion conclusion. |
| `Confirmed` | The normal caller persisted an immutable receipt. |
| `Reconciled` | Recovery obtained authoritative evidence that the effect completed. |
| `Retryable` | Authoritative evidence established that the effect is absent. |
| `Uncertain` | Available evidence is missing, non-authoritative, or contradictory. Automatic dispatch stops. |

Terminal states have no automatic outgoing transition. `Retryable -> Prepared` increments the attempt while preserving operation identity. An operator may move `Uncertain` only to `Reconciled` or `Retryable`, with actor, reason, and evidence reference.

## Evidence capability profiles

The design recognizes five mechanisms:

1. Durable receipt lookup.
2. Operation-ID status lookup.
3. Indirect inspection of external state.
4. Idempotency-key deduplication or repeat.
5. No useful evidence.

The current email adapter implements authoritative status lookup under the `receipt_status` profile. The other profiles are modeled in the schema and domain vocabulary but are not runnable integrations yet.

## Guarantees and boundaries

When authoritative receipt or status evidence is available, Watchkeeper can avoid redispatching a proven committed effect and can authorize retry for a proven absent effect. Every transition is version checked and audit recorded.

When evidence is insufficient, the guarantee is conservative behavior: preserve `Uncertain` rather than issue an unsafe retry. Watchkeeper does not promise exactly-once execution, infer generic compensation semantics, or coordinate a distributed transaction with the external service.

