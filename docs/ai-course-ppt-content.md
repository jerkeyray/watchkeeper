# Watchkeeper: Explainable AI for Safe Workflow Recovery

> Course presentation material. The current implementation, limitations, and retained evidence are documented in the [final project report](final-project-report.md) and [evaluation](evaluation.md). This script is not the canonical project specification.

## Presentation goal

Present Watchkeeper as a **symbolic, evidence-driven decision system** that decides whether an interrupted workflow should continue, retry, or abstain. The current prototype does not use machine learning, neural networks, or an LLM. Its AI relevance comes from explicit knowledge representation, reasoning under uncertainty, autonomous decision-making, and explainable outcomes.

Recommended length: 8–10 minutes, 9 slides.

---

## Slide 1 — Title

### Watchkeeper

**Explainable AI for Safe Recovery of Interrupted Workflows**

Subtitle: *Reasoning from incomplete evidence before taking an irreversible action*

**Say:**

> Watchkeeper is a decision system for a difficult situation: an external action may have happened, but the program crashed before recording the result. The system must decide whether to retry, accept completion, or stop because the evidence is insufficient.

---

## Slide 2 — The intelligent decision problem

### Did the action happen?

1. A workflow asks an external service to send an email.
2. The service may commit the email.
3. The worker crashes before saving the receipt.
4. After restart, local state says “incomplete,” but reality is unknown.

| Decision | Risk |
|---|---|
| Retry immediately | The email may be sent twice |
| Skip the retry | The email may never be sent |
| Investigate evidence | Slower, but safer and explainable |

**Say:**

> This is a partially observable decision problem. The system cannot directly see the past event, so it must infer the safest action from durable state and external evidence.

---

## Slide 3 — Why this belongs in AI

### AI concepts implemented by Watchkeeper

- **Knowledge representation:** operation identity, expected effect, state, evidence, and service capabilities are stored explicitly.
- **Symbolic reasoning:** deterministic rules map observations to recovery decisions.
- **Reasoning under uncertainty:** missing evidence is not treated as proof that nothing happened.
- **Autonomous agent behavior:** a coordinator observes, decides, and acts without replaying blindly.
- **Abstention:** the system chooses `Uncertain` when it cannot justify a safe action.
- **Explainability:** every observation and decision is preserved in an audit trail.

**Say:**

> This is classical or symbolic AI rather than statistical AI. The intelligence is in representing knowledge, applying safety rules, and knowing when not to act.

---

## Slide 4 — The agent model

### Observe → Reason → Act → Record

```text
Interrupted operation
        |
        v
Observe service evidence
        |
        v
Check identity, payload, count, and capability contract
        |
        v
Decide: Reconciled | Retryable | Uncertain
        |
        v
Continue, retry safely, or request human review
        |
        v
Record evidence and explanation
```

**AI interpretation:**

- **Environment:** workflow database and external email service
- **Agent:** recovery coordinator
- **Sensors:** receipt lookup, status lookup, indirect field inspection, capability metadata
- **Actions:** reconcile, authorize retry, abstain, or defer after a transient failure
- **Objective:** complete workflows while minimizing duplicate and missing effects

---

## Slide 5 — Knowledge and rules

### Five states encode what the system knows

| State | Meaning |
|---|---|
| `Prepared` | Intent is durable; completion is not yet confirmed |
| `Confirmed` | The normal response was stored |
| `Reconciled` | Later evidence proves the effect completed |
| `Retryable` | Another attempt is proven safe |
| `Uncertain` | Completion and safe replay cannot be established |

### Example decision rules

```text
IF matching authoritative evidence shows one effect
THEN Reconciled

IF dispatch was never authorized
THEN Retryable

IF the service guarantees stable-key deduplication
THEN Retryable with the same identity and payload

IF evidence conflicts, is ambiguous, or is insufficient
THEN Uncertain
```

**Say:**

> The key AI behavior is conservative inference. For example, an empty status response after dispatch is not proof of absence because a delayed request may still commit.

---

## Slide 6 — Explainability and human oversight

### Every decision has a trace

Watchkeeper records:

- the original intent and expected effect;
- the stable operation identity and request fingerprint;
- which recovery mechanism was used;
- the raw observation and whether it was authoritative;
- the rule-based decision and reason code;
- the state transition and operation version;
- any later manual resolution, including actor and evidence reference.

**Say:**

> A professor or operator can reconstruct why the agent acted. `Uncertain` is also a human-in-the-loop boundary: automation stops and preserves the evidence for later review.

---

## Slide 7 — Experiment and comparison

### Three-step email workflow

The benchmark compares four recovery strategies:

1. Blind retry
2. Idempotency-key retry
3. Checkpoint recovery
4. Watchkeeper

It tests four evidence environments:

1. Receipt/status lookup
2. Durable idempotency only
3. Indirect unique-field inspection
4. No recovery evidence

