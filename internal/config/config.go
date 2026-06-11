package config

import (
	"fmt"
	"os"

	"github.com/BurntSushi/toml"
)

type Scrapers struct {
	GovCmd    []string `toml:"gov_cmd"`    // e.g. ["/opt/gov_api/venv/bin/python", "/opt/gov_api/main.py"]
	GovExport string   `toml:"gov_export"` // path to raw-leads-cbop-latest.json
	OlxCmd    []string `toml:"olx_cmd"`
	OlxExport string   `toml:"olx_export"`
}

type Bizraport struct {
	Email         string  `toml:"email"`
	Password      string  `toml:"password"`
	DailyCapPLN   float64 `toml:"daily_cap_pln"`
	CostPerRowPLN float64 `toml:"cost_per_row_pln"`
	MaxCandidates int     `toml:"max_candidates"`
}

type Regon struct {
	APIKey   string `toml:"api_key"`
	Endpoint string `toml:"endpoint"` // empty = production default
}

type Signal struct {
	APIURL  string `toml:"api_url"`
	Number  string `toml:"number"`
	GroupID string `toml:"group_id"`
}

type Pipedrive struct {
	APIToken  string            `toml:"api_token"`
	BaseURL   string            `toml:"base_url"` // empty = https://api.pipedrive.com
	StageID   int64             `toml:"stage_id"`
	FieldKeys map[string]string `toml:"field_keys"` // nip, regon, krs, pkd, board_members, source
}

type Config struct {
	DBPath              string    `toml:"db_path"`
	SuppressionDays     int       `toml:"suppression_days"`
	ScoreThreshold      int       `toml:"score_threshold"`
	ExcludedPKDPrefixes []string  `toml:"excluded_pkd_prefixes"`
	Scrapers            Scrapers  `toml:"scrapers"`
	Bizraport           Bizraport `toml:"bizraport"`
	Regon               Regon     `toml:"regon"`
	Signal              Signal    `toml:"signal"`
	Pipedrive           Pipedrive `toml:"pipedrive"`
}

func Load(path string) (*Config, error) {
	cfg := &Config{
		SuppressionDays:     30,
		ScoreThreshold:      50,
		ExcludedPKDPrefixes: []string{"77", "78"},
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	if err := toml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if cfg.DBPath == "" {
		return nil, fmt.Errorf("config: db_path is required")
	}
	if cfg.Bizraport.Email != "" || cfg.Bizraport.Password != "" {
		if cfg.Bizraport.DailyCapPLN <= 0 || cfg.Bizraport.CostPerRowPLN <= 0 {
			return nil, fmt.Errorf("config: bizraport credentials set but daily_cap_pln/cost_per_row_pln missing — the daily cap would be disabled")
		}
	}
	if cfg.Bizraport.MaxCandidates == 0 {
		cfg.Bizraport.MaxCandidates = 5
	}
	return cfg, nil
}
