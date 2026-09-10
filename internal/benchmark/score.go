package benchmark

import (
	"fmt"
	"math"
	"math/rand"
	"slices"
	"strings"
	"time"
)

type Step struct {
	Key             string `json:"key"`
	Attempted       bool   `json:"attempted"`
	Acknowledged    bool   `json:"acknowledged"`
	Uncertain       bool   `json:"uncertain"`
	Effects         int    `json:"effects"`
	DeadlineEffects int    `json:"deadline_effects"`
}
type Result struct {
	ExecutionID      string    `json:"execution_id"`
	Cell             string    `json:"cell"`
	Strategy         string    `json:"strategy"`
	Profile          string    `json:"profile"`
	Scenario         string    `json:"scenario"`
	Repetition       int       `json:"repetition"`
	Seed             int64     `json:"seed"`
	Status           string    `json:"status"`
	Error            string    `json:"error,omitempty"`
	Steps            []Step    `json:"steps"`
	StartedAt        time.Time `json:"started_at"`
	DurationMS       float64   `json:"duration_ms"`
	RecoveryMS       *float64  `json:"recovery_ms,omitempty"`
	Completed        bool      `json:"completed"`
	Censored         bool      `json:"censored"`
	Duplicate        bool      `json:"duplicate"`
	ExcessEffects    int       `json:"excess_effects"`
	Missing          int       `json:"missing_at_deadline"`
	Unattempted      int       `json:"unattempted"`
	UncertainMissing int       `json:"uncertain_missing"`
	Abandoned        int       `json:"terminally_abandoned"`
	Orphans          int       `json:"unacknowledged_effects"`
	FalseCompletion  int       `json:"false_completion"`
	Uncertain        bool      `json:"uncertain"`
	Dispatches       int       `json:"dispatches"`
	Retries          int       `json:"retries"`
	Verifications    int       `json:"verifications"`
	Writes           int64     `json:"application_row_writes"`
	StorageBytes     int64     `json:"storage_growth_bytes"`
	FaultTriggered   bool      `json:"fault_triggered"`
}

func Score(r *Result) {
	r.Completed = true
	for _, s := range r.Steps {
		if !s.Acknowledged {
			r.Completed = false
		}
		if s.Effects > 1 {
			r.Duplicate = true
			r.ExcessEffects += s.Effects - 1
		}
		if s.DeadlineEffects == 0 {
			r.Missing++
			if !s.Attempted {
				r.Unattempted++
			} else if s.Uncertain {
				r.UncertainMissing++
			} else if !r.Censored {
				r.Abandoned++
			}
		}
		acknowledgedEffects := 0
		if s.Acknowledged {
			acknowledgedEffects = 1
		}
		r.Orphans += max(0, s.Effects-acknowledgedEffects)
		if s.Acknowledged && s.Effects == 0 {
			r.FalseCompletion++
		}
		r.Uncertain = r.Uncertain || s.Uncertain
	}
}
func Wilson(k, n int) (float64, float64) {
	if n == 0 {
		return math.NaN(), math.NaN()
	}
	z := 1.959963984540054
	p := float64(k) / float64(n)
	den := 1 + z*z/float64(n)
	mid := (p + z*z/(2*float64(n))) / den
	half := z * math.Sqrt(p*(1-p)/float64(n)+z*z/(4*float64(n*n))) / den
	lo, hi := math.Max(0, mid-half), math.Min(1, mid+half)
	if k == 0 {
		lo = 0
	}
	if k == n {
		hi = 1
	}
	return lo, hi
}
func quantile(v []float64, q float64) float64 {
	if len(v) == 0 {
		return math.NaN()
	}
	slices.Sort(v)
	i := q * float64(len(v)-1)
	lo := int(i)
	hi := int(math.Ceil(i))
	return v[lo] + (v[hi]-v[lo])*(i-float64(lo))
}
func Bootstrap(values []float64, q float64, seed int64) (float64, float64, float64) {
	point := quantile(slices.Clone(values), q)
	if len(values) < 2 {
		return point, math.NaN(), math.NaN()
	}
	rng := rand.New(rand.NewSource(seed))
	samples := make([]float64, 2000)
	v := make([]float64, len(values))
	for i := range samples {
		for j := range v {
			v[j] = values[rng.Intn(len(values))]
		}
		samples[i] = quantile(v, q)
	}
	return point, quantile(slices.Clone(samples), .025), quantile(samples, .975)
}

// CheckAssertions enforces the safety invariants a validation configuration
// declares. Violations are reported per cell so a regression names the exact
// strategy, capability profile, and fault scenario that produced extra effects.
func CheckAssertions(c Config, results []Result) error {
	if len(c.AssertNoDuplicateEffects) == 0 {
		return nil
	}
	var violations []string
	observed := map[string]bool{}
	for _, r := range results {
		if r.Status != "completed" || !slices.Contains(c.AssertNoDuplicateEffects, r.Strategy) {
			continue
		}
		// Duplicated delivery creates a second effect inside the service itself.
		// No client-side protocol prevents that, so it is measured, not asserted.
		if r.Scenario == "duplicate_delivery" {
			continue
		}
		observed[r.Strategy] = true
		if r.Duplicate || r.ExcessEffects > 0 {
			violations = append(violations, fmt.Sprintf("%s execution=%s excess_effects=%d", r.Cell, r.ExecutionID, r.ExcessEffects))
		}
	}
	for _, strategy := range c.AssertNoDuplicateEffects {
		if !observed[strategy] {
			violations = append(violations, strategy+" produced no completed trials to assert on")
		}
	}
	if len(violations) > 0 {
		return fmt.Errorf("duplicate-effect assertion failed: %s", strings.Join(violations, "; "))
	}
	return nil
}
