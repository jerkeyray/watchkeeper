# Watchkeeper documentation

The [final project report](final-project-report.md) is the comprehensive project description. [Download the PDF](report/Watchkeeper_Final_Project_Report.pdf). It covers the problem, scope, contribution against existing approaches, AI formulation, architecture, stack, protocols, evaluation, repository, and limitations.

## Guide map

| Guide | Purpose |
| --- | --- |
| [Final project report](final-project-report.md) | Complete implementation and evaluation document |
| [Getting started](getting-started.md) | Requirements, local stack, smoke proofs, and configuration |
| [Mental model](mental-model.md) | Intent, operation identity, evidence, and state semantics |
| [Architecture](architecture.md) | Components, storage ownership, transactions, and stack |
| [Recovery](recovery.md) | Leases, observation classification, policy, retry, and manual resolution |
| [API](api.md) | Authentication, endpoints, dispatch guard, versions, and typed errors |
| [Testing](testing.md) | Test layers, CI definitions, current validation limits, and reproduction |
| [Benchmarks](benchmarks.md) | Matrix configuration, process faults, metrics, and resume rules |
| [Evaluation](evaluation.md) | Retained results and interpretation |
| [Evidence](evidence/README.md) | Compressed raw data, manifests, summaries, checksums, and examples |
| [Demo](demo.md) | Browser lost-confirmation and terminal process-crash presentation flows |
| [Roadmap](roadmap.md) | Delivered scope, further experiments, and production work |

The [OpenAPI contract](../api/openapi.yaml) and [migrations](../migrations/) define the HTTP and storage interfaces. The runnable workload is email with all four evidence profiles. The current policy is deterministic and symbolic. Additional workloads and learned policies remain future work.

The [AI slide content](ai-course-ppt-content.md) and [course speaking script](presentation-script.md) are presentation aids. Their course framing does not add implemented workloads, machine learning, or stronger guarantees.
