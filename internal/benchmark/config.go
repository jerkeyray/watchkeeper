// Package benchmark runs reproducible, process-crash experiments against real
// Watchkeeper HTTP handlers and independent PostgreSQL effect storage.
package benchmark

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"slices"
	"time"

	"github.com/jerkeyray/watchkeeper/internal/api"
)

var Strategies = []string{"blind_retry", "idempotency_key_retry", "checkpoint_recovery", "watchkeeper"}
var Profiles = []string{"receipt_status", "idempotency_only", "indirect_unique", "none"}
var Scenarios = []string{"none", "before_intent", "before_dispatch", "accepted_before_commit", "after_commit", "after_response", "after_persistence", "lost_response", "duplicate_delivery", "delayed_commit", "service_unavailable", "database_unavailable", "coordinator_before_result", "coordinator_after_result"}

type Config struct {
	Version     int      `json:"version"`
	Strategies  []string `json:"strategies"`
	Profiles    []string `json:"profiles"`
	Scenarios   []string `json:"scenarios"`
	Repetitions int      `json:"repetitions"`
	Seed        int64    `json:"seed"`
	DeadlineMS  int      `json:"deadline_ms"`
	OutageMS    int      `json:"outage_ms"`
	DelayMS     int      `json:"delay_ms"`
	DrainMS     int      `json:"drain_ms"`
	// PreparedGraceMS is the ordinary recovery age threshold measured from
	// preparation or retry start. Explicit reconciliation scheduling can
	// override it. Experiments configure and retain it as a tuning parameter.
	PreparedGraceMS int `json:"prepared_grace_ms"`
	// AssertNoDuplicateEffects names strategies that a validation run requires to
	// produce no duplicate or excess effects. Research configurations leave it
	// empty so that measured duplication is recorded rather than assumed away.
	AssertNoDuplicateEffects []string `json:"assert_no_duplicate_effects"`
}

func LoadConfig(path string) (Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	var c Config
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&c); err != nil {
		return c, err
	}
	if err = decoder.Decode(&struct{}{}); err != io.EOF {
		return c, fmt.Errorf("configuration must contain one JSON object")
	}
	if c.Version != 1 {
		return c, fmt.Errorf("unsupported config version %d", c.Version)
	}
	if len(c.Strategies) == 0 {
		c.Strategies = Strategies
	}
	if len(c.Profiles) == 0 {
		c.Profiles = Profiles
	}
	if len(c.Scenarios) == 0 {
		c.Scenarios = Scenarios
	}
	if c.Repetitions == 0 {
		c.Repetitions = 100
	}
	if c.Seed == 0 {
		c.Seed = 1
	}
	if c.DeadlineMS == 0 {
		c.DeadlineMS = 60000
	}
	if c.OutageMS == 0 {
		c.OutageMS = 2000
	}
	if c.DelayMS == 0 {
		c.DelayMS = 5000
	}
	if c.DrainMS == 0 {
		c.DrainMS = 10000
	}
	if c.PreparedGraceMS == 0 {
		c.PreparedGraceMS = int(api.DefaultPreparedGrace / time.Millisecond)
	}
	if c.PreparedGraceMS < 1 || c.PreparedGraceMS > c.DeadlineMS {
		return c, fmt.Errorf("prepared_grace_ms must be positive and below the execution deadline")
	}
	if c.Repetitions < 1 || c.DeadlineMS < 1 || c.OutageMS < 1 || c.DelayMS < 1 || c.DrainMS < 1 {
		return c, fmt.Errorf("durations and repetitions must be positive")
	}
	for _, pair := range []struct{ got, want []string }{{c.Strategies, Strategies}, {c.Profiles, Profiles}, {c.Scenarios, Scenarios}, {c.AssertNoDuplicateEffects, c.Strategies}} {
		seen := map[string]bool{}
		for _, s := range pair.got {
			if !slices.Contains(pair.want, s) || seen[s] {
				return c, fmt.Errorf("unknown or repeated matrix value %q", s)
			}
			seen[s] = true
		}
	}
	if !slices.Contains(c.Scenarios, "none") {
		c.Scenarios = append([]string{"none"}, c.Scenarios...)
	}
	return c, nil
}
func (c Config) Hash() string {
	raw, _ := json.Marshal(c)
	return fmt.Sprintf("%x", sha256.Sum256(raw))
}
func Supported(strategy, profile, scenario string) bool {
	if strategy == "idempotency_key_retry" && profile != "idempotency_only" {
		return false
	}
	if (scenario == "coordinator_before_result" || scenario == "coordinator_after_result") && strategy != "watchkeeper" {
		return false
	}
	if scenario == "database_unavailable" && (strategy == "blind_retry" || strategy == "idempotency_key_retry") {
		return false
	}
	return true
}
