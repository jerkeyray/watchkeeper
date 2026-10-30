# Published evaluation evidence

The source-controlled evidence supports the [final project report](../final-project-report.md) and [evaluation summary](../evaluation.md). It is copied from the retained local experiment artifacts without changing historical source or environment fields.

| Path | Contents |
| --- | --- |
| `research-email/raw.jsonl.gz` | 15,900 valid execution records and 6,500 unsupported records |
| `research-email/manifest.json` | Original configuration, source, schema, image, host, and measurement provenance |
| `research-email/summary.csv` | Original per-cell aggregate report |
| `storage-overhead/raw.jsonl.gz` | 1,300 valid clean-path records and 300 unsupported records |
| `storage-overhead/manifest.json` | Original independent overhead-batch provenance |
| `storage-overhead/summary.csv` | Original per-cell overhead summary |
| `examples/reconciled.json` | One after-commit status-based recovery with its operation, observation, decision, audit, and ledger |
| `examples/uncertain.json` | One no-evidence recovery preserving uncertainty and unattempted downstream work |
| `analysis.json` | Derived counts, invariant correctness checks, pooled latency, and overhead statistics |
| `checksums.json` | SHA-256 hashes of uncompressed raw bytes and committed gzip archives |

The archives contain structured trial records, not the full per-execution logs or retained database projects. The selected examples expose inspectable decision provenance but are not a replacement for every original log. The larger `results/` directory remains generated and ignored by Git.

To inspect without decompressing files in place:

```sh
gzip -dc docs/evidence/research-email/raw.jsonl.gz | head -n 1
```

The CSV generator includes Wilson intervals and bootstrap timing intervals. Because all correctness outcomes are invariant within the deterministic cells, the final report uses condition-specific correctness counts without treating those repetitions as independent samples of real deployment failures.

To build a new experiment, follow [the benchmark guide](../benchmarks.md). Use a new output name and retain its own manifest. A documentation update does not constitute a fresh experiment.

Run `make evidence-check` or `python3 scripts/analyze-evidence.py` to verify both archive hashes and reproduce every value in `analysis.json` without Docker or a database.
