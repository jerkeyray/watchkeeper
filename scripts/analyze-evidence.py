#!/usr/bin/env python3
"""Verify committed archives and reproduce report statistics without services.

By default checks that analysis.json matches the compressed raw records.
--write updates analysis.json after the source archives pass their hash checks.
"""
import argparse
import collections
import gzip
import hashlib
import json
import statistics
from pathlib import Path

BASE = Path(__file__).resolve().parents[1] / 'docs/evidence'
STRATEGIES = ['blind_retry', 'idempotency_key_retry', 'checkpoint_recovery', 'watchkeeper']
PROFILES = ['receipt_status', 'idempotency_only', 'indirect_unique', 'none']


def quantile(values, q):
    values = sorted(values)
    point = q * (len(values) - 1)
    lo = int(point)
    return values[lo] + (values[min(lo + 1, len(values) - 1)] - values[lo]) * (point - lo)


def analyze():
    checks = json.loads((BASE / 'checksums.json').read_text())
    metrics = {}
    for name in ['research-email', 'storage-overhead']:
        packed = (BASE / name / 'raw.jsonl.gz').read_bytes()
        raw = gzip.decompress(packed)
        for key, payload in [('raw_sha256', raw), ('gzip_sha256', packed)]:
            if hashlib.sha256(payload).hexdigest() != checks[name][key]:
                raise ValueError(f'{name}: {key} mismatch')
        xs = [json.loads(line) for line in raw.splitlines()]
        valid = [x for x in xs if x['status'] == 'completed']
        metrics[name] = {
            'statuses': dict(collections.Counter(x['status'] for x in xs)),
            'cells': len({x['cell'] for x in valid}),
        }
        m = metrics[name]
        if name == 'research-email':
            grouped = collections.defaultdict(list)
            for x in valid:
                grouped[x['cell']].append(x)
            fields = ['steps', 'duplicate', 'completed', 'uncertain', 'false_completion', 'terminally_abandoned']
            def signature(x):
                return json.dumps({k: x[k] for k in fields}, sort_keys=True)
            m['invariant_correctness_cells'] = sum(len({signature(x) for x in rows}) == 1 for rows in grouped.values())
            m['totals'] = {k: sum(x[k] for x in valid) for k in [
                'false_completion', 'terminally_abandoned', 'missing_at_deadline',
                'unattempted', 'uncertain_missing', 'unacknowledged_effects', 'excess_effects', 'censored']}
            m['faults'] = {}
            for fault in sorted({x['scenario'] for x in valid}):
                m['faults'][fault] = {}
                for strategy in STRATEGIES:
                    rows = [x for x in valid if x['scenario'] == fault and x['strategy'] == strategy]
                    m['faults'][fault][strategy] = [sum(x['duplicate'] for x in rows), len(rows)]
            m['watchkeeper_profiles'] = {}
            for profile in PROFILES:
                rows = [x for x in valid if x['strategy'] == 'watchkeeper' and x['profile'] == profile and x['scenario'] != 'none']
                m['watchkeeper_profiles'][profile] = {
                    'n': len(rows), 'completed': sum(x['completed'] for x in rows),
                    'uncertain': sum(x['uncertain'] for x in rows),
                    'duplicates': sum(x['duplicate'] for x in rows)}
            m['latency'] = {}
            for strategy in STRATEGIES:
                rows = [x for x in valid if x['strategy'] == strategy and x['scenario'] != 'none']
                m['latency'][strategy] = {k: statistics.median(x[k] for x in rows if k in x)
                    if any(k in x for x in rows) else None
                    for k in ['recovery_ms', 'detection_ms', 'resolution_ms']}
        else:
            m['strategies'] = {}
            for strategy in sorted({x['strategy'] for x in valid}):
                rows = [x for x in valid if x['strategy'] == strategy]
                m['strategies'][strategy] = {
                    'n': len(rows), 'median_ms': statistics.median(x['duration_ms'] for x in rows),
                    'p95_ms': quantile([x['duration_ms'] for x in rows], .95),
                    'mean_row_changes': statistics.mean(x['application_row_writes'] for x in rows),
                    'mean_relation_growth_bytes': statistics.mean(x['storage_growth_bytes'] for x in rows)}
    return metrics


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--write', action='store_true', help='replace analysis.json with the recomputed values')
    args = parser.parse_args()
    result = analyze()
    target = BASE / 'analysis.json'
    if args.write:
        target.write_text(json.dumps(result, indent=2) + '\n')
    elif result != json.loads(target.read_text()):
        raise SystemExit('FAIL: analysis.json differs from recomputed raw-record statistics')
    print('PASS: archive hashes and all retained analysis statistics match')
