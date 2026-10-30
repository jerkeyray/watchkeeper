# Watchkeeper: Review 2 speaking script

> Course presentation material. The current implementation, limitations, and retained evidence are documented in the [final project report](final-project-report.md) and [evaluation](evaluation.md). This script is not the canonical project specification.

BCSE308P Computer Networks · Aditya, Anshith, Ananta

About 12 minutes of talking plus a 4–5 minute demo. Say it in your own words. The
**bold lines** are the ones worth landing exactly.

---

## The project in 30 seconds (everyone should be able to say this)

When a workflow calls an external service over the network and the call times
out, the caller cannot tell what happened. The request may never have arrived,
the service may have failed, or the service may have done the work and only the
reply was lost. Retrying blindly turns that last case into a duplicate: a second
email, a second charge. Checkpointing does not help either, because the crash
happens between "service committed" and "we wrote it down".

Watchkeeper fixes this with three rules:

1. **Write the intent durably before sending.** The intent is the operation,
   given a stable operation ID.
2. **Send that ID with the request.** It goes in the `X-Watchkeeper-Operation-ID`
   header, so the service can be asked about it later.
3. **After a crash, ask before resending.** Recovery queries the service by the
   ID. The recovery decision has three principal outcomes:
   - **Reconciled:** the effect exists, so it is not resent.
   - **Retryable:** dispatch was never authorized, or a verified durable idempotency contract makes replay safe.
   - **Uncertain:** there is no evidence, so it stops for a human.

**Retained matched after-commit result with status evidence: 0/100 duplicate trials for Watchkeeper versus 100/100 for each blind retry and checkpoint recovery. Watchkeeper completed 100/100; without evidence it stopped uncertain instead.**

---

## Slide by slide

Suggested split: **Aditya** slides 1–5, **Anshith** slides 6–11 plus the demo,
**Ananta** slides 12–16. Swap these if your roles differ.

### 1. Title (Aditya)
"We're presenting Watchkeeper, a crash-consistent recovery layer for workflows
that call services over unreliable networks. I'm Aditya, with Anshith and
Ananta."

### 2. Problem & objectives
- Workflows such as orders, payments and notifications call remote services over
  HTTP.
- Networks drop packets and processes crash. The dangerous moment is **after the
  remote side commits but before the caller hears back**.
- Objectives:
  - Never duplicate an external effect.
  - Never falsely claim success.
  - Recover automatically whenever the evidence allows it.

### 3. A timeout has three causes
**"A timeout tells you nothing about what happened on the other side."**
- The request was lost on the way: safe to retry.
- The service failed before committing: safe to retry.
- The reply was lost on the way back: the effect is already done, and **retrying
  duplicates it**.

The caller sees the same timeout in all three cases. That is the core networking
problem: there is no common knowledge across an unreliable channel, which is the
Two Generals problem.

### 4. Existing approaches
- **Blind retry:** simple, but duplicates in the reply-lost case.
- **Checkpointing:** it records progress, but the crash window sits between the
  commit and the checkpoint.
- **Idempotency keys:** they help only if the service supports them, and they do
  not tell you the outcome.
- **Exactly-once messaging and transactions:** these need both sides in one
  system. External services are not.

### 5. Proposed solution
The three rules above. Keep it to one sentence each. **"Intent before dispatch,
stable ID on the wire, evidence before redispatch."**

### 6. What is new (Anshith)
- **Evidence-based recovery with an honest "Uncertain" outcome.** We never guess.
- **Single-send authorization.** The server grants permission to dispatch once.
  A restarted worker asking again is refused with HTTP 409.
- **Leases for recovery.** Leases are hashed, expire, and use versioned writes,
  so two recoverers can't both act.
- **An independent ground truth.** The simulated service keeps its own effect
  ledger. We measure duplicates there, not from our own logs.

### 7. Final architecture
Follow the flow from left to right:
1. The workflow calls the Watchkeeper API to *prepare* the operation. The
   operation is stored in PostgreSQL.
