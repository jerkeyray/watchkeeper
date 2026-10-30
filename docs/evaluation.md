# Retained evaluation results

This is a final evaluation summary, not a proposal. The [comprehensive report](final-project-report.md) describes the architecture, method, conclusions, and limitations. [Evidence](evidence/README.md) includes the underlying compressed records, manifests, and selected traces.

## Datasets

| Batch | Valid executions | Unsupported combinations | Supported cells |
| --- | ---: | ---: | ---: |
| Main email matrix | 15,900 | 6,500 | 159 |
| Clean storage and overhead | 1,300 | 300 | 13 |

Each supported cell contains 100 deterministic repetitions. The main dataset has no failed, invalid, or censored records. Correctness outcomes are invariant within all 159 cells; these repetitions do not sample an arbitrary real-world fault distribution. Report counts by condition, rather than treating the pooled matrix as a population failure rate.

## Matched crash after external commit

| Strategy and capability | Duplicate trials | Complete workflows | Uncertain trials |
| --- | ---: | ---: | ---: |
| Blind retry with receipt/status | 100/100 | 100/100 | 0/100 |
| Checkpoint with receipt/status | 100/100 | 100/100 | 0/100 |
| Watchkeeper with receipt/status | 0/100 | 100/100 | 0/100 |
| Watchkeeper with no evidence | 0/100 | 0/100 | 100/100 |
| Keyed retry with durable idempotency | 0/100 | 100/100 | 0/100 |

Keyed retry uses a different service contract, as labeled. It is a sufficient alternative where durable deduplication is available. Zero duplicate trials for the no-evidence profile must be reported alongside zero completed workflows.

## Failure boundaries

Watchkeeper produced no duplicate trials in the tested process-crash conditions. Under `duplicate_delivery`, the service itself commits accepted work twice in the three profiles without keyed deduplication, producing 300/400 duplicate trials. Single-send authorization cannot prevent internal service duplication.

Under `delayed_commit`, the service commits after the first empty lookup. Receipt/status, indirect, and no-evidence Watchkeeper profiles each stop uncertain in 100/100 trials. Only the explicit idempotency contract justifies replay and completion. An empty status result after dispatch is not proof of permanent absence.

The main dataset records zero false-completion steps and zero terminally abandoned steps, but also 1,900 missing-at-deadline steps: 1,300 unattempted downstream steps and 600 uncertain-missing steps. These are different outcomes. The data does not show that every intended effect was delivered.

## Clean path overhead

| Strategy | Valid executions | Median ms | p95 ms | Mean application row changes | Mean relation growth B |
| --- | ---: | ---: | ---: | ---: | ---: |
| Blind retry | 400 | 4.58 | 7.74 | 6.00 | 1,454 |
| Keyed retry | 100 | 5.02 | 7.67 | 9.00 | 3,277 |
| Checkpoint | 400 | 8.29 | 11.94 | 9.00 | 1,597 |
| Watchkeeper | 400 | 18.59 | 26.78 | 28.75 | 8,028 |

Rows are pooled from the dedicated no-failure raw records, not averages of per-cell medians or percentiles. Relation growth includes allocator noise. Application row changes are not physical writes, WAL, or fsync counts.

Watchkeeper's median fault recovery is 635.44 ms, median detection 531.73 ms, and median resolution 115.15 ms, with a configured 500 ms grace. Independent medians need not add exactly. The baselines restart without this detection step, so their recovery times are not directly comparable protocol costs.

## Source lineage and current validation

The historical main manifest records revision `ce072f6a25addbc7739afc3707b86054c78bf572` and a dirty-source fingerprint. Preserve those fields. Later documentation, demo, or timing-instrumentation commits do not change which source generated the retained experiment.

These are retained results, not a new full benchmark run during documentation preparation. See [Testing](testing.md) for current local checks and sandbox restrictions. The supplied concurrency evaluation reports 500/500 single winners among 12 authorizers per operation; its test source is retained, but a fresh integration rerun was unavailable and no independent concurrency raw dataset is included here.
