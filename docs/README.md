# Watchkeeper documentation

Watchkeeper is a research prototype for recovering workflow operations whose external effect may have committed before the workflow saved its response. These guides explain both the working vertical slice and the boundaries of its current guarantees.

## Choose a path

If you want to run the project, start with [Getting started](getting-started.md), then use the [API guide](api.md).

If you want to understand the design, read [Mental model](mental-model.md), [Architecture](architecture.md), and [Recovery model](recovery.md) in that order.

If you want to contribute or evaluate it, use [Testing and experiments](testing.md) and the [Roadmap](roadmap.md).

## Guide map

| Guide | Explains |
| --- | --- |
| [Getting started](getting-started.md) | Prerequisites, local stack, smoke proofs, configuration, and troubleshooting. |
| [Mental model](mental-model.md) | The uncertain-completion problem, durable intent, operation identity, evidence, and state transitions. |
| [Architecture](architecture.md) | Processes, storage ownership, repository packages, transactions, and concurrency controls. |
| [Recovery model](recovery.md) | Claims, leases, verification, observations, decisions, retries, and manual resolution. |
| [API guide](api.md) | Authentication, endpoints, idempotency, optimistic concurrency, errors, and pagination. |
| [Testing and experiments](testing.md) | Test layers, Compose proofs, independent ground truth, CI, and future evaluation rules. |
| [Roadmap](roadmap.md) | What is complete, what comes next, and what is required before production use. |

The [OpenAPI document](../api/openapi.yaml) is the machine-readable HTTP contract. The migrations under [`migrations/`](../migrations/) are the authoritative persisted schema. This documentation describes implemented behavior unless a section is explicitly marked as planned.

## Current scope

The runnable system supports the email workload with an authoritative `receipt_status` capability. It proves normal confirmation and recovery after an external commit with a lost acknowledgement. Calendar, inventory, record transitions, other evidence profiles, deterministic fault injection, K3s experiments, and comparative analysis remain planned.

