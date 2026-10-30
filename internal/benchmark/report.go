package benchmark

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

func ReadResults(output string) ([]Result, error) {
	paths, err := filepath.Glob(filepath.Join(output, "runs", "*", "result.json"))
	if err != nil {
		return nil, err
	}
	var results []Result
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		var r Result
		if err = json.Unmarshal(raw, &r); err != nil {
			return nil, err
		}
		results = append(results, r)
	}
	return results, nil
}
func number(v float64) string {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return "NA"
	}
	return strconv.FormatFloat(v, 'f', 3, 64)
}
func interval(k, n int) string {
	lo, hi := Wilson(k, n)
	if n == 0 {
		return "NA"
	}
	return fmt.Sprintf("%d/%d [%s, %s]", k, n, number(lo), number(hi))
}
func Summarize(output string) error {
	rows, err := ReadResults(output)
	if err != nil {
		return err
	}
	groups := map[string][]Result{}
	for _, r := range rows {
		groups[r.Cell] = append(groups[r.Cell], r)
	}
	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	f, err := os.Create(filepath.Join(output, "summary.csv"))
	if err != nil {
		return err
	}
	defer f.Close()
	w := csv.NewWriter(f)
	header := []string{"cell", "valid_n", "invalid_n", "failed_n", "unsupported_n", "censored_n", "duplicate_n", "duplicate_ci_low", "duplicate_ci_high", "completion_n", "completion_ci_low", "completion_ci_high", "uncertain_n", "uncertain_ci_low", "uncertain_ci_high", "missing_workflow_n", "missing_ci_low", "missing_ci_high", "orphan_workflow_n", "orphan_ci_low", "orphan_ci_high", "false_completion_n", "median_ms", "median_ci_low", "median_ci_high", "p95_ms", "p95_ci_low", "p95_ci_high", "recovery_median_ms", "recovery_ci_low", "recovery_ci_high", "detection_median_ms", "detection_ci_low", "detection_ci_high", "resolution_median_ms", "resolution_ci_low", "resolution_ci_high", "mean_dispatches", "mean_retries", "mean_verifications", "mean_application_row_writes", "aggregate_storage_growth_bytes", "median_vs_no_failure_ratio", "excess_effects", "unattempted_steps", "uncertain_missing_steps", "terminally_abandoned_steps"}
	if err = w.Write(header); err != nil {
		return err
	}
	var md strings.Builder
	md.WriteString("# Email benchmark results\n\nRates use 95% Wilson intervals. Latency intervals use 2,000 seeded bootstrap resamples of uncensored runs; fewer than two observations has no interval. Zero observed failures is not a guarantee. Expected baseline duplicates are valid results. Missing effects are measured at the observation deadline, not assumed permanently lost. Recovery latency is split into detection delay, which is dominated by the configured `prepared_grace_ms`, and resolution time, which covers verification and durable decision.\n\n| Cell | Valid / invalid / failed / unsupported | Censored | Duplicate rate [95% CI] | Completion rate [95% CI] | Uncertain rate [95% CI] | Median / p95 ms |\n|---|---:|---:|---|---|---|---:|\n")
	for _, key := range keys {
		var n, invalid, failed, unsupported, censored, duplicates, complete, uncertain, missing, orphans, falseCompletion, excess, unattempted, uncertainMissing, abandoned int
		var latency, recovery, detection, resolution []float64
		var dispatch, retry, verify, writes, storage float64
		for _, r := range groups[key] {
			switch r.Status {
			case "invalid":
				invalid++
				continue
			case "failed":
				failed++
				continue
			case "unsupported":
				unsupported++
				continue
			case "completed":
			default:
				invalid++
				continue
			}
			n++
			if r.Censored {
				censored++
			} else {
				latency = append(latency, r.DurationMS)
				if r.RecoveryMS != nil {
					recovery = append(recovery, *r.RecoveryMS)
				}
				if r.DetectionMS != nil {
					detection = append(detection, *r.DetectionMS)
				}
				if r.ResolutionMS != nil {
					resolution = append(resolution, *r.ResolutionMS)
				}
			}
			if r.Duplicate {
				duplicates++
			}
			if r.Completed {
				complete++
			}
			if r.Uncertain {
				uncertain++
			}
			if r.Missing > 0 {
				missing++
			}
			if r.Orphans > 0 {
				orphans++
			}
			if r.FalseCompletion > 0 {
				falseCompletion++
			}
			dispatch += float64(r.Dispatches)
			retry += float64(r.Retries)
			verify += float64(r.Verifications)
			writes += float64(r.Writes)
			storage += float64(r.StorageBytes)
			excess += r.ExcessEffects
			unattempted += r.Unattempted
			uncertainMissing += r.UncertainMissing
			abandoned += r.Abandoned
		}
		median, ml, mh := Bootstrap(latency, .5, 1)
		p95, pl, ph := Bootstrap(latency, .95, 1)
		rm, rl, rh := Bootstrap(recovery, .5, 1)
		dm, dl, dh := Bootstrap(detection, .5, 1)
		sm, sl, sh := Bootstrap(resolution, .5, 1)
		parts := strings.Split(key, "/")
		baselineKey := parts[0] + "/" + parts[1] + "/none"
		var baseline []float64
		for _, v := range groups[baselineKey] {
			if v.Status == "completed" && !v.Censored {
				baseline = append(baseline, v.DurationMS)
			}
		}
		base := quantile(baseline, .5)
		ratio := math.NaN()
		if base > 0 {
			ratio = median / base
		}
		values := []string{key, strconv.Itoa(n), strconv.Itoa(invalid), strconv.Itoa(failed), strconv.Itoa(unsupported), strconv.Itoa(censored)}
		for _, k := range []int{duplicates, complete, uncertain, missing, orphans} {
			lo, hi := Wilson(k, n)
			values = append(values, strconv.Itoa(k), number(lo), number(hi))
		}
		values = append(values, strconv.Itoa(falseCompletion), number(median), number(ml), number(mh), number(p95), number(pl), number(ph), number(rm), number(rl), number(rh), number(dm), number(dl), number(dh), number(sm), number(sl), number(sh))
		for _, v := range []float64{dispatch, retry, verify, writes} {
			mean := math.NaN()
			if n > 0 {
				mean = v / float64(n)
			}
			values = append(values, number(mean))
		}
		values = append(values, number(storage), number(ratio), strconv.Itoa(excess), strconv.Itoa(unattempted), strconv.Itoa(uncertainMissing), strconv.Itoa(abandoned))
		if err = w.Write(values); err != nil {
			return err
		}
		fmt.Fprintf(&md, "| %s | %d / %d / %d / %d | %d | %s | %s | %s | %s / %s |\n", key, n, invalid, failed, unsupported, censored, interval(duplicates, n), interval(complete, n), interval(uncertain, n), number(median), number(p95))
	}
	w.Flush()
	if err = w.Error(); err != nil {
		return err
	}
	md.WriteString("\nPhysical storage deltas include page allocation and index growth and may be zero or negative for individual trials. Aggregate the dedicated suite; do not interpret individual deltas as per-operation storage costs. Application row writes count committed rows, not WAL, fsyncs, or SQL statements. Fault-observation hooks add shared experimental overhead. Full details and no-failure ratios are in summary.csv.\n")
	return os.WriteFile(filepath.Join(output, "summary.md"), []byte(md.String()), 0644)
}
