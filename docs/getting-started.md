# Getting started

This guide takes the project from a clean checkout to two executable proofs: a normal email operation and a lost-confirmation recovery.

## Requirements

- Go 1.27.0. With `GOTOOLCHAIN=auto`, an older Go installation can fetch the pinned toolchain.
- Docker Desktop or another Docker engine with the Compose plugin.
- `curl` for manual API examples.

## Run the proofs

Download Go dependencies and run the normal path:

```bash
make bootstrap
make smoke
```

`make smoke` builds the containers, creates independently credentialed Watchkeeper and simulator databases, applies both Goose migrations, starts the API and simulator, and runs one workflow worker. A successful result reports `confirmed`, exactly one external effect, and ordered prepare/confirm events.

Exercise the uncertain-completion path:

```bash
make recovery-smoke
```

This flow persists an intent, dispatches an email, deliberately omits confirmation, and waits for the coordinator to look up the committed effect. Success reports `reconciled`, one external effect, and no redispatch.

Run both:

```bash
make smoke-all
```

Stop the services while retaining the named PostgreSQL volume:

```bash
make compose-down
```

To remove local database data as well, explicitly run `docker compose -f deploy/compose/compose.yaml down -v`. This is destructive and is not part of the normal Make target.

## Local configuration

Copy `.env.example` if you want to run binaries outside Compose:

```bash
cp .env.example .env
set -a
. ./.env
set +a
```

The file contains development-only credentials. `.env` is ignored by Git. Do not reuse these tokens or database passwords outside an isolated local environment.

The API resolves configuration in flag, environment, then default order. Important values are:

| Setting | Purpose | Default |
| --- | --- | --- |
| `WK_DATABASE_URL` / `-database-url` | Watchkeeper PostgreSQL connection. | Required |
| `WK_AUTH_TOKEN` | Public workflow API bearer token. | Required |
| `WK_ADMIN_TOKEN` | Recovery and administrative bearer token. | Public token if omitted |
| `WK_HTTP_ADDR` / `-http-addr` | API listen address. | `:8080` |
| `WK_LOG_LEVEL` / `-log-level` | `debug`, `info`, `warn`, or `error`. | `info` |
| `WK_AUTH_TOKEN_FILE` / `-auth-token-file` | Read the public token from a file. | Empty |
| `WK_ADMIN_TOKEN_FILE` / `-admin-token-file` | Read the admin token from a file. | Empty |
| `-shutdown-timeout` | Graceful shutdown deadline. | `10s` |

Configuration logging redacts database URLs and tokens.

## Run processes directly

Start only PostgreSQL, apply migrations, and then run the binaries:

```bash
docker compose -f deploy/compose/compose.yaml up -d postgres
make migrate
make migrate-simulator
go run ./cmd/watchkeeper-api
```

In other terminals, run the simulator and coordinator:

```bash
go run ./cmd/service-simulator
go run ./cmd/recovery-coordinator
```

The simulator and coordinator use their `SIM_*` and `WK_API_URL` settings as shown in [`deploy/compose/compose.yaml`](../deploy/compose/compose.yaml).

## Check health

Liveness means the process is running. Readiness additionally checks PostgreSQL connectivity and exact schema compatibility.

```bash
curl -sS http://localhost:8080/health/live
curl -sS http://localhost:8080/health/ready
curl -sS http://localhost:8090/health/ready
```

Metrics require the administrative token:

```bash
curl -sS http://localhost:8080/metrics \
  -H 'Authorization: Bearer watchkeeper-admin-dev-token'
```

## Common problems

- **Docker cannot connect:** start Docker Desktop, then rerun the Make target.
- **Port 5432, 8080, or 8090 is busy:** stop the conflicting process or change the Compose port mapping.
- **Readiness reports an incompatible schema:** run both migration targets against the same URLs used by the processes.
- **A reused volume has old initialization credentials:** remove the Compose volume only if its data is disposable, then recreate the stack.
- **A request returns 401:** public operation routes use `WK_AUTH_TOKEN`; recovery, manual-resolution, and metrics routes use `WK_ADMIN_TOKEN`.