The research matrix contains **159 supported cells and 15,900 executed trials**, using process crashes, lost responses, delayed commits, duplicate delivery, and temporary service/database failures. Another **6,500 combinations are explicitly marked unsupported**, rather than being silently treated as successful tests.

**Say:**

> The benchmark evaluates decision quality under different observability conditions. Ground truth comes from an independent effect ledger that the recovery agent cannot access.

---

## Slide 8 — Main findings

### Safety improves when trustworthy evidence exists

- Blind retry frequently duplicated already completed work after restart.
- Idempotency-key retry produced one effect per logical step when the service guaranteed durable deduplication.
- Checkpoint recovery protected previously acknowledged steps, but could still duplicate the in-flight step.
- Watchkeeper produced no duplicate caused by its crash-recovery decision path.
- With weak or missing evidence, Watchkeeper selected `Uncertain` and stopped instead of guessing.
- A deliberately faulty service could still duplicate an accepted delivery; Watchkeeper cannot undo behavior inside an external service.

### Cost of safer reasoning

- Clean-path median latency: approximately **18.6 ms** for Watchkeeper versus **4.6 ms** for blind retry.
- Committed application-row writes: approximately **28.8** versus **6.0** per clean trial.
- Median Watchkeeper recovery latency: approximately **661 ms**, including a configured 500 ms detection grace period.
- Dispatch-authorization concurrency test: **500 of 500 operations** had exactly one winner among 12 concurrent callers.

**Say:**

> The result is a safety-versus-cost tradeoff. More reasoning, durable evidence, and auditability add latency and writes.

---

## Slide 9 — Limitations and future AI work

### Current limitations

- The current policy is hand-authored and deterministic.
- There is no learned model, LLM, classifier, or anomaly detector.
- Experiments use a simulated email service and process failures on one host.
- The design depends on the quality of evidence exposed by the external service.
- `Uncertain` protects safety but may leave downstream work incomplete.

### Credible AI extensions

- Learn risk estimates from historical recovery outcomes while keeping hard safety rules as constraints.
- Rank or select verification mechanisms using cost, latency, and confidence.
- Detect anomalous or contradictory evidence before automated recovery.
- Use probabilistic reasoning to estimate whether a delayed operation may still commit.
- Generate operator explanations from the structured audit trace.
- Compare a learned policy with the deterministic policy using false-completion and duplicate-effect rates.

**Say:**

> Machine learning should advise the decision process, not bypass the safety boundary. High-risk actions would still require verifiable evidence or human approval.

---

## Closing statement

> Watchkeeper demonstrates an explainable AI approach to recovery: represent what is known, gather evidence, apply explicit rules, act only when justified, and abstain when uncertainty cannot be resolved.

---

## Questions the professor may ask

### “Where exactly is the AI?”

The recovery coordinator is a symbolic agent. It observes an uncertain environment, uses explicit knowledge and rules to select an action, and records an explanation. It is AI in the classical decision-system sense, although it is not machine learning.

### “Why not simply use an LLM?”

The recovery decision can cause irreversible external effects, so correctness must be deterministic, testable, and auditable. An LLM could explain structured evidence to an operator, but it should not invent whether an effect occurred.

### “What makes `Uncertain` useful?”

It is rule-based abstention. The system refuses to make a completion or retry claim when neither is supported, preventing false confidence from becoming an unsafe action.

### “Is this exactly-once execution?”

No. The guarantee is conditional on evidence and service capabilities. Watchkeeper controls its own dispatch authorization and recovery behavior, but it cannot force an external service to be exactly once.

### “How would you add machine learning?”

Use a model to estimate recovery risk, prioritize verification, or detect anomalies. Keep deterministic safety constraints around dispatch, identity matching, and minimum evidence requirements, then compare learned and fixed policies experimentally.

### “What is the project’s research question?”

> Can an explainable, evidence-aware recovery agent reduce duplicate effects after workflow crashes without making false completion claims, and what latency and storage overhead does that safety require?

---

## Repository readiness for an AI-course presentation

### Ready now

- The implementation, tests, reproducible benchmark harness, retained results, documentation, and explainable decision trace are present.
- Test definitions cover the repository; see [Testing](testing.md) for current execution status and environment limitations.
- The benchmark dataset provides measurable comparisons, limitations, and statistical intervals.

### Add before presenting if the course requires machine learning

- A small, explicit learned component and dataset split.
- A baseline model and evaluation metrics such as precision/recall for safe-retry prediction, calibration error, false-completion rate, and unsafe-retry rate.
- A comparison showing whether the model improves cost or recovery time while remaining behind hard safety constraints.

If the course accepts symbolic AI, intelligent agents, expert systems, or decision-making under uncertainty, the repository is ready for the presentation. If the rubric specifically requires training a model, the repository needs an ML extension before it should be presented as an ML project.
