package benchmark

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestConfigDefaultsAndControls(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	os.WriteFile(p, []byte(`{"version":1,"scenarios":["after_commit"]}`), 0600)
	c, err := LoadConfig(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Repetitions != 100 || c.DeadlineMS != 60000 || !slices.Contains(c.Scenarios, "none") {
		t.Fatalf("%+v", c)
	}
}
func TestConfigRejectsTyposAndDuplicates(t *testing.T) {
	for _, raw := range []string{`{"version":1,"repetitons":2}`, `{"version":1,"profiles":["none","none"]}`, `{"version":2}`, `{"version":1,"repetitions":-1}`} {
		p := filepath.Join(t.TempDir(), "config.json")
		os.WriteFile(p, []byte(raw), 0600)
		if _, err := LoadConfig(p); err == nil {
			t.Fatal(raw)
		}
	}
}
