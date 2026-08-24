# Testing and experiments

Watchkeeper separates deterministic logic tests, PostgreSQL consistency tests, HTTP contract tests, and full-system proofs. The simulator's independent ledger is the authority for whether an external effect actually happened.

## Fast checks

```bash
make fmt-check
make test
make vet
make build
```

Run the race detector separately:

```bash
make test-race
```

The unit suite covers all allowed and rejected state transitions, canonical JSON fingerprints, immutable receipt comparison, configuration validation and redaction, cursor round trips, authentication, handler validation, recovery classification, and client behavior.

## PostgreSQL integration tests

Point `WK_TEST_DATABASE_URL` at a disposable migrated database:

```bash
WK_TEST_DATABASE_URL='postgres://watchkeeper:watchkeeper_dev@localhost:5432/watchkeeper?sslmode=disable' \
  go test -race ./internal/store
```

These tests exercise clean migrations, constraints and indexes, transactional rollback, concurrent duplicate preparation, fingerprint conflicts, identical and conflicting confirmation, lease exclusion and renewal, result transactions, uncertainty, and manual resolution.

Never point integration tests at a database containing important data.

## System proofs

```bash
make smoke
make recovery-smoke
make smoke-all
```

The normal smoke proof asserts:

1. Intent exists before simulator dispatch.
2. The simulator ledger contains exactly one committed effect.
3. Watchkeeper contains one receipt.
4. The operation is `Confirmed`.
5. Prepared and confirmed events are ordered.

The recovery proof asserts:

1. Intent exists before dispatch.
2. The service commits but confirmation is omitted.
3. The coordinator verifies by stable operation ID.
4. The operation becomes `Reconciled`.
5. The simulator ledger still contains exactly one effect, proving no redispatch.

## Contract and migration checks

The API tests load and validate [`api/openapi.yaml`](../api/openapi.yaml). Readiness checks exact Goose schema compatibility rather than accepting any nonzero migration. Migration replay should be run on a fresh database and on an already-current database to prove both clean installation and idempotent startup behavior.

## Continuous integration

The workflow under [`.github/workflows/ci.yml`](../.github/workflows/ci.yml) runs formatting, unit tests, race tests, vet, build, OpenAPI validation through the test suite, and Docker-backed smoke proofs. A local pass is still valuable because Compose behavior depends on the host engine and retained volumes.

## Planned experiment method

The experiment phase will cross four dimensions: strategy, workload, service capability, and injected failure point. Each configured cell defaults to 100 deterministic seeded repetitions. Raw runs, configuration hashes, source revision, image digests, fault barriers, observations, decisions, timings, and ledger truth must be retained.

Reported outcomes will include duplicate, loss, orphan, inconsistency, completion, uncertainty, recovery latency, retry, database-write, storage-growth, and execution-overhead metrics with 95% confidence intervals. A result is complete only when every configured cell has its expected repetitions and can be reproduced from recorded inputs.

Those comparative experiments are not implemented yet; current smoke tests validate mechanisms, not research conclusions.
