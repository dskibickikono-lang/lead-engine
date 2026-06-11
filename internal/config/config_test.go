package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.toml")
	os.WriteFile(p, []byte(`
db_path = "/tmp/leads.db"
score_threshold = 60

[bizraport]
email = "x@y.z"
password = "secret"
daily_cap_pln = 10.0
cost_per_row_pln = 0.5
max_candidates = 5

[signal]
api_url = "http://localhost:8080"
number = "+48111222333"
group_id = "group.abc"
`), 0o644)

	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.DBPath != "/tmp/leads.db" {
		t.Errorf("DBPath = %q", cfg.DBPath)
	}
	if cfg.ScoreThreshold != 60 {
		t.Errorf("ScoreThreshold = %d", cfg.ScoreThreshold)
	}
	if cfg.SuppressionDays != 30 { // default
		t.Errorf("SuppressionDays default = %d", cfg.SuppressionDays)
	}
	if got := cfg.ExcludedPKDPrefixes; len(got) != 2 || got[0] != "77" || got[1] != "78" {
		t.Errorf("ExcludedPKDPrefixes default = %v", got)
	}
	if cfg.Bizraport.DailyCapPLN != 10.0 {
		t.Errorf("DailyCapPLN = %v", cfg.Bizraport.DailyCapPLN)
	}
}

func TestLoadRequiresDBPath(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.toml")
	os.WriteFile(p, []byte(`score_threshold = 1`), 0o644)
	if _, err := Load(p); err == nil {
		t.Fatal("expected error for missing db_path")
	}
}
