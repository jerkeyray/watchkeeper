# Live presentation demo

Run `make demo`, then open http://127.0.0.1:8070. Keep this process running while presenting.
The command starts PostgreSQL, applies migrations, and starts the API, email simulator,
and recovery coordinator. The presentation console runs locally on port 8070.

## A five-minute walkthrough

1. **Normal:** select the scenario and click **Run**. An order-confirmation
   email goes to the simulated service. Both approaches create one effect. Watchkeeper
   saves intent before sending and stores the receipt.
2. **Reply lost:** both emails commit, but the demo deliberately omits the Watchkeeper
   confirmation. Blind retry sends again and produces two effects. Watchkeeper's running
   coordinator discovers pending intent, checks the service by operation ID, and reconciles
   it with one effect and one dispatch.
3. **No evidence:** the operation declares no evidence capability. Watchkeeper
   stops uncertain rather than authorizing another send. Safe recovery depends on the
   evidence a service exposes.
4. Expand **Evidence** at the bottom to show the operation ID and the audit trail
   read from the Watchkeeper API.

The main point: an unfinished workflow does not prove an external action failed.
Watchkeeper uses durable intent, stable identity, and evidence to decide what is safe.

## What the screen is showing

This is a live demonstration against the local research stack. The baseline sends
through the same simulator without deduplication. Its explicit replay models blind retry
of one ambiguous operation. Every demo run creates fresh identities. Both approaches use
the same recipient and template, with separate logical message keys to keep evidence
independent. The normal baseline does not persist workflow progress; its completion is
observed by the demo controller.

The fault deliberately omits local confirmation after external commit; it reproduces
the uncertain-completion window without killing an OS process. Existing process-crash
benchmarks remain separate. Presenter pacing delays are not performance measurements.
The email service is a simulator: no real email is delivered.

Counts come from the simulator's independent admin ledger. That ledger is for display;
the coordinator uses its normal verifier and declared capability profile. In the
no-evidence scenario the simulator retains its status endpoint, but the coordinator
does not use it for the operation declaring `none`.

## Configuration

The console accepts `WK_API_URL`, `WK_AUTH_TOKEN`, `SIMULATOR_URL`,
`SIM_AUTH_TOKEN`, and `SIM_ADMIN_TOKEN`. Defaults match the development Compose stack.
Credentials stay on the server. `DEMO_HTTP_ADDR` defaults to `127.0.0.1:8070`.

Start just the console against already running services with:

```sh
go run ./cmd/watchkeeper-demo
```

The coordinator must run for recovery scenarios. Dependency failures and recovery
timeouts appear on screen with any available evidence. Reloading restores the current
run within the same console process. Up to 20 runs remain in console memory; operations
and effects remain in PostgreSQL. Stop the console with Ctrl+C. `make compose-down`
stops backing services while retaining their database volume.

## Terminal process crash comparison

`make demo-cli` starts the same development services and runs `cmd/demo`. Unlike the browser console, it kills a child worker process with SIGKILL after external commit, then compares blind retry, status-based Watchkeeper recovery, and no-evidence uncertainty. The controller reads the independent simulator ledger. `DEMO_REPEAT` defaults to 20 repeated trials and can be set to zero for the narrated run only. `DEMO_PACE=0` removes presentation delays.

The terminal and browser demos each intend one email per approach. The research benchmark uses three sequential actions and targets step two, so its total effect counts differ. Neither demonstration sends real email. Presenter pacing is not a benchmark latency.

[The final project report](final-project-report.md) is the current comprehensive project description. Course-specific slides and scripts are presentation aids rather than the authoritative system specification.
