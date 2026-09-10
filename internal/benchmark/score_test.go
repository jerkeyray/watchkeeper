package benchmark

import (
	"encoding/csv"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestScoreSeparatesUnknownAndUnattempted(t *testing.T) {
	r := Result{Censored: true, Steps: []Step{{Key: "1", Acknowledged: true, Attempted: true, Effects: 2, DeadlineEffects: 2}, {Key: "2", Attempted: true, Uncertain: true}, {Key: "3"}}}
	Score(&r)
	if !r.Duplicate || r.ExcessEffects != 1 || r.Missing != 2 || r.Unattempted != 1 || r.UncertainMissing != 1 || r.Abandoned != 0 || r.Completed || !r.Uncertain {
		t.Fatalf("%+v", r)
	}
}
func TestScoreOrphansAndFalseCompletion(t *testing.T) {
	r := Result{Steps: []Step{{Acknowledged: true}, {Effects: 1, DeadlineEffects: 1, Attempted: true}}}
	Score(&r)
	if r.FalseCompletion != 1 || r.Orphans != 1 {
		t.Fatalf("%+v", r)
	}
}
func TestWilsonAndBootstrap(t *testing.T) {
	lo, hi := Wilson(0, 100)
	if lo != 0 || hi < .036 || hi > .038 {
		t.Fatalf("[%v,%v]", lo, hi)
	}
	m, l, h := Bootstrap([]float64{2, 2, 2}, .5, 7)
	if m != 2 || l != 2 || h != 2 {
		t.Fatal(m, l, h)
	}
	_, l, _ = Bootstrap([]float64{2}, .95, 7)
	if !math.IsNaN(l) {
		t.Fatal("single observation interval")
	}
}
func TestReportDenominators(t *testing.T) {
	dir := t.TempDir()
	for i, r := range []Result{{Cell: "watchkeeper/none/none", Status: "completed", DurationMS: 1}, {Cell: "watchkeeper/none/none", Status: "failed"}, {Cell: "watchkeeper/none/none", Status: "invalid"}} {
		if err := writeJSONFile(filepath.Join(dir, "runs", string(rune('a'+i)), "result.json"), r); err != nil {
			t.Fatal(err)
		}
	}
	if err := Summarize(dir); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "summary.csv"))
	rows, err := csv.NewReader(strings.NewReader(string(raw))).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if rows[1][1] != "1" || rows[1][2] != "1" || rows[1][3] != "1" {
		t.Fatal(rows)
	}
}
func TestMatrixSupport(t *testing.T) {
	if Supported("idempotency_key_retry", "none", "none") || Supported("blind_retry", "receipt_status", "coordinator_after_result") {
		t.Fatal("unsupported cell accepted")
	}
	if !Supported("watchkeeper", "none", "after_commit") {
		t.Fatal("uncertainty cell excluded")
	}
}

func TestCheckAssertionsRejectsDuplicateEffects(t *testing.T) {
	c := Config{AssertNoDuplicateEffects: []string{"watchkeeper"}}
	clean := []Result{{Status: "completed", Strategy: "watchkeeper", Cell: "watchkeeper/none/after_commit"}}
	if err := CheckAssertions(c, clean); err != nil {
		t.Fatalf("clean results must satisfy the assertion: %v", err)
	}
	dirty := []Result{{Status: "completed", Strategy: "watchkeeper", Cell: "watchkeeper/none/after_commit", Duplicate: true, ExcessEffects: 1}}
	if err := CheckAssertions(c, dirty); err == nil {
		t.Fatal("duplicate watchkeeper effects must fail the assertion")
	}
	if err := CheckAssertions(c, nil); err == nil {
		t.Fatal("an asserted strategy with no completed trials must fail")
	}
	if err := CheckAssertions(Config{}, dirty); err != nil {
		t.Fatalf("research configurations must not assert: %v", err)
	}
}

func TestCheckAssertionsExcludesServiceCausedDuplicates(t *testing.T) {
	c := Config{AssertNoDuplicateEffects: []string{"watchkeeper"}}
	results := []Result{
		{Status: "completed", Strategy: "watchkeeper", Scenario: "after_commit", Cell: "watchkeeper/none/after_commit"},
		{Status: "completed", Strategy: "watchkeeper", Scenario: "duplicate_delivery", Cell: "watchkeeper/none/duplicate_delivery", Duplicate: true, ExcessEffects: 1},
	}
	if err := CheckAssertions(c, results); err != nil {
		t.Fatalf("duplicated delivery is a service behaviour and must not fail the assertion: %v", err)
	}
}