2. The workflow asks the API to *authorize* dispatch, then sends the request with
   the operation ID to the email service.
3. The **coordinator** runs separately. It finds operations stuck after dispatch,
   claims a lease, and queries the service's status endpoint by ID.

Deployment is Docker Compose, with Goose migrations for the database.

### 8. Protocol sequence
Walk the arrows: prepare → authorize → send with the ID → reply lost → crash →
restart → authorize refused → coordinator asks the service by ID → "committed"
→ reconciled. **"Nothing is ever resent without evidence."**

### 9. Methodology
- The rules table and the state machine: prepared → dispatched →
  confirmed / reconciled / retryable / uncertain.
- Every transition is a versioned write, so a stale worker cannot overwrite
  newer state.

### 10. Implementation
Go, PostgreSQL, a REST API, the coordinator, the email simulator, and the
benchmark harness. Give the numbers from the slide; don't read the list.

### 11. Live demonstration → switch to the demo (see the next section)

### 12. Testing (Ananta)
- Unit and race tests, smoke tests, and recovery smoke tests.
- Fault injection: dropped requests, dropped replies, crashes at each step, and
  missing evidence.

### 13. Results
**"15,900 trials. In the ambiguous fault cases, blind retry and checkpointing
duplicated in 100 out of 100 runs per fault, 1,000 of 1,000 in total.
Watchkeeper duplicated in 0 of 500. Zero false completions."**

### 14. Performance & trade-offs
- Normal path: about **18.8 ms** with Watchkeeper against about **5.1 ms** for
  blind retry. That is the cost of the durable write and the authorization
  round trip.
- Recovery: a pooled median of **635 ms**, with a 532 ms median detection component
  and a 115 ms median resolution component. These medians are computed separately.
- **"Durable recovery adds overhead while avoiding recovery-induced duplication in the tested crash conditions."**

### 15. Individual contributions
Each person gives one sentence on what they built:
- **Aditya:** protocol, API and storage.
- **Anshith:** recovery coordinator and simulator.
- **Ananta:** fault injection and benchmarks.

### 16. Conclusion & future work
- A timeout is ambiguous. Watchkeeper resolves it with evidence instead of
  guessing.
- Future work:
  - More service types (payments, storage).
  - Adaptive grace periods.
  - Distributed coordinators.
  - Services that don't expose a status endpoint.

---

## Demo script

Have both ready before you start:
- a browser on `http://127.0.0.1:8070` from `make demo`;
- a terminal in the repo.

### Part 1: web console (about 90 s)

1. **Reply lost → Run trial.**
   "Same email, two strategies, same service. The service commits, and then
   the reply is dropped. You can see the ✕."
   "Blind retry has no record, so it sends again. Look at the ledger: two emails,
   one marked as a duplicate."
   "Watchkeeper doesn't resend. The coordinator asks the service 'GET status?'
   using the operation ID, gets 'committed', and marks it **Reconciled**. One
   email."
2. **No evidence → Run trial.**
   "Now the service can't answer status queries. Blind retry still duplicates.
   Watchkeeper doesn't guess: it marks the operation **Uncertain** and stops for
   a human. **Halting is better than a wrong answer.**"
3. Optionally, open *Audit evidence* to show the operation ID and the audit
   trail.

### Part 2: CLI, a real crash (about 2–3 min)

Run:

```bash
make demo-cli
```

The run has five parts: the setup, three trials (A, B, C), a repeated-crash run
and a results table. The cyan lines are the actual HTTP messages on the wire.
Point at them; this is a networks course.

| On screen | Say |
|---|---|
| **Setup / Measure** header | "This isn't an animation. Each worker is a real OS process, and we kill it with `kill -9` at the worst moment. The counts come from the email service's own database, which no strategy can see." |
| **Trial A**, `→ POST /email/messages  X-Watchkeeper-Operation-ID: …` | "First, ordinary blind retry. Here's the request going out." |
| `✓ email committed`, `← 201 Created` | "The service did the work and replied…" |
| `✗ kill -9 …`, `ledger already holds 1 row` | "…and we kill the worker before it records the reply. The email exists, but the worker never found out. This is the lost-reply window." |
| restart, a second `→ POST` with a **different ID** | "The restarted worker has no memory, so it sends again. Notice the operation ID changed: the service has no way to tell this is the same email." |
| Ledger: 2 rows, `duplicate` | "Two rows in the service's table. The customer got two emails." |
| **Trial B**, `→ POST /v1/operations`, `state=prepared v1` | "Now Watchkeeper. Before anything goes to the email service, the intent is written to PostgreSQL. It gets an operation ID and a version number." |
| `→ …/authorize-dispatch`, `← 200 OK dispatch granted once` | "The worker asks permission to send. The server grants it **once**." |
| `→ POST /email/messages  X-Watchkeeper-Operation-ID: …` | "The send carries that same operation ID in a header. That ID is what makes recovery possible." |
| `✗ kill -9 …` | "Same crash, same moment." |
| restart, `← existing operation … v2` | "The new worker re-registers the same workflow and gets back the *existing* operation, not a new one." |
| `→ authorize-dispatch`, `← HTTP 409 dispatch_not_authorized` | "It asks to send again, and **the server refuses with a 409**. The decision isn't left to a client that may have lost its memory." |
| `coordinator → GET /email/operations/…`, `← 200 committed` | "The coordinator asks the service directly, by operation ID: did this happen? It did." |
| `+0.65s operation state → reconciled` | "So the operation is reconciled, with nothing resent. The roughly half-second delay is our grace period, the same latency trade-off as on slide 14." |
| `audit trail: …` | "Every step is in the audit log: prepared, authorized, reconciliation requested, recovery claimed, result." |
| Ledger: 1 row | "One row. Exactly one email." |
| **Trial C**, `profile=none`, `the service offers no way to ask` | "Last trial: same crash, but this service can't answer status queries." |
| `! marked Uncertain, dispatch halted` | "Watchkeeper doesn't guess. Resending might duplicate, and assuming success might lose the email, so it halts as **Uncertain** for a human. Still one email." |
| **Repeated crashes** progress bars | "These are 20 repeated controlled SIGKILL executions for each strategy. Watch the committed-effect counts. Repetitions test consistency and timing; they do not sample arbitrary real-world fault conditions." |
| **Results** table | "Read the counts from this live run. The retained benchmark reports zero recovery-induced duplicate trials for Watchkeeper in the tested crash conditions and zero false-completion steps. It also includes service-side duplicated delivery, uncertainty, and incomplete downstream work, which must be reported separately." |

If you're short on time, run `DEMO_REPEAT=0 make demo-cli` to skip the repeated
crashes, or `DEMO_PACE=300 make demo-cli` to make the narration faster.


If something fails on stage, run `make compose-up` and then run it again. Each
trial uses a fresh key, so reruns never interfere with each other.

---

## Likely questions

- **"Isn't this just idempotency keys?"** No. An idempotency key only prevents
  duplicates if the service enforces it, and it doesn't tell you the outcome. We
  add durable intent, single-send authorization, an explicit recovery protocol,
  and an honest Uncertain state when no evidence exists.
- **"What if the service has no status endpoint?"** That is the *No evidence*
  case. We mark the operation Uncertain and never resend automatically. That's
  safe by design; the cost is human review.
- **"Why not exactly-once delivery?"** Exactly-once across an unreliable network
  to an independent service isn't achievable in general. We give *at most once
  automatically*, plus evidence-based reconciliation.
- **"What if the coordinator crashes?"** Its lease expires and another
  coordinator claims it. Versioned writes stop a stale one from overwriting a
  newer state.
- **"Where does the 528 ms grace period come from?"** It's a configurable wait
  before recovery starts, so an in-flight reply can still arrive. It's a tuning
  knob, and adaptive grace periods are in our future work.
- **"How do you know your numbers are right?"** Duplicates are counted from the
  simulated service's independent effect ledger, not from Watchkeeper's own
  records.
