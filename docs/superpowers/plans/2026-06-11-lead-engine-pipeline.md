# Lead Engine Unified Pipeline Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the `lead-engine` Go orchestrator that ingests raw-leads JSON from the gov_api and OLX scrapers into a unified SQLite DB, resolves/enriches company identities (BizRaport → KRS → REGON), deduplicates, and delivers a daily Signal digest plus Pipedrive pushes — runnable from a single cron entry.

**Architecture:** Per the approved spec (`docs/superpowers/specs/2026-06-11-lead-generator-design.md`): polyglot scrapers stay untouched except additive JSON exporters; a new Go binary owns `leads.db`, all post-merge enrichment, dedup, and delivery. Stages are idempotent and recorded in a `runs`/`run_stages` table for `--resume`.

**Tech Stack:** Go 1.22+, `modernc.org/sqlite` (CGO-free, cross-compiles for the VPS), `github.com/BurntSushi/toml`, `github.com/spf13/cobra`, stdlib `net/http` + `httptest` fakes. Python 3.12/pytest for the gov_api exporter task.

**Repos touched:**
- `~/projects/lead-engine` (new, this repo) — Tasks 1–5, 8–18
- `~/projects/gov_api` (Python) — Task 6
- `~/projects/printing-press/olx` (Go) — Task 7

**Phases (each ends with working software):**
- Phase 1 (Tasks 1–7): scaffold + store + contract + ingest + match; both scrapers export the contract. Verifiable: fixture exports land in a unified DB with merged companies.
- Phase 2 (Tasks 8–12): NIP resolution + enrichment with spend cap and caches.
- Phase 3 (Tasks 13–18): qualification, digest, Signal, Pipedrive, runner, CLI, deploy docs.

---

## Phase 1 — Foundation & ingestion

### Task 1: Scaffold lead-engine repo + config loader

**Files:**
- Create: `go.mod`, `Makefile`, `.gitignore`
- Create: `internal/config/config.go`
- Test: `internal/config/config_test.go`

- [ ] **Step 1: Initialize module and tooling**

```bash
cd ~/projects/lead-engine
go mod init github.com/hrkono/lead-engine
go get github.com/BurntSushi/toml@latest
```

Create `.gitignore`:

```
bin/
*.db
*.db-wal
*.db-shm
/data/
```

Create `Makefile`:

```makefile
.PHONY: build test
build:
	go build -o bin/lead-engine ./cmd/lead-engine
test:
	go test ./...
```

(`cmd/lead-engine` does not exist until Task 18; `make test` is the command used until then.)

- [ ] **Step 2: Write the failing config test**

`internal/config/config_test.go`:

```go
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
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./internal/config/ -v`
Expected: FAIL — `Load` undefined.

- [ ] **Step 4: Implement config loader**

`internal/config/config.go`:

```go
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
	if cfg.Bizraport.MaxCandidates == 0 {
		cfg.Bizraport.MaxCandidates = 5
	}
	return cfg, nil
}
```

- [ ] **Step 5: Run tests, verify pass**

Run: `go test ./internal/config/ -v`
Expected: PASS (2 tests).

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum Makefile .gitignore internal/config/
git commit -m "feat: scaffold lead-engine with TOML config loader"
```

---

### Task 2: Unified store — schema & open

**Files:**
- Create: `internal/store/store.go`
- Test: `internal/store/store_test.go`

- [ ] **Step 1: Write the failing test**

`internal/store/store_test.go`:

```go
package store

import (
	"path/filepath"
	"testing"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "leads.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestOpenCreatesSchema(t *testing.T) {
	st := openTest(t)
	for _, table := range []string{"raw_offers", "companies", "leads", "deliveries", "api_cache", "spend_log", "runs", "run_stages"} {
		var n int
		err := st.DB.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&n)
		if err != nil || n != 1 {
			t.Errorf("table %s missing (n=%d err=%v)", table, n, err)
		}
	}
}

func TestOpenIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "leads.db")
	st1, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	st1.Close()
	st2, err := Open(p)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	st2.Close()
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go get modernc.org/sqlite@latest && go test ./internal/store/ -v`
Expected: FAIL — `Open` undefined.

- [ ] **Step 3: Implement the store**

`internal/store/store.go`:

```go
// Package store owns leads.db — the unified lead store.
package store

import (
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS raw_offers (
  source       TEXT NOT NULL,
  external_id  TEXT NOT NULL,
  nip          TEXT,
  company_name TEXT NOT NULL,
  position     TEXT,
  location     TEXT,
  vacancies    INTEGER,
  salary_from  REAL,
  salary_to    REAL,
  phone        TEXT,
  email        TEXT,
  score        INTEGER,
  scraped_at   TEXT,
  ingested_at  TEXT NOT NULL,
  company_id   INTEGER REFERENCES companies(id),
  payload      TEXT,
  PRIMARY KEY (source, external_id)
);
CREATE INDEX IF NOT EXISTS idx_raw_offers_company ON raw_offers(company_id);
CREATE INDEX IF NOT EXISTS idx_raw_offers_nip ON raw_offers(nip);

CREATE TABLE IF NOT EXISTS companies (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  nip             TEXT UNIQUE,
  name            TEXT NOT NULL,
  normalized_name TEXT NOT NULL,
  nip_status      TEXT NOT NULL DEFAULT 'pending', -- pending | verified | unresolved
  address         TEXT NOT NULL DEFAULT '',
  regon           TEXT NOT NULL DEFAULT '',
  krs             TEXT NOT NULL DEFAULT '',
  legal_form      TEXT NOT NULL DEFAULT '',
  pkd_main        TEXT NOT NULL DEFAULT '',
  company_size    TEXT NOT NULL DEFAULT '',
  website         TEXT NOT NULL DEFAULT '',
  email           TEXT NOT NULL DEFAULT '',
  phone           TEXT NOT NULL DEFAULT '',
  board_members   TEXT NOT NULL DEFAULT '', -- JSON array [{"name":..,"role":..}]
  first_seen      TEXT NOT NULL,
  last_seen       TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_companies_normname ON companies(normalized_name);

CREATE TABLE IF NOT EXISTS leads (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  company_id INTEGER NOT NULL REFERENCES companies(id),
  run_id     INTEGER NOT NULL,
  positions  TEXT NOT NULL, -- JSON array of strings
  score      INTEGER,       -- NULL for OLX-only leads
  qualified  INTEGER NOT NULL DEFAULT 0,
  status     TEXT NOT NULL DEFAULT 'new', -- new | delivered | suppressed
  reason     TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_leads_company ON leads(company_id);

CREATE TABLE IF NOT EXISTS deliveries (
  id                INTEGER PRIMARY KEY AUTOINCREMENT,
  lead_id           INTEGER NOT NULL REFERENCES leads(id),
  channel           TEXT NOT NULL, -- signal | pipedrive
  delivered_at      TEXT NOT NULL,
  pipedrive_org_id  INTEGER,
  pipedrive_deal_id INTEGER,
  status            TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_deliveries_lead ON deliveries(lead_id);

CREATE TABLE IF NOT EXISTS api_cache (
  api        TEXT NOT NULL,
  identifier TEXT NOT NULL,
  payload    TEXT NOT NULL,
  fetched_at TEXT NOT NULL,
  PRIMARY KEY (api, identifier)
);

CREATE TABLE IF NOT EXISTS spend_log (
  day TEXT NOT NULL,
  api TEXT NOT NULL,
  pln REAL NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_spend_day ON spend_log(day, api);

CREATE TABLE IF NOT EXISTS runs (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  started_at  TEXT NOT NULL,
  finished_at TEXT,
  status      TEXT NOT NULL DEFAULT 'running' -- running | ok | failed
);

CREATE TABLE IF NOT EXISTS run_stages (
  run_id   INTEGER NOT NULL REFERENCES runs(id),
  stage    TEXT NOT NULL,
  status   TEXT NOT NULL, -- ok | failed | skipped
  detail   TEXT NOT NULL DEFAULT '',
  ended_at TEXT NOT NULL,
  PRIMARY KEY (run_id, stage)
);
`

type Store struct {
	DB *sql.DB
}

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("init schema: %w", err)
	}
	return &Store{DB: db}, nil
}

func (s *Store) Close() error { return s.DB.Close() }
```

- [ ] **Step 4: Run tests, verify pass**

Run: `go test ./internal/store/ -v`
Expected: PASS (2 tests).

- [ ] **Step 5: Commit**

```bash
git add internal/store/ go.mod go.sum
git commit -m "feat: unified leads.db schema and store"
```

---

### Task 3: Raw-leads JSON contract (v1) + fixtures

**Files:**
- Create: `internal/contract/contract.go`
- Create: `internal/contract/testdata/raw-leads-cbop.json`
- Create: `internal/contract/testdata/raw-leads-olx.json`
- Test: `internal/contract/contract_test.go`

- [ ] **Step 1: Create the fixtures (these double as the contract's reference documents)**

`internal/contract/testdata/raw-leads-cbop.json`:

```json
{
  "contractVersion": 1,
  "source": "cbop",
  "exportedAt": "2026-06-10T05:10:00Z",
  "offers": [
    {
      "externalId": "cbop:abc123",
      "nip": "1234567890",
      "companyName": "Stalmet Sp. z o.o.",
      "position": "Operator maszyn CNC",
      "location": "Warszawa",
      "vacancies": 8,
      "salaryFrom": 4500,
      "salaryTo": 6000,
      "phone": "+48221112233",
      "email": "hr@stalmet.example",
      "score": 98,
      "scrapedAt": "2026-06-10T05:00:00Z",
      "extra": {"qualified": true, "regon": {"pkdMain": "29.10.Z"}}
    }
  ]
}
```

`internal/contract/testdata/raw-leads-olx.json`:

```json
{
  "contractVersion": 1,
  "source": "olx",
  "exportedAt": "2026-06-10T05:20:00Z",
  "offers": [
    {
      "externalId": "olx:987654",
      "nip": null,
      "companyName": "Stalmet sp. z o.o.",
      "position": "Operator wtryskarki",
      "location": "Warszawa",
      "vacancies": 1,
      "salaryFrom": null,
      "salaryTo": null,
      "phone": "+48501502503",
      "email": null,
      "score": null,
      "scrapedAt": "2026-06-10T04:55:00Z",
      "extra": {"jobCount": 4}
    }
  ]
}
```

- [ ] **Step 2: Write the failing test**

`internal/contract/contract_test.go`:

```go
package contract

import (
	"os"
	"testing"
)

func TestParseFixtures(t *testing.T) {
	for _, name := range []string{"testdata/raw-leads-cbop.json", "testdata/raw-leads-olx.json"} {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		f, err := Parse(data)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(f.Offers) != 1 {
			t.Errorf("%s: offers = %d", name, len(f.Offers))
		}
	}
}

func TestParseRejectsBadVersionAndSource(t *testing.T) {
	if _, err := Parse([]byte(`{"contractVersion":2,"source":"cbop","offers":[]}`)); err == nil {
		t.Error("version 2 accepted")
	}
	if _, err := Parse([]byte(`{"contractVersion":1,"source":"linkedin","offers":[]}`)); err == nil {
		t.Error("unknown source accepted")
	}
}

func TestNullFieldsParse(t *testing.T) {
	data, _ := os.ReadFile("testdata/raw-leads-olx.json")
	f, _ := Parse(data)
	o := f.Offers[0]
	if o.NIP != "" || o.Score != nil || o.SalaryFrom != nil {
		t.Errorf("null handling: nip=%q score=%v salaryFrom=%v", o.NIP, o.Score, o.SalaryFrom)
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./internal/contract/ -v`
Expected: FAIL — `Parse` undefined.

- [ ] **Step 4: Implement the contract package**

`internal/contract/contract.go`:

```go
// Package contract defines the raw-leads JSON interchange format (v1)
// produced by both scrapers and consumed by lead-engine. The fixtures in
// testdata/ are the normative examples; scraper exporter tests assert
// against the same shapes.
package contract

import (
	"encoding/json"
	"fmt"
)

type File struct {
	ContractVersion int     `json:"contractVersion"`
	Source          string  `json:"source"` // "cbop" | "olx"
	ExportedAt      string  `json:"exportedAt"`
	Offers          []Offer `json:"offers"`
}

type Offer struct {
	ExternalID  string         `json:"externalId"`
	NIP         string         `json:"nip"` // "" when unknown (JSON null)
	CompanyName string         `json:"companyName"`
	Position    string         `json:"position"`
	Location    string         `json:"location"`
	Vacancies   int            `json:"vacancies"`
	SalaryFrom  *float64       `json:"salaryFrom"`
	SalaryTo    *float64       `json:"salaryTo"`
	Phone       string         `json:"phone"`
	Email       string         `json:"email"`
	Score       *int           `json:"score"`
	ScrapedAt   string         `json:"scrapedAt"`
	Extra       map[string]any `json:"extra"`
}

func Parse(data []byte) (*File, error) {
	var f File
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("contract: %w", err)
	}
	if f.ContractVersion != 1 {
		return nil, fmt.Errorf("contract: unsupported contractVersion %d", f.ContractVersion)
	}
	if f.Source != "cbop" && f.Source != "olx" {
		return nil, fmt.Errorf("contract: unknown source %q", f.Source)
	}
	for i, o := range f.Offers {
		if o.ExternalID == "" {
			return nil, fmt.Errorf("contract: offers[%d] missing externalId", i)
		}
		if o.CompanyName == "" {
			return nil, fmt.Errorf("contract: offers[%d] missing companyName", i)
		}
	}
	return &f, nil
}
```

- [ ] **Step 5: Run tests, verify pass**

Run: `go test ./internal/contract/ -v`
Expected: PASS (3 tests).

- [ ] **Step 6: Commit**

```bash
git add internal/contract/
git commit -m "feat: raw-leads v1 contract with normative fixtures"
```

---

### Task 4: Ingest stage — contract file → raw_offers

**Files:**
- Create: `internal/store/offers.go`
- Create: `internal/ingest/ingest.go`
- Test: `internal/ingest/ingest_test.go`

- [ ] **Step 1: Write the failing test**

`internal/ingest/ingest_test.go`:

```go
package ingest

import (
	"path/filepath"
	"testing"

	"github.com/hrkono/lead-engine/internal/store"
)

func TestIngestIsIdempotent(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "leads.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	fixture := "../contract/testdata/raw-leads-cbop.json"
	n, err := Ingest(st, fixture)
	if err != nil {
		t.Fatalf("first ingest: %v", err)
	}
	if n != 1 {
		t.Errorf("first ingest n = %d", n)
	}
	if _, err := Ingest(st, fixture); err != nil {
		t.Fatalf("second ingest: %v", err)
	}
	var count int
	st.DB.QueryRow(`SELECT COUNT(*) FROM raw_offers`).Scan(&count)
	if count != 1 {
		t.Errorf("raw_offers rows = %d, want 1 (upsert)", count)
	}
	var nip string
	st.DB.QueryRow(`SELECT nip FROM raw_offers WHERE external_id='cbop:abc123'`).Scan(&nip)
	if nip != "1234567890" {
		t.Errorf("nip = %q", nip)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/ingest/ -v`
Expected: FAIL — package/function undefined.

- [ ] **Step 3: Implement upsert + ingest**

`internal/store/offers.go`:

```go
package store

import "database/sql"

type RawOffer struct {
	Source      string
	ExternalID  string
	NIP         string
	CompanyName string
	Position    string
	Location    string
	Vacancies   int
	SalaryFrom  *float64
	SalaryTo    *float64
	Phone       string
	Email       string
	Score       *int
	ScrapedAt   string
	Payload     string
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func (s *Store) UpsertRawOffer(o RawOffer) error {
	_, err := s.DB.Exec(`INSERT INTO raw_offers
		(source, external_id, nip, company_name, position, location, vacancies,
		 salary_from, salary_to, phone, email, score, scraped_at, ingested_at, payload)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,datetime('now'),?)
		ON CONFLICT(source, external_id) DO UPDATE SET
		 nip=excluded.nip, company_name=excluded.company_name,
		 position=excluded.position, location=excluded.location,
		 vacancies=excluded.vacancies, salary_from=excluded.salary_from,
		 salary_to=excluded.salary_to, phone=excluded.phone,
		 email=excluded.email, score=excluded.score,
		 scraped_at=excluded.scraped_at, payload=excluded.payload`,
		o.Source, o.ExternalID, nullIfEmpty(o.NIP), o.CompanyName, o.Position,
		o.Location, o.Vacancies, o.SalaryFrom, o.SalaryTo, o.Phone, o.Email,
		o.Score, o.ScrapedAt, o.Payload)
	return err
}

// scanNullStr reads a nullable TEXT column into "".
func scanNullStr(ns sql.NullString) string {
	if ns.Valid {
		return ns.String
	}
	return ""
}
```

`internal/ingest/ingest.go`:

```go
// Package ingest loads raw-leads contract files into raw_offers.
package ingest

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/hrkono/lead-engine/internal/contract"
	"github.com/hrkono/lead-engine/internal/store"
)

func Ingest(st *store.Store, path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("ingest %s: %w", path, err)
	}
	f, err := contract.Parse(data)
	if err != nil {
		return 0, fmt.Errorf("ingest %s: %w", path, err)
	}
	n := 0
	for _, o := range f.Offers {
		payload, _ := json.Marshal(o)
		err := st.UpsertRawOffer(store.RawOffer{
			Source:      f.Source,
			ExternalID:  o.ExternalID,
			NIP:         o.NIP,
			CompanyName: o.CompanyName,
			Position:    o.Position,
			Location:    o.Location,
			Vacancies:   o.Vacancies,
			SalaryFrom:  o.SalaryFrom,
			SalaryTo:    o.SalaryTo,
			Phone:       o.Phone,
			Email:       o.Email,
			Score:       o.Score,
			ScrapedAt:   o.ScrapedAt,
			Payload:     string(payload),
		})
		if err != nil {
			return n, fmt.Errorf("ingest %s offer %s: %w", path, o.ExternalID, err)
		}
		n++
	}
	return n, nil
}
```

- [ ] **Step 4: Run tests, verify pass**

Run: `go test ./internal/ingest/ ./internal/store/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/store/offers.go internal/ingest/
git commit -m "feat: idempotent ingest of raw-leads contract files"
```

---

### Task 5: Match & merge — offers → companies

**Files:**
- Create: `internal/match/normalize.go`
- Create: `internal/match/match.go`
- Create: `internal/store/companies.go`
- Test: `internal/match/normalize_test.go`, `internal/match/match_test.go`

- [ ] **Step 1: Write the failing normalize test**

`internal/match/normalize_test.go`:

```go
package match

import "testing"

func TestNormalize(t *testing.T) {
	cases := map[string]string{
		"Stalmet Sp. z o.o.":          "stalmet",
		"STALMET spółka z ograniczoną odpowiedzialnością": "stalmet",
		"Żółć  S.A.":                  "zolc",
		"Kowalski sp.j.":              "kowalski",
		"ABC-Produkcja Sp. K.":        "abc produkcja",
	}
	for in, want := range cases {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/match/ -v`
Expected: FAIL — `Normalize` undefined.

- [ ] **Step 3: Implement Normalize**

`internal/match/normalize.go` (same approach as the olx module's `normalizeCompanyName` — lowercase, fold Polish diacritics, strip punctuation and legal-form suffixes, collapse spaces):

```go
package match

import "strings"

var polishFold = strings.NewReplacer(
	"ą", "a", "ć", "c", "ę", "e", "ł", "l", "ń", "n",
	"ó", "o", "ś", "s", "ź", "z", "ż", "z",
)

// Longest suffixes first so "spolka z ograniczona odpowiedzialnoscia"
// is removed before "spolka".
var legalSuffixes = []string{
	"spolka z ograniczona odpowiedzialnoscia spolka komandytowa",
	"spolka z ograniczona odpowiedzialnoscia",
	"spolka komandytowo akcyjna",
	"spolka komandytowa",
	"spolka akcyjna",
	"spolka jawna",
	"spolka cywilna",
	"sp z o o sp k",
	"sp z o o",
	"sp k",
	"sp j",
	"s c",
	"s a",
	"z o o",
}

func Normalize(name string) string {
	s := strings.ToLower(name)
	s = polishFold.Replace(s)
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteRune(' ')
		}
	}
	s = strings.Join(strings.Fields(b.String()), " ")
	for changed := true; changed; {
		changed = false
		for _, suf := range legalSuffixes {
			if strings.HasSuffix(s, " "+suf) || s == suf {
				s = strings.TrimSpace(strings.TrimSuffix(s, suf))
				changed = true
			}
		}
	}
	return s
}
```

- [ ] **Step 4: Run normalize test, verify pass**

Run: `go test ./internal/match/ -v -run TestNormalize`
Expected: PASS.

- [ ] **Step 5: Write the failing match test**

`internal/match/match_test.go`:

```go
package match

import (
	"path/filepath"
	"testing"

	"github.com/hrkono/lead-engine/internal/ingest"
	"github.com/hrkono/lead-engine/internal/store"
)

func TestAttachMergesByNIPAndCreatesProvisional(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "leads.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := ingest.Ingest(st, "../contract/testdata/raw-leads-cbop.json"); err != nil {
		t.Fatal(err)
	}
	if _, err := ingest.Ingest(st, "../contract/testdata/raw-leads-olx.json"); err != nil {
		t.Fatal(err)
	}

	stats, err := Attach(st)
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if stats.Attached != 2 {
		t.Errorf("Attached = %d, want 2", stats.Attached)
	}

	// cbop offer has a NIP -> verified company; olx offer has none but the
	// SAME normalized name -> attaches to the same company row.
	var companies int
	st.DB.QueryRow(`SELECT COUNT(*) FROM companies`).Scan(&companies)
	if companies != 1 {
		t.Fatalf("companies = %d, want 1 (name match should reuse the NIP row)", companies)
	}
	var status string
	st.DB.QueryRow(`SELECT nip_status FROM companies`).Scan(&status)
	if status != "verified" {
		t.Errorf("nip_status = %q", status)
	}

	// Re-running Attach must be a no-op.
	stats2, _ := Attach(st)
	if stats2.Attached != 0 {
		t.Errorf("second Attach attached %d", stats2.Attached)
	}
}
```

- [ ] **Step 6: Run test to verify it fails**

Run: `go test ./internal/match/ -v -run TestAttach`
Expected: FAIL — `Attach` undefined.

- [ ] **Step 7: Implement company store functions + Attach**

`internal/store/companies.go`:

```go
package store

import (
	"database/sql"
	"errors"
	"fmt"
)

type Company struct {
	ID             int64
	NIP            string
	Name           string
	NormalizedName string
	NIPStatus      string
	Address        string
	REGON          string
	KRS            string
	LegalForm      string
	PKDMain        string
	CompanySize    string
	Website        string
	Email          string
	Phone          string
	BoardMembers   string
	FirstSeen      string
	LastSeen       string
}

const companyCols = `id, COALESCE(nip,''), name, normalized_name, nip_status,
	address, regon, krs, legal_form, pkd_main, company_size, website, email,
	phone, board_members, first_seen, last_seen`

func scanCompany(row *sql.Row) (*Company, error) {
	var c Company
	err := row.Scan(&c.ID, &c.NIP, &c.Name, &c.NormalizedName, &c.NIPStatus,
		&c.Address, &c.REGON, &c.KRS, &c.LegalForm, &c.PKDMain, &c.CompanySize,
		&c.Website, &c.Email, &c.Phone, &c.BoardMembers, &c.FirstSeen, &c.LastSeen)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func (s *Store) FindCompanyByNIP(nip string) (*Company, error) {
	return scanCompany(s.DB.QueryRow(
		`SELECT `+companyCols+` FROM companies WHERE nip = ?`, nip))
}

func (s *Store) FindCompanyByNormalizedName(norm string) (*Company, error) {
	return scanCompany(s.DB.QueryRow(
		`SELECT `+companyCols+` FROM companies WHERE normalized_name = ? ORDER BY id LIMIT 1`, norm))
}

func (s *Store) CreateCompany(nip, name, norm, status string) (int64, error) {
	res, err := s.DB.Exec(`INSERT INTO companies
		(nip, name, normalized_name, nip_status, first_seen, last_seen)
		VALUES (?,?,?,?,datetime('now'),datetime('now'))`,
		nullIfEmpty(nip), name, norm, status)
	if err != nil {
		return 0, fmt.Errorf("create company %q: %w", name, err)
	}
	return res.LastInsertId()
}

func (s *Store) AttachOffer(source, externalID string, companyID int64) error {
	_, err := s.DB.Exec(`UPDATE raw_offers SET company_id = ?
		WHERE source = ? AND external_id = ?`, companyID, source, externalID)
	return err
}

func (s *Store) TouchCompany(id int64) error {
	_, err := s.DB.Exec(`UPDATE companies SET last_seen = datetime('now') WHERE id = ?`, id)
	return err
}

func (s *Store) UnattachedOffers() ([]RawOffer, error) {
	rows, err := s.DB.Query(`SELECT source, external_id, COALESCE(nip,''),
		company_name FROM raw_offers WHERE company_id IS NULL ORDER BY source, external_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RawOffer
	for rows.Next() {
		var o RawOffer
		if err := rows.Scan(&o.Source, &o.ExternalID, &o.NIP, &o.CompanyName); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// MergeCompanies repoints everything from src onto dst and deletes src.
// Used when NIP resolution discovers a provisional company is an existing one.
func (s *Store) MergeCompanies(srcID, dstID int64) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE raw_offers SET company_id=? WHERE company_id=?`, dstID, srcID); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE leads SET company_id=? WHERE company_id=?`, dstID, srcID); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM companies WHERE id=?`, srcID); err != nil {
		return err
	}
	return tx.Commit()
}
```

`internal/match/match.go`:

```go
// Package match attaches raw offers to unified company rows.
package match

import (
	"fmt"

	"github.com/hrkono/lead-engine/internal/store"
)

type Stats struct {
	Attached int
}

func Attach(st *store.Store) (Stats, error) {
	var stats Stats
	offers, err := st.UnattachedOffers()
	if err != nil {
		return stats, fmt.Errorf("match: %w", err)
	}
	for _, o := range offers {
		norm := Normalize(o.CompanyName)
		var c *store.Company
		if o.NIP != "" {
			c, err = st.FindCompanyByNIP(o.NIP)
		} else {
			c, err = st.FindCompanyByNormalizedName(norm)
		}
		if err != nil {
			return stats, fmt.Errorf("match %s/%s: %w", o.Source, o.ExternalID, err)
		}
		if c == nil && o.NIP != "" {
			// A NIP-less provisional row with the same name may already exist.
			c, err = st.FindCompanyByNormalizedName(norm)
			if err != nil {
				return stats, err
			}
			if c != nil && c.NIP == "" {
				if _, err := st.DB.Exec(`UPDATE companies SET nip=?, nip_status='verified' WHERE id=?`, o.NIP, c.ID); err != nil {
					return stats, err
				}
			} else {
				c = nil
			}
		}
		if c == nil {
			status := "pending"
			if o.NIP != "" {
				status = "verified"
			}
			id, err := st.CreateCompany(o.NIP, o.CompanyName, norm, status)
			if err != nil {
				return stats, err
			}
			c = &store.Company{ID: id}
		}
		if err := st.AttachOffer(o.Source, o.ExternalID, c.ID); err != nil {
			return stats, err
		}
		if err := st.TouchCompany(c.ID); err != nil {
			return stats, err
		}
		stats.Attached++
	}
	return stats, nil
}
```

Note on ordering: `UnattachedOffers` orders `cbop` before `olx` (alphabetical), so NIP-bearing gov offers create the verified row before OLX name-matches against it — which is what the test asserts.

- [ ] **Step 8: Run all tests, verify pass**

Run: `go test ./... -v`
Expected: PASS across config, store, contract, ingest, match.

- [ ] **Step 9: Commit**

```bash
git add internal/match/ internal/store/companies.go
git commit -m "feat: company matching with NIP identity and provisional name rows"
```

---

### Task 6: gov_api raw-leads exporter (Python, additive)

**Files (in `~/projects/gov_api`):**
- Modify: `exporter.py` (append new function)
- Modify: `main.py` (build unfiltered export list + call)
- Test: `tests/test_exporter_raw_leads.py`

- [ ] **Step 1: Write the failing test**

`tests/test_exporter_raw_leads.py`:

```python
import json

from models import JobOffer
from exporter import export_raw_leads


def test_export_raw_leads_writes_contract_v1(tmp_path):
    offer = JobOffer(offer_id="abc123", nip="1234567890")
    leads = [(offer, None, {"total": 75}, "2026-06-10T05:00:00")]

    path = export_raw_leads(leads, tmp_path)

    payload = json.loads(path.read_text(encoding="utf-8"))
    assert payload["contractVersion"] == 1
    assert payload["source"] == "cbop"
    assert len(payload["offers"]) == 1
    o = payload["offers"][0]
    assert o["externalId"].endswith("abc123")
    assert o["nip"] == "1234567890"
    assert "companyName" in o and "position" in o and "scrapedAt" in o
    # stable filename for the orchestrator to pick up
    assert path.name == "raw-leads-cbop-latest.json"
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd ~/projects/gov_api && pytest tests/test_exporter_raw_leads.py -v`
Expected: FAIL — `export_raw_leads` not importable.

- [ ] **Step 3: Implement `export_raw_leads` in `exporter.py`**

Append to `exporter.py` (reuses the existing `_format_lead(offer, regon, bd, scraped_at)` helper so field mapping stays in one place; input tuples have the same shape as `export_to_json`):

```python
def export_raw_leads(leads: list, export_dir: Path) -> Path:
    """Export ALL scored offers (not only qualified) as a raw-leads v1
    contract file for the lead-engine orchestrator.

    leads: list of (offer, regon, breakdown_with_score, scraped_at) tuples,
    same shape as export_to_json input but unfiltered.
    """
    export_dir = Path(export_dir)
    export_dir.mkdir(parents=True, exist_ok=True)

    offers = []
    for offer, regon, bd, scraped_at in leads:
        lead = _format_lead(offer, regon, bd, scraped_at)
        oc = lead.get("offerContent") or {}
        offers.append({
            "externalId": f"cbop:{lead['externalId']}",
            "nip": lead.get("nip"),
            "companyName": lead.get("employerName") or "",
            "position": lead.get("offerTitle") or "",
            "location": lead.get("location") or "",
            "vacancies": lead.get("numberOfVacancies") or 1,
            "salaryFrom": oc.get("salaryFrom"),
            "salaryTo": oc.get("salaryTo"),
            "phone": getattr(regon, "telefon", None),
            "email": getattr(regon, "email", None),
            "score": lead.get("score"),
            "scrapedAt": lead.get("scrapedAt"),
            "extra": {
                "qualified": lead.get("qualified"),
                "regon": lead.get("regon"),
                "offerContent": oc,
            },
        })

    payload = {
        "contractVersion": 1,
        "source": "cbop",
        "exportedAt": datetime.now(timezone.utc).isoformat(),
        "offers": offers,
    }
    path = export_dir / "raw-leads-cbop-latest.json"
    path.write_text(
        json.dumps(payload, ensure_ascii=False, indent=2, default=str),
        encoding="utf-8",
    )
    logger.info(f"Raw-leads contract export: {path} ({len(offers)} offers)")
    return path
```

If `_format_lead` keys differ from the documented `gov_api_v2` output schema (`externalId`, `employerName`, `offerTitle`, `numberOfVacancies`, `location`, `score`, `qualified`, `scrapedAt`, `offerContent`, `regon` — see `docs/output-schema.md`), adjust the mapping to the actual keys returned; the test pins the contract side, `docs/output-schema.md` pins the source side. Ensure `datetime`/`timezone` are imported at the top of `exporter.py` (add `from datetime import datetime, timezone` if absent).

- [ ] **Step 4: Run test, verify pass**

Run: `pytest tests/test_exporter_raw_leads.py -v`
Expected: PASS.

- [ ] **Step 5: Wire into `main.py`**

In `main.py`, locate the loop that builds `qualified_export` (~line 341–350, comment "Build export tuples"). Add an unfiltered list alongside it and one call after the existing exports:

```python
            # Build export tuples: (offer, regon, breakdown+score+qualified, scraped_at)
            qualified_export = []
            raw_export = []  # ALL scored offers for the lead-engine contract
            for offer, bd, scraped_at in scored:          # match the existing loop variables
                raw_export.append((offer, regon_map.get(offer.nip), bd, scraped_at))
                if bd.get("qualified"):                    # keep the existing qualification condition
                    qualified_export.append((offer, regon_map.get(offer.nip), bd, scraped_at))

            if qualified_export:
                export_path = export_to_json(qualified_export, run_id, export_dir)
                export_to_csv(qualified_export, run_id, export_dir)
            if raw_export:
                export_raw_leads(raw_export, export_dir)
```

Keep the existing loop's actual variable names and qualification condition — only *add* `raw_export` collection and the `export_raw_leads` call; do not change the qualified path. Add `export_raw_leads` to the `from exporter import ...` line.

- [ ] **Step 6: Run the full gov_api suite + dry run**

Run: `pytest tests/ -v --tb=short && python main.py --dry-run --voivodeships 14 --max-offers 50`
Expected: all tests PASS; dry run completes without errors.

- [ ] **Step 7: Commit (in gov_api repo, on a branch per its workflow)**

```bash
cd ~/projects/gov_api
git checkout -b feat/raw-leads-export
git add exporter.py main.py tests/test_exporter_raw_leads.py
git commit -m "feat: export raw-leads v1 contract file for lead-engine"
```

---

### Task 7: OLX raw-leads exporter (Go, additive)

**Files (in `~/projects/printing-press/olx/src`):**
- Create: `internal/store/rawleads.go`
- Modify: `internal/cli/export.go` (new `--kind raw-leads`)
- Test: `internal/store/rawleads_test.go`

- [ ] **Step 1: Find the store test helper used by existing tests**

Run: `grep -rn "func openTest\|store.Open\|NewStore" ~/projects/printing-press/olx/src/internal --include="*_test.go" | head`
Use whatever helper the existing store tests use to open a temp store; the test below assumes a `openTestStore(t)`-style helper exists — adapt the two setup lines to the real one found here.

- [ ] **Step 2: Write the failing test**

`internal/store/rawleads_test.go` (setup lines adapted per Step 1):

```go
package store

import (
	"context"
	"testing"
	"time"
)

func TestRawLeadRows(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t) // adapt to the existing test helper

	mustExec(t, st, `INSERT INTO companies (id, name, nip, phone, first_seen, last_seen)
		VALUES ('olx:c1', 'Stalmet sp. z o.o.', '', '+48600700800', datetime('now'), datetime('now'))`)
	mustExec(t, st, `INSERT INTO jobs (id, url, title, location_city, company_id, fetched_at, raw_json)
		VALUES ('olx:j1', 'https://olx.pl/x', 'Operator wtryskarki', 'Warszawa', 'olx:c1', datetime('now'), '{}')`)

	rows, err := st.RawLeadRows(ctx, 7)
	if err != nil {
		t.Fatalf("RawLeadRows: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	r := rows[0]
	if r.JobID != "olx:j1" || r.CompanyName != "Stalmet sp. z o.o." || r.Phone != "+48600700800" {
		t.Errorf("unexpected row: %+v", r)
	}
	if time.Since(r.FetchedAt) > time.Hour {
		t.Errorf("FetchedAt looks wrong: %v", r.FetchedAt)
	}
}
```

(`mustExec` — add a tiny local helper if one doesn't exist: executes SQL against the store's DB, `t.Fatal` on error.)

- [ ] **Step 3: Run test to verify it fails**

Run: `cd ~/projects/printing-press/olx/src && go test ./internal/store/ -run TestRawLeadRows -v`
Expected: FAIL — `RawLeadRows` undefined.

- [ ] **Step 4: Implement `RawLeadRows`**

`internal/store/rawleads.go`:

```go
package store

import (
	"context"
	"fmt"
	"time"
)

// RawLeadRow is one offer row destined for the lead-engine raw-leads
// contract export.
type RawLeadRow struct {
	JobID       string
	Title       string
	City        string
	Region      string
	CompanyName string
	NIP         string
	Phone       string
	Email       string
	FetchedAt   time.Time
}

// RawLeadRows returns jobs fetched within the last `days` days joined with
// their company and best-known phone (per-job phone first, company phone
// as fallback).
func (s *Store) RawLeadRows(ctx context.Context, days int) ([]RawLeadRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT j.id, j.title,
		       COALESCE(j.location_city, ''), COALESCE(j.location_region, ''),
		       COALESCE(c.name, ''), COALESCE(c.nip, ''),
		       COALESCE((SELECT p.phone FROM phones p WHERE p.job_id = j.id LIMIT 1),
		                COALESCE(c.phone, '')),
		       COALESCE(c.email, ''), j.fetched_at
		FROM jobs j
		LEFT JOIN companies c ON c.id = j.company_id
		WHERE j.fetched_at >= datetime('now', ?)
		ORDER BY j.fetched_at DESC`,
		fmt.Sprintf("-%d days", days))
	if err != nil {
		return nil, fmt.Errorf("raw lead rows: %w", err)
	}
	defer rows.Close()
	var out []RawLeadRow
	for rows.Next() {
		var r RawLeadRow
		if err := rows.Scan(&r.JobID, &r.Title, &r.City, &r.Region,
			&r.CompanyName, &r.NIP, &r.Phone, &r.Email, &r.FetchedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
```

(If the store's DB field is not `s.db`, match whatever `createOLXTables` uses.)

- [ ] **Step 5: Run test, verify pass**

Run: `go test ./internal/store/ -run TestRawLeadRows -v`
Expected: PASS.

- [ ] **Step 6: Add `--kind raw-leads` to the export command**

In `internal/cli/export.go`: register the new kind and a `--days` flag (default 7). In `runExport`, alongside the existing `jobs`/`companies` cases, add:

```go
	case "raw-leads":
		if f.format != "json" {
			return usageErr("--kind raw-leads requires --format json")
		}
		rows, err := st.RawLeadRows(ctx, f.days)
		if err != nil {
			return err
		}
		type contractOffer struct {
			ExternalID  string         `json:"externalId"`
			NIP         *string        `json:"nip"`
			CompanyName string         `json:"companyName"`
			Position    string         `json:"position"`
			Location    string         `json:"location"`
			Vacancies   int            `json:"vacancies"`
			SalaryFrom  *float64       `json:"salaryFrom"`
			SalaryTo    *float64       `json:"salaryTo"`
			Phone       *string        `json:"phone"`
			Email       *string        `json:"email"`
			Score       *int           `json:"score"`
			ScrapedAt   string         `json:"scrapedAt"`
			Extra       map[string]any `json:"extra"`
		}
		strPtr := func(s string) *string {
			if s == "" {
				return nil
			}
			return &s
		}
		offers := make([]contractOffer, 0, len(rows))
		for _, r := range rows {
			loc := r.City
			if loc == "" {
				loc = r.Region
			}
			offers = append(offers, contractOffer{
				ExternalID:  r.JobID,
				NIP:         strPtr(r.NIP),
				CompanyName: r.CompanyName,
				Position:    r.Title,
				Location:    loc,
				Vacancies:   1,
				Phone:       strPtr(r.Phone),
				Email:       strPtr(r.Email),
				ScrapedAt:   r.FetchedAt.UTC().Format(time.RFC3339),
				Extra:       map[string]any{},
			})
		}
		payload := map[string]any{
			"contractVersion": 1,
			"source":          "olx",
			"exportedAt":      time.Now().UTC().Format(time.RFC3339),
			"offers":          offers,
		}
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(payload)
```

Add the flag next to the existing ones in `newExportCmd`:

```go
	cmd.Flags().IntVar(&f.days, "days", 7, "raw-leads: include jobs fetched in the last N days")
```

and `days int` to the `exportFlags` struct. Reuse the existing output-file plumbing (`f.out` / default timestamped path) — `w` is the same writer the `jobs`/`companies` cases use.

- [ ] **Step 7: Build, test, and verify against the lead-engine fixture shape**

```bash
make build && make test
./bin/olx-pp-cli export --kind raw-leads --format json --out /tmp/raw-leads-olx.json
python3 -c "import json; d=json.load(open('/tmp/raw-leads-olx.json')); assert d['contractVersion']==1 and d['source']=='olx'; print(len(d['offers']), 'offers OK')"
```

Expected: build + tests pass; the assertion prints an offer count.

- [ ] **Step 8: Commit (branch per the repo's workflow)**

```bash
cd ~/projects/printing-press/olx
git checkout -b feature/raw-leads-export
git add src/internal/store/rawleads.go src/internal/store/rawleads_test.go src/internal/cli/export.go
git commit -m "feat: export raw-leads v1 contract for lead-engine"
```

---

## Phase 2 — Enrichment

### Task 8: Spend ledger + API cache store functions

**Files:**
- Create: `internal/store/spend.go`
- Create: `internal/store/cache.go`
- Test: `internal/store/spend_test.go`

- [ ] **Step 1: Write the failing test**

`internal/store/spend_test.go`:

```go
package store

import (
	"testing"
	"time"
)

func TestSpendLedger(t *testing.T) {
	st := openTest(t)
	if got, _ := st.SpendToday("bizraport"); got != 0 {
		t.Errorf("initial spend = %v", got)
	}
	st.AddSpend("bizraport", 1.5)
	st.AddSpend("bizraport", 0.5)
	if got, _ := st.SpendToday("bizraport"); got != 2.0 {
		t.Errorf("spend = %v, want 2.0", got)
	}
	if got, _ := st.SpendToday("other"); got != 0 {
		t.Errorf("other-api spend = %v", got)
	}
}

func TestAPICache(t *testing.T) {
	st := openTest(t)
	if _, ok, _ := st.CacheGet("krs", "0000123456", time.Hour); ok {
		t.Error("empty cache returned a hit")
	}
	st.CachePut("krs", "0000123456", []byte(`{"a":1}`))
	got, ok, err := st.CacheGet("krs", "0000123456", time.Hour)
	if err != nil || !ok || string(got) != `{"a":1}` {
		t.Errorf("cache get: %q ok=%v err=%v", got, ok, err)
	}
	// TTL expiry
	if _, ok, _ := st.CacheGet("krs", "0000123456", -time.Second); ok {
		t.Error("expired entry returned as hit")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/store/ -run "TestSpend|TestAPICache" -v`
Expected: FAIL — functions undefined.

- [ ] **Step 3: Implement**

`internal/store/spend.go`:

```go
package store

func (s *Store) SpendToday(api string) (float64, error) {
	var pln float64
	err := s.DB.QueryRow(`SELECT COALESCE(SUM(pln), 0) FROM spend_log
		WHERE api = ? AND day = date('now')`, api).Scan(&pln)
	return pln, err
}

func (s *Store) AddSpend(api string, pln float64) error {
	_, err := s.DB.Exec(`INSERT INTO spend_log (day, api, pln)
		VALUES (date('now'), ?, ?)`, api, pln)
	return err
}
```

`internal/store/cache.go`:

```go
package store

import (
	"database/sql"
	"errors"
	"time"
)

func (s *Store) CacheGet(api, identifier string, ttl time.Duration) ([]byte, bool, error) {
	var payload, fetched string
	err := s.DB.QueryRow(`SELECT payload, fetched_at FROM api_cache
		WHERE api = ? AND identifier = ?`, api, identifier).Scan(&payload, &fetched)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	t, err := time.Parse(time.RFC3339, fetched)
	if err != nil || time.Since(t) > ttl {
		return nil, false, nil
	}
	return []byte(payload), true, nil
}

func (s *Store) CachePut(api, identifier string, payload []byte) error {
	_, err := s.DB.Exec(`INSERT INTO api_cache (api, identifier, payload, fetched_at)
		VALUES (?,?,?,?)
		ON CONFLICT(api, identifier) DO UPDATE SET
		  payload = excluded.payload, fetched_at = excluded.fetched_at`,
		api, identifier, string(payload), time.Now().UTC().Format(time.RFC3339))
	return err
}
```

- [ ] **Step 4: Run tests, verify pass**

Run: `go test ./internal/store/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/store/spend.go internal/store/cache.go internal/store/spend_test.go
git commit -m "feat: daily spend ledger and TTL api cache"
```

---

### Task 9: BizRaport client port + NIP resolution stage

**Files:**
- Create: `internal/enrich/bizraport/client.go` (ported from olx repo)
- Create: `internal/enrich/resolve.go`
- Create: `internal/store/enrichment.go`
- Test: `internal/enrich/resolve_test.go`

- [ ] **Step 1: Port the client**

```bash
mkdir -p internal/enrich/bizraport
cp ~/projects/printing-press/olx/src/internal/bizraport/client.go internal/enrich/bizraport/client.go
go build ./...
```

The file is self-contained (stdlib only). Keep `package bizraport`. Fix any imports the build complains about. Key surface used below: `New(Options) *Client`, `HasCredentials()`, `Search(ctx, q, limit) ([]string /*krs*/, bool, error)`, `GetByKRS(ctx, krs)`, `GetByNIP(ctx, nip)`, `ParseProfile(raw)`, `CompanyProfile{KRS, NIP, KodPKD, Info{Nazwa, REGON, FormaPrawna, Ulica, KodPocztowy, Miejscowosc, Email, StronaWWW}, Raw}`. Check `Options` fields in the copied file (base URL, email, password, HTTP client) and note them — the test and config wiring use them.

- [ ] **Step 2: Write the failing resolve test**

`internal/enrich/resolve_test.go`:

```go
package enrich

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/hrkono/lead-engine/internal/enrich/bizraport"
	"github.com/hrkono/lead-engine/internal/store"
)

// fakeBizraport serves /api/szukaj (one KRS hit) and /api/dane (profile).
func fakeBizraport(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/szukaj":
			fmt.Fprint(w, `{"wyniki":[{"krs":"0000123456","nazwa":"STALMET SP Z O O"}]}`)
		case "/api/dane":
			fmt.Fprint(w, `{"krs":"0000123456","nip":"1234567890","regon":"123456785",
				"informacje":{"nazwa":"Stalmet Sp. z o.o.","forma_prawna":"sp. z o.o.",
				"ulica":"Prosta 1","kod_pocztowy":"00-001","miejscowosc":"Warszawa",
				"email":"biuro@stalmet.example","strona_www":"stalmet.example"},
				"kod_pkd":"25.11.Z"}`)
		default:
			http.NotFound(w, r)
		}
	}))
}

func testStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "leads.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestResolveNIPs(t *testing.T) {
	srv := fakeBizraport(t)
	defer srv.Close()
	st := testStore(t)
	id, _ := st.CreateCompany("", "Stalmet sp. z o.o.", "stalmet", "pending")

	bz := bizraport.New(bizraport.Options{BaseURL: srv.URL, Email: "x", Password: "y"})
	stats, err := ResolveNIPs(context.Background(), st, bz, ResolveConfig{
		DailyCapPLN: 10, CostPerRowPLN: 0.5, MaxCandidates: 5,
	})
	if err != nil {
		t.Fatalf("ResolveNIPs: %v", err)
	}
	if stats.Resolved != 1 {
		t.Errorf("Resolved = %d", stats.Resolved)
	}
	c, _ := st.FindCompanyByNIP("1234567890")
	if c == nil || c.ID != id || c.NIPStatus != "verified" {
		t.Fatalf("company not verified: %+v", c)
	}
	if c.KRS != "0000123456" || c.Website != "stalmet.example" || c.PKDMain != "25.11.Z" {
		t.Errorf("profile not applied: %+v", c)
	}
	spent, _ := st.SpendToday("bizraport")
	if spent <= 0 {
		t.Errorf("no spend recorded: %v", spent)
	}
}

func TestResolveRespectsCap(t *testing.T) {
	srv := fakeBizraport(t)
	defer srv.Close()
	st := testStore(t)
	st.CreateCompany("", "Stalmet sp. z o.o.", "stalmet", "pending")
	bz := bizraport.New(bizraport.Options{BaseURL: srv.URL, Email: "x", Password: "y"})

	stats, err := ResolveNIPs(context.Background(), st, bz, ResolveConfig{
		DailyCapPLN: 0.01, CostPerRowPLN: 0.5, MaxCandidates: 5, // cap < worst case
	})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Resolved != 0 || stats.SkippedBudget != 1 {
		t.Errorf("stats = %+v, want 0 resolved / 1 skipped", stats)
	}
}

var _ = json.Marshal // silence unused import if shapes change
```

Adapt the fake server's JSON field names to what the ported `ParseProfile`/`Search` actually decode (read the copied client.go; the existing olx tests, if any, show the exact wire shapes). The assertions on store state are the contract of this task.

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./internal/enrich/ -v`
Expected: FAIL — `ResolveNIPs` undefined.

- [ ] **Step 4: Implement store helpers + resolution**

`internal/store/enrichment.go`:

```go
package store

func (s *Store) CompaniesPendingNIP() ([]Company, error) {
	rows, err := s.DB.Query(`SELECT ` + companyCols + ` FROM companies
		WHERE nip_status = 'pending' ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Company
	for rows.Next() {
		var c Company
		if err := rows.Scan(&c.ID, &c.NIP, &c.Name, &c.NormalizedName, &c.NIPStatus,
			&c.Address, &c.REGON, &c.KRS, &c.LegalForm, &c.PKDMain, &c.CompanySize,
			&c.Website, &c.Email, &c.Phone, &c.BoardMembers, &c.FirstSeen, &c.LastSeen); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) MarkCompanyUnresolved(id int64) error {
	_, err := s.DB.Exec(`UPDATE companies SET nip_status='unresolved' WHERE id=?`, id)
	return err
}

func (s *Store) SetCompanyNIP(id int64, nip string) error {
	_, err := s.DB.Exec(`UPDATE companies SET nip=?, nip_status='verified' WHERE id=?`, nip, id)
	return err
}

// FillCompanyFields sets each non-empty value only where the current column
// is still empty — enrichment never overwrites earlier data.
func (s *Store) FillCompanyFields(id int64, f map[string]string) error {
	allowed := map[string]bool{
		"address": true, "regon": true, "krs": true, "legal_form": true,
		"pkd_main": true, "company_size": true, "website": true,
		"email": true, "phone": true, "board_members": true,
	}
	for col, val := range f {
		if val == "" || !allowed[col] {
			continue
		}
		if _, err := s.DB.Exec(
			`UPDATE companies SET `+col+` = ? WHERE id = ? AND `+col+` = ''`,
			val, id); err != nil {
			return err
		}
	}
	return nil
}
```

`internal/enrich/resolve.go`:

```go
// Package enrich resolves company identity and fills registry data
// post-merge, per the spec's cost-optimal sequencing.
package enrich

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/hrkono/lead-engine/internal/enrich/bizraport"
	"github.com/hrkono/lead-engine/internal/match"
	"github.com/hrkono/lead-engine/internal/store"
)

const cacheTTL = 90 * 24 * time.Hour

type ResolveConfig struct {
	DailyCapPLN   float64
	CostPerRowPLN float64
	MaxCandidates int
}

type ResolveStats struct {
	Resolved      int
	Unresolved    int
	SkippedBudget int
}

func ResolveNIPs(ctx context.Context, st *store.Store, bz *bizraport.Client, cfg ResolveConfig) (ResolveStats, error) {
	var stats ResolveStats
	pending, err := st.CompaniesPendingNIP()
	if err != nil {
		return stats, fmt.Errorf("resolve: %w", err)
	}
	for _, c := range pending {
		// Worst case: search returns MaxCandidates rows + we fetch profiles.
		worst := float64(2*cfg.MaxCandidates) * cfg.CostPerRowPLN
		spent, err := st.SpendToday("bizraport")
		if err != nil {
			return stats, err
		}
		if spent+worst > cfg.DailyCapPLN {
			stats.SkippedBudget++
			continue // company stays pending; retried tomorrow
		}
		profile, paidRows, err := resolveByName(ctx, st, bz, c.Name, cfg.MaxCandidates)
		if paidRows > 0 {
			if serr := st.AddSpend("bizraport", float64(paidRows)*cfg.CostPerRowPLN); serr != nil {
				return stats, serr
			}
		}
		if err != nil {
			return stats, fmt.Errorf("resolve %q: %w", c.Name, err)
		}
		if profile == nil || profile.NIP == "" {
			if err := st.MarkCompanyUnresolved(c.ID); err != nil {
				return stats, err
			}
			stats.Unresolved++
			continue
		}
		targetID := c.ID
		if existing, _ := st.FindCompanyByNIP(profile.NIP); existing != nil && existing.ID != c.ID {
			if err := st.MergeCompanies(c.ID, existing.ID); err != nil {
				return stats, err
			}
			targetID = existing.ID
		} else if err := st.SetCompanyNIP(c.ID, profile.NIP); err != nil {
			return stats, err
		}
		if err := applyProfile(st, targetID, profile); err != nil {
			return stats, err
		}
		stats.Resolved++
	}
	return stats, nil
}

// resolveByName mirrors the olx module's resolveProfile: bounded paid
// search, then verify the registry name matches before trusting a hit.
// Returns the profile (nil if no confident match) and the number of
// billable rows consumed.
func resolveByName(ctx context.Context, st *store.Store, bz *bizraport.Client, name string, maxCandidates int) (*bizraport.CompanyProfile, int, error) {
	krsList, _, err := bz.Search(ctx, name, maxCandidates)
	if err != nil {
		return nil, 0, err
	}
	paid := len(krsList) // /api/szukaj bills per returned row
	want := match.Normalize(name)
	for _, krs := range krsList {
		var p *bizraport.CompanyProfile
		if raw, ok, _ := st.CacheGet("bizraport-krs", krs, cacheTTL); ok {
			p, err = bizraport.ParseProfile(raw)
		} else {
			p, err = bz.GetByKRS(ctx, krs)
			if err == nil && p != nil {
				paid++ // /api/dane bills the returned row
				st.CachePut("bizraport-krs", krs, p.Raw)
			}
		}
		if err != nil {
			return nil, paid, err
		}
		if p == nil {
			continue
		}
		if match.Normalize(p.Info.Nazwa) == want {
			return p, paid, nil
		}
	}
	return nil, paid, nil
}

func applyProfile(st *store.Store, companyID int64, p *bizraport.CompanyProfile) error {
	addr := strings.TrimSpace(strings.Join(nonEmpty(
		p.Info.Ulica, p.Info.KodPocztowy, p.Info.Miejscowosc), ", "))
	return st.FillCompanyFields(companyID, map[string]string{
		"regon":      p.Info.REGON,
		"krs":        p.KRS,
		"legal_form": p.Info.FormaPrawna,
		"pkd_main":   p.KodPKD,
		"website":    p.Info.StronaWWW,
		"email":      p.Info.Email,
		"address":    addr,
	})
}

func nonEmpty(parts ...string) []string {
	var out []string
	for _, p := range parts {
		if strings.TrimSpace(p) != "" {
			out = append(out, p)
		}
	}
	return out
}
```

(If the ported `CompanyProfile.Info` lacks a `REGON` field, take REGON from the top-level profile field the client exposes — check the copied struct and keep `applyProfile` consistent with it.)

- [ ] **Step 5: Run tests, verify pass**

Run: `go test ./internal/enrich/ ./internal/store/ -v`
Expected: PASS (both resolve tests, cap respected).

- [ ] **Step 6: Commit**

```bash
git add internal/enrich/ internal/store/enrichment.go
git commit -m "feat: BizRaport NIP resolution with verified-name match and daily cap"
```

---

### Task 10: KRS client — board members

**Files:**
- Create: `internal/enrich/krs/client.go`
- Create: `internal/enrich/krs/testdata/odpis.json`
- Test: `internal/enrich/krs/client_test.go`

- [ ] **Step 1: Create the test fixture**

`internal/enrich/krs/testdata/odpis.json` (minimal shape of the MS KRS `OdpisAktualny` response — replace with a recorded real response when first integrating live; the struct tags below are what matter):

```json
{
  "odpis": {
    "naglowekA": {"numerKRS": "0000123456"},
    "dane": {
      "dzial2": {
        "reprezentacja": {
          "sklad": [
            {
              "nazwisko": {"nazwiskoICzlon": "KOWALSKI"},
              "imiona": {"imie": "JAN"},
              "funkcjaWOrganie": "PREZES ZARZĄDU"
            },
            {
              "nazwisko": {"nazwiskoICzlon": "NOWAK"},
              "imiona": {"imie": "ANNA"},
              "funkcjaWOrganie": "CZŁONEK ZARZĄDU"
            }
          ]
        }
      }
    }
  }
}
```

- [ ] **Step 2: Write the failing test**

`internal/enrich/krs/client_test.go`:

```go
package krs

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func TestFetchBoard(t *testing.T) {
	fixture, err := os.ReadFile("testdata/odpis.json")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/krs/OdpisAktualny/0000123456" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("rejestr") != "P" {
			http.Error(w, "missing rejestr", 400)
			return
		}
		w.Write(fixture)
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL}
	board, err := c.FetchBoard(context.Background(), "0000123456")
	if err != nil {
		t.Fatalf("FetchBoard: %v", err)
	}
	if len(board) != 2 {
		t.Fatalf("board = %d members", len(board))
	}
	if board[0].Name != "JAN KOWALSKI" || board[0].Role != "PREZES ZARZĄDU" {
		t.Errorf("member[0] = %+v", board[0])
	}
}

func TestFetchBoardNotFound(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	c := &Client{BaseURL: srv.URL}
	board, err := c.FetchBoard(context.Background(), "0000000000")
	if err != nil || board != nil {
		t.Errorf("404 should be (nil, nil), got %v, %v", board, err)
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./internal/enrich/krs/ -v`
Expected: FAIL — package empty.

- [ ] **Step 4: Implement the client**

`internal/enrich/krs/client.go`:

```go
// Package krs fetches current KRS extracts from the free MS registry API
// (api-krs.ms.gov.pl) — the only source of board members in the pipeline.
package krs

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const DefaultBaseURL = "https://api-krs.ms.gov.pl"

type BoardMember struct {
	Name string `json:"name"`
	Role string `json:"role"`
}

type Client struct {
	BaseURL string
	HTTP    *http.Client
}

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 30 * time.Second}
}

func (c *Client) base() string {
	if c.BaseURL != "" {
		return c.BaseURL
	}
	return DefaultBaseURL
}

type odpisResponse struct {
	Odpis struct {
		Dane struct {
			Dzial2 struct {
				Reprezentacja struct {
					Sklad []struct {
						Nazwisko struct {
							NazwiskoICzlon string `json:"nazwiskoICzlon"`
						} `json:"nazwisko"`
						Imiona struct {
							Imie string `json:"imie"`
						} `json:"imiona"`
						Funkcja string `json:"funkcjaWOrganie"`
					} `json:"sklad"`
				} `json:"reprezentacja"`
			} `json:"dzial2"`
		} `json:"dane"`
	} `json:"odpis"`
}

// FetchBoard returns the management board for a KRS number, or (nil, nil)
// when the registry has no such entity (404) — non-blocking by design.
func (c *Client) FetchBoard(ctx context.Context, krsNum string) ([]BoardMember, error) {
	url := fmt.Sprintf("%s/api/krs/OdpisAktualny/%s?rejestr=P&format=json", c.base(), krsNum)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http().Do(req)
	if err != nil {
		return nil, fmt.Errorf("krs %s: %w", krsNum, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("krs %s: status %d", krsNum, resp.StatusCode)
	}
	var o odpisResponse
	if err := json.NewDecoder(resp.Body).Decode(&o); err != nil {
		return nil, fmt.Errorf("krs %s: decode: %w", krsNum, err)
	}
	var board []BoardMember
	for _, m := range o.Odpis.Dane.Dzial2.Reprezentacja.Sklad {
		name := strings.TrimSpace(m.Imiona.Imie + " " + m.Nazwisko.NazwiskoICzlon)
		if name == "" {
			continue
		}
		board = append(board, BoardMember{Name: name, Role: m.Funkcja})
	}
	return board, nil
}
```

- [ ] **Step 5: Run tests, verify pass**

Run: `go test ./internal/enrich/krs/ -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/enrich/krs/
git commit -m "feat: KRS client for board members (free MS registry API)"
```

---

### Task 11: REGON client (BIR1.1) — gap fill + KRS-number discovery

**Files:**
- Create: `internal/enrich/regon/client.go`
- Create: `internal/enrich/regon/envelopes.go`
- Test: `internal/enrich/regon/client_test.go`

REGON's BIR1.1 is SOAP. Scope here is deliberately narrow: given a NIP, return phone/email/website/address and the KRS number for legal entities. PKD and company size come from BizRaport (OLX path) or the gov_api export (`extra.regon`) — this client only fills contact/identity gaps.

- [ ] **Step 1: Write the failing test (fake SOAP server)**

`internal/enrich/regon/client_test.go`:

```go
package regon

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const searchInner = `&lt;root&gt;&lt;dane&gt;&lt;Regon&gt;123456785&lt;/Regon&gt;&lt;Typ&gt;P&lt;/Typ&gt;&lt;Nazwa&gt;STALMET&lt;/Nazwa&gt;&lt;/dane&gt;&lt;/root&gt;`

const reportInner = `&lt;root&gt;&lt;dane&gt;` +
	`&lt;praw_numerTelefonu&gt;221112233&lt;/praw_numerTelefonu&gt;` +
	`&lt;praw_adresEmail&gt;biuro@stalmet.example&lt;/praw_adresEmail&gt;` +
	`&lt;praw_adresStronyinternetowej&gt;stalmet.example&lt;/praw_adresStronyinternetowej&gt;` +
	`&lt;praw_numerWRejestrzeEwidencji&gt;0000123456&lt;/praw_numerWRejestrzeEwidencji&gt;` +
	`&lt;praw_adSiedzMiejscowosc_Nazwa&gt;Warszawa&lt;/praw_adSiedzMiejscowosc_Nazwa&gt;` +
	`&lt;praw_adSiedzUlica_Nazwa&gt;Prosta&lt;/praw_adSiedzUlica_Nazwa&gt;` +
	`&lt;praw_adSiedzNumerNieruchomosci&gt;1&lt;/praw_adSiedzNumerNieruchomosci&gt;` +
	`&lt;praw_adSiedzKodPocztowy&gt;00001&lt;/praw_adSiedzKodPocztowy&gt;` +
	`&lt;/dane&gt;&lt;/root&gt;`

func soapBody(action, result, inner string) string {
	return fmt.Sprintf(`<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope">
<s:Body><%sResponse xmlns="http://CIS/BIR/PUBL/2014/07"><%s>%s</%s></%sResponse></s:Body>
</s:Envelope>`, action, result, inner, result, action)
}

func fakeBIR(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s := string(body)
		switch {
		case strings.Contains(s, "Zaloguj"):
			fmt.Fprint(w, soapBody("Zaloguj", "ZalogujResult", "test-sid-123"))
		case strings.Contains(s, "DaneSzukajPodmioty"):
			if r.Header.Get("sid") != "test-sid-123" {
				http.Error(w, "no sid", 403)
				return
			}
			fmt.Fprint(w, soapBody("DaneSzukajPodmioty", "DaneSzukajPodmiotyResult", searchInner))
		case strings.Contains(s, "DanePobierzPelnyRaport"):
			fmt.Fprint(w, soapBody("DanePobierzPelnyRaport", "DanePobierzPelnyRaportResult", reportInner))
		case strings.Contains(s, "Wyloguj"):
			fmt.Fprint(w, soapBody("Wyloguj", "WylogujResult", "true"))
		default:
			http.Error(w, "unknown action", 400)
		}
	}))
}

func TestLookupByNIP(t *testing.T) {
	srv := fakeBIR(t)
	defer srv.Close()
	c := &Client{Endpoint: srv.URL, APIKey: "test-key"}
	rep, err := c.LookupByNIP(context.Background(), "1234567890")
	if err != nil {
		t.Fatalf("LookupByNIP: %v", err)
	}
	if rep.REGON != "123456785" || rep.KRS != "0000123456" {
		t.Errorf("identity: %+v", rep)
	}
	if rep.Phone != "221112233" || rep.Email != "biuro@stalmet.example" || rep.Website != "stalmet.example" {
		t.Errorf("contact: %+v", rep)
	}
	if rep.Address != "Prosta 1, 00-001 Warszawa" {
		t.Errorf("address = %q", rep.Address)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/enrich/regon/ -v`
Expected: FAIL.

- [ ] **Step 3: Implement envelopes + client**

`internal/enrich/regon/envelopes.go`:

```go
package regon

// SOAP 1.2 envelopes for BIR1.1 (UslugaBIRzewnPubl). %s placeholders are
// filled with fmt.Sprintf; the To header is required by the service.
const (
	envZaloguj = `<soap:Envelope xmlns:soap="http://www.w3.org/2003/05/soap-envelope" xmlns:ns="http://CIS/BIR/PUBL/2014/07" xmlns:wsa="http://www.w3.org/2005/08/addressing">
<soap:Header><wsa:To>%s</wsa:To><wsa:Action>http://CIS/BIR/PUBL/2014/07/IUslugaBIRzewnPubl/Zaloguj</wsa:Action></soap:Header>
<soap:Body><ns:Zaloguj><ns:pKluczUzytkownika>%s</ns:pKluczUzytkownika></ns:Zaloguj></soap:Body>
</soap:Envelope>`

	envSzukaj = `<soap:Envelope xmlns:soap="http://www.w3.org/2003/05/soap-envelope" xmlns:ns="http://CIS/BIR/PUBL/2014/07" xmlns:dat="http://CIS/BIR/PUBL/2014/07/DataContract" xmlns:wsa="http://www.w3.org/2005/08/addressing">
<soap:Header><wsa:To>%s</wsa:To><wsa:Action>http://CIS/BIR/PUBL/2014/07/IUslugaBIRzewnPubl/DaneSzukajPodmioty</wsa:Action></soap:Header>
<soap:Body><ns:DaneSzukajPodmioty><ns:pParametryWyszukiwania><dat:Nip>%s</dat:Nip></ns:pParametryWyszukiwania></ns:DaneSzukajPodmioty></soap:Body>
</soap:Envelope>`

	envRaport = `<soap:Envelope xmlns:soap="http://www.w3.org/2003/05/soap-envelope" xmlns:ns="http://CIS/BIR/PUBL/2014/07" xmlns:wsa="http://www.w3.org/2005/08/addressing">
<soap:Header><wsa:To>%s</wsa:To><wsa:Action>http://CIS/BIR/PUBL/2014/07/IUslugaBIRzewnPubl/DanePobierzPelnyRaport</wsa:Action></soap:Header>
<soap:Body><ns:DanePobierzPelnyRaport><ns:pRegon>%s</ns:pRegon><ns:pNazwaRaportu>%s</ns:pNazwaRaportu></ns:DanePobierzPelnyRaport></soap:Body>
</soap:Envelope>`

	envWyloguj = `<soap:Envelope xmlns:soap="http://www.w3.org/2003/05/soap-envelope" xmlns:ns="http://CIS/BIR/PUBL/2014/07" xmlns:wsa="http://www.w3.org/2005/08/addressing">
<soap:Header><wsa:To>%s</wsa:To><wsa:Action>http://CIS/BIR/PUBL/2014/07/IUslugaBIRzewnPubl/Wyloguj</wsa:Action></soap:Header>
<soap:Body><ns:Wyloguj><ns:pIdentyfikatorSesji>%s</ns:pIdentyfikatorSesji></ns:Wyloguj></soap:Body>
</soap:Envelope>`
)
```

`internal/enrich/regon/client.go`:

```go
// Package regon implements a minimal BIR1.1 client: NIP -> contact data,
// address, and KRS number. Free API; sessions are opened and closed per
// lookup batch via Login/Logout.
package regon

import (
	"context"
	"encoding/xml"
	"fmt"
	"html"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

const DefaultEndpoint = "https://wyszukiwarkaregon.stat.gov.pl/wsBIR/UslugaBIRzewnPubl.svc"

type Report struct {
	REGON   string
	Type    string // P = legal entity, F = natural person
	KRS     string
	Phone   string
	Email   string
	Website string
	Address string
}

type Client struct {
	Endpoint string
	APIKey   string
	HTTP     *http.Client
}

func (c *Client) endpoint() string {
	if c.Endpoint != "" {
		return c.Endpoint
	}
	return DefaultEndpoint
}

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 60 * time.Second}
}

func (c *Client) call(ctx context.Context, sid, envelope string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint(), strings.NewReader(envelope))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/soap+xml; charset=utf-8")
	if sid != "" {
		req.Header.Set("sid", sid)
	}
	resp, err := c.http().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("regon: status %d", resp.StatusCode)
	}
	// Responses arrive as multipart MTOM; the envelope is the part between
	// the first <s:Envelope and its closing tag.
	s := string(body)
	start := strings.Index(s, "<s:Envelope")
	if start < 0 {
		start = strings.Index(s, "<soap:Envelope")
	}
	if start < 0 {
		return "", fmt.Errorf("regon: no envelope in response")
	}
	end := strings.Index(s[start:], "Envelope>")
	if end < 0 {
		return "", fmt.Errorf("regon: truncated envelope")
	}
	return s[start : start+end+len("Envelope>")], nil
}

// resultOf extracts the inner text of <XxxResult> and HTML-unescapes it
// (BIR returns embedded XML escaped inside the result element).
func resultOf(envelope, result string) string {
	re := regexp.MustCompile(`(?s)<` + result + `[^>]*>(.*?)</` + result + `>`)
	m := re.FindStringSubmatch(envelope)
	if m == nil {
		return ""
	}
	return html.UnescapeString(m[1])
}

// parseDane decodes BIR's <root><dane>...</dane></root> rows into maps.
func parseDane(innerXML string) ([]map[string]string, error) {
	dec := xml.NewDecoder(strings.NewReader(innerXML))
	var rows []map[string]string
	var cur map[string]string
	var field string
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local == "dane" {
				cur = map[string]string{}
			} else if cur != nil {
				field = t.Name.Local
			}
		case xml.CharData:
			if cur != nil && field != "" {
				cur[field] += string(t)
			}
		case xml.EndElement:
			if t.Name.Local == "dane" && cur != nil {
				rows = append(rows, cur)
				cur = nil
			}
			field = ""
		}
	}
	return rows, nil
}

func (c *Client) LookupByNIP(ctx context.Context, nip string) (*Report, error) {
	env, err := c.call(ctx, "", fmt.Sprintf(envZaloguj, c.endpoint(), c.APIKey))
	if err != nil {
		return nil, fmt.Errorf("regon login: %w", err)
	}
	sid := strings.TrimSpace(resultOf(env, "ZalogujResult"))
	if sid == "" {
		return nil, fmt.Errorf("regon login: empty session id (bad api key?)")
	}
	defer c.call(ctx, sid, fmt.Sprintf(envWyloguj, c.endpoint(), sid))

	env, err = c.call(ctx, sid, fmt.Sprintf(envSzukaj, c.endpoint(), nip))
	if err != nil {
		return nil, fmt.Errorf("regon search %s: %w", nip, err)
	}
	rows, err := parseDane(resultOf(env, "DaneSzukajPodmiotyResult"))
	if err != nil || len(rows) == 0 {
		return nil, fmt.Errorf("regon search %s: no result (err=%v)", nip, err)
	}
	rep := &Report{REGON: rows[0]["Regon"], Type: rows[0]["Typ"]}
	if rep.Type != "P" {
		return rep, nil // sole traders: no praw_ report, no KRS
	}

	env, err = c.call(ctx, sid, fmt.Sprintf(envRaport, c.endpoint(), rep.REGON, "BIR11OsPrawna"))
	if err != nil {
		return rep, fmt.Errorf("regon report %s: %w", rep.REGON, err)
	}
	rrows, err := parseDane(resultOf(env, "DanePobierzPelnyRaportResult"))
	if err != nil || len(rrows) == 0 {
		return rep, nil // search data is still useful
	}
	d := rrows[0]
	rep.Phone = d["praw_numerTelefonu"]
	rep.Email = d["praw_adresEmail"]
	rep.Website = d["praw_adresStronyinternetowej"]
	rep.KRS = d["praw_numerWRejestrzeEwidencji"]
	street := strings.TrimSpace(d["praw_adSiedzUlica_Nazwa"] + " " + d["praw_adSiedzNumerNieruchomosci"])
	zip := d["praw_adSiedzKodPocztowy"]
	if len(zip) == 5 {
		zip = zip[:2] + "-" + zip[2:]
	}
	cityPart := strings.TrimSpace(zip + " " + d["praw_adSiedzMiejscowosc_Nazwa"])
	parts := []string{}
	if street != "" {
		parts = append(parts, street)
	}
	if cityPart != "" {
		parts = append(parts, cityPart)
	}
	rep.Address = strings.Join(parts, ", ")
	return rep, nil
}
```

- [ ] **Step 4: Run tests, verify pass**

Run: `go test ./internal/enrich/regon/ -v`
Expected: PASS. (Field names like `praw_adresStronyinternetowej` must match the live BIR11OsPrawna report; verify casing against `~/projects/gov_api/enricher.py` lines ~130–140, which uses the same report, and against one live call during integration.)

- [ ] **Step 5: Commit**

```bash
git add internal/enrich/regon/
git commit -m "feat: minimal BIR1.1 REGON client for contact data and KRS discovery"
```

---

### Task 12: Enrichment orchestration stage

**Files:**
- Create: `internal/enrich/enrich.go`
- Test: `internal/enrich/enrich_test.go`

- [ ] **Step 1: Write the failing test**

`internal/enrich/enrich_test.go`:

```go
package enrich

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/hrkono/lead-engine/internal/enrich/krs"
	"github.com/hrkono/lead-engine/internal/enrich/regon"
	"github.com/hrkono/lead-engine/internal/store"
)

// regonStub/krsStub satisfy the two lookup interfaces without HTTP.
type regonStub struct{ rep *regon.Report }

func (r regonStub) LookupByNIP(ctx context.Context, nip string) (*regon.Report, error) {
	return r.rep, nil
}

type krsStub struct{ board []krs.BoardMember }

func (k krsStub) FetchBoard(ctx context.Context, krsNum string) ([]krs.BoardMember, error) {
	return k.board, nil
}

func TestEnrichFillsGapsAndBoard(t *testing.T) {
	st := testStore(t) // helper from resolve_test.go
	id, _ := st.CreateCompany("1234567890", "Stalmet Sp. z o.o.", "stalmet", "verified")

	stats, err := Enrich(context.Background(), st,
		regonStub{rep: &regon.Report{REGON: "123456785", Type: "P", KRS: "0000123456",
			Phone: "221112233", Email: "biuro@stalmet.example", Website: "stalmet.example",
			Address: "Prosta 1, 00-001 Warszawa"}},
		krsStub{board: []krs.BoardMember{{Name: "JAN KOWALSKI", Role: "PREZES ZARZĄDU"}}},
	)
	if err != nil {
		t.Fatalf("Enrich: %v", err)
	}
	if stats.Enriched != 1 {
		t.Errorf("Enriched = %d", stats.Enriched)
	}
	c, _ := st.FindCompanyByNIP("1234567890")
	if c.ID != id || c.KRS != "0000123456" || c.Phone != "221112233" {
		t.Errorf("regon fields: %+v", c)
	}
	var board []krs.BoardMember
	json.Unmarshal([]byte(c.BoardMembers), &board)
	if len(board) != 1 || board[0].Name != "JAN KOWALSKI" {
		t.Errorf("board = %q", c.BoardMembers)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/enrich/ -run TestEnrichFills -v`
Expected: FAIL — `Enrich` undefined.

- [ ] **Step 3: Implement**

`internal/enrich/enrich.go`:

```go
package enrich

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/hrkono/lead-engine/internal/enrich/krs"
	"github.com/hrkono/lead-engine/internal/enrich/regon"
	"github.com/hrkono/lead-engine/internal/store"
)

// RegonLookup and KRSLookup abstract the API clients for testability.
type RegonLookup interface {
	LookupByNIP(ctx context.Context, nip string) (*regon.Report, error)
}

type KRSLookup interface {
	FetchBoard(ctx context.Context, krsNum string) ([]krs.BoardMember, error)
}

type Stats struct {
	Enriched int
	Errors   int
}

// Enrich fills missing registry fields on verified companies using the
// free APIs: REGON for contact/address/KRS number, then KRS for the board.
// Failures are per-company and non-blocking: the company ships partial and
// is retried on the next run.
func Enrich(ctx context.Context, st *store.Store, rg RegonLookup, kc KRSLookup) (Stats, error) {
	var stats Stats
	companies, err := st.CompaniesNeedingEnrichment()
	if err != nil {
		return stats, fmt.Errorf("enrich: %w", err)
	}
	for _, c := range companies {
		if c.Phone == "" || c.Email == "" || c.Website == "" || c.Address == "" || c.KRS == "" || c.REGON == "" {
			rep, err := lookupRegonCached(ctx, st, rg, c.NIP)
			if err != nil {
				stats.Errors++
			} else if rep != nil {
				if err := st.FillCompanyFields(c.ID, map[string]string{
					"regon": rep.REGON, "krs": rep.KRS, "phone": rep.Phone,
					"email": rep.Email, "website": rep.Website, "address": rep.Address,
				}); err != nil {
					return stats, err
				}
			}
		}
		// Re-read: REGON may have just supplied the KRS number.
		cur, err := st.FindCompanyByNIP(c.NIP)
		if err != nil || cur == nil {
			continue
		}
		if cur.KRS != "" && cur.BoardMembers == "" {
			board, err := fetchBoardCached(ctx, st, kc, cur.KRS)
			if err != nil {
				stats.Errors++
			} else if len(board) > 0 {
				b, _ := json.Marshal(board)
				if err := st.FillCompanyFields(cur.ID, map[string]string{"board_members": string(b)}); err != nil {
					return stats, err
				}
			}
		}
		stats.Enriched++
	}
	return stats, nil
}

func lookupRegonCached(ctx context.Context, st *store.Store, rg RegonLookup, nip string) (*regon.Report, error) {
	if raw, ok, _ := st.CacheGet("regon-nip", nip, cacheTTL); ok {
		var rep regon.Report
		if err := json.Unmarshal(raw, &rep); err == nil {
			return &rep, nil
		}
	}
	rep, err := rg.LookupByNIP(ctx, nip)
	if err != nil {
		return nil, err
	}
	if rep != nil {
		if raw, err := json.Marshal(rep); err == nil {
			st.CachePut("regon-nip", nip, raw)
		}
	}
	return rep, nil
}

func fetchBoardCached(ctx context.Context, st *store.Store, kc KRSLookup, krsNum string) ([]krs.BoardMember, error) {
	if raw, ok, _ := st.CacheGet("krs-board", krsNum, cacheTTL); ok {
		var board []krs.BoardMember
		if err := json.Unmarshal(raw, &board); err == nil {
			return board, nil
		}
	}
	board, err := kc.FetchBoard(ctx, krsNum)
	if err != nil {
		return nil, err
	}
	if raw, err := json.Marshal(board); err == nil {
		st.CachePut("krs-board", krsNum, raw)
	}
	return board, nil
}
```

Add to `internal/store/enrichment.go`:

```go
// CompaniesNeedingEnrichment returns verified companies missing any
// enrichment field that the free APIs can supply.
func (s *Store) CompaniesNeedingEnrichment() ([]Company, error) {
	rows, err := s.DB.Query(`SELECT ` + companyCols + ` FROM companies
		WHERE nip_status = 'verified'
		  AND (phone='' OR email='' OR website='' OR address='' OR krs='' OR regon='' OR board_members='')
		ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Company
	for rows.Next() {
		var c Company
		if err := rows.Scan(&c.ID, &c.NIP, &c.Name, &c.NormalizedName, &c.NIPStatus,
			&c.Address, &c.REGON, &c.KRS, &c.LegalForm, &c.PKDMain, &c.CompanySize,
			&c.Website, &c.Email, &c.Phone, &c.BoardMembers, &c.FirstSeen, &c.LastSeen); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
```

(Refactor note: `CompaniesPendingNIP` and `CompaniesNeedingEnrichment` share the scan loop — extract a `queryCompanies(query string, args ...any)` helper used by both, plus `FindCompanyByNIP`'s scan; keep it in `companies.go`.)

- [ ] **Step 4: Run tests, verify pass**

Run: `go test ./internal/... -v`
Expected: PASS everywhere.

- [ ] **Step 5: Commit**

```bash
git add internal/enrich/enrich.go internal/enrich/enrich_test.go internal/store/
git commit -m "feat: free-API enrichment stage (REGON gaps + KRS board) with caching"
```

---

## Phase 3 — Qualification, delivery, runner

### Task 13: Lead building — dedup, suppression, qualification

**Files:**
- Create: `internal/qualify/qualify.go`
- Create: `internal/store/leads.go`
- Test: `internal/qualify/qualify_test.go`

- [ ] **Step 1: Write the failing test**

`internal/qualify/qualify_test.go`:

```go
package qualify

import (
	"path/filepath"
	"testing"

	"github.com/hrkono/lead-engine/internal/store"
)

func testStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "leads.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func addOffer(t *testing.T, st *store.Store, source, extID string, companyID int64, position string, score *int) {
	t.Helper()
	if err := st.UpsertRawOffer(store.RawOffer{
		Source: source, ExternalID: extID, CompanyName: "x",
		Position: position, Score: score, ScrapedAt: "2026-06-10T05:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.AttachOffer(source, extID, companyID); err != nil {
		t.Fatal(err)
	}
}

func TestBuildLeads(t *testing.T) {
	st := testStore(t)
	cfg := Config{SuppressionDays: 30, ScoreThreshold: 50, ExcludedPKDPrefixes: []string{"77", "78"}}

	// 1: gov company above threshold -> qualified
	govID, _ := st.CreateCompany("1111111111", "GoodCo", "goodco", "verified")
	hi := 80
	addOffer(t, st, "cbop", "cbop:1", govID, "Spawacz", &hi)

	// 2: gov company below threshold -> not qualified
	lowID, _ := st.CreateCompany("2222222222", "LowCo", "lowco", "verified")
	lo := 10
	addOffer(t, st, "cbop", "cbop:2", lowID, "Pakowacz", &lo)

	// 3: OLX-only company (nil score) -> qualified by default
	olxID, _ := st.CreateCompany("3333333333", "OlxCo", "olxco", "verified")
	addOffer(t, st, "olx", "olx:3", olxID, "Monter", nil)

	// 4: staffing agency -> suppressed by PKD
	agID, _ := st.CreateCompany("4444444444", "AgencyCo", "agencyco", "verified")
	st.FillCompanyFields(agID, map[string]string{"pkd_main": "78.20.Z"})
	addOffer(t, st, "cbop", "cbop:4", agID, "Operator", &hi)

	stats, err := BuildLeads(st, cfg, 1)
	if err != nil {
		t.Fatalf("BuildLeads: %v", err)
	}
	if stats.Created != 3 || stats.SuppressedPKD != 1 {
		t.Errorf("stats = %+v", stats)
	}

	var qualified int
	st.DB.QueryRow(`SELECT COUNT(*) FROM leads WHERE qualified=1 AND status='new'`).Scan(&qualified)
	if qualified != 2 { // GoodCo + OlxCo
		t.Errorf("qualified new leads = %d, want 2", qualified)
	}

	// Re-run: same offers must not create duplicate leads.
	stats2, _ := BuildLeads(st, cfg, 2)
	if stats2.Created != 0 {
		t.Errorf("re-run created %d leads", stats2.Created)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/qualify/ -v`
Expected: FAIL.

- [ ] **Step 3: Implement store lead functions**

`internal/store/leads.go`:

```go
package store

import "encoding/json"

// LeadCandidate is a company with offers not yet covered by any lead.
type LeadCandidate struct {
	Company   Company
	Positions []string
	MaxScore  *int // nil when no offer carries a score (OLX-only)
}

// LeadCandidates returns companies that have attached offers newer than the
// company's most recent lead (or that never had a lead).
func (s *Store) LeadCandidates() ([]LeadCandidate, error) {
	rows, err := s.DB.Query(`
		SELECT c.id, COALESCE(c.nip,''), c.name, c.normalized_name, c.nip_status,
		       c.address, c.regon, c.krs, c.legal_form, c.pkd_main, c.company_size,
		       c.website, c.email, c.phone, c.board_members, c.first_seen, c.last_seen
		FROM companies c
		WHERE EXISTS (
		  SELECT 1 FROM raw_offers o
		  WHERE o.company_id = c.id
		    AND o.ingested_at > COALESCE(
		      (SELECT MAX(l.created_at) FROM leads l WHERE l.company_id = c.id), '')
		)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LeadCandidate
	for rows.Next() {
		var c Company
		if err := rows.Scan(&c.ID, &c.NIP, &c.Name, &c.NormalizedName, &c.NIPStatus,
			&c.Address, &c.REGON, &c.KRS, &c.LegalForm, &c.PKDMain, &c.CompanySize,
			&c.Website, &c.Email, &c.Phone, &c.BoardMembers, &c.FirstSeen, &c.LastSeen); err != nil {
			return nil, err
		}
		out = append(out, LeadCandidate{Company: c})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		prows, err := s.DB.Query(`SELECT DISTINCT COALESCE(position,''), score
			FROM raw_offers WHERE company_id = ?`, out[i].Company.ID)
		if err != nil {
			return nil, err
		}
		for prows.Next() {
			var pos string
			var score *int
			if err := prows.Scan(&pos, &score); err != nil {
				prows.Close()
				return nil, err
			}
			if pos != "" {
				out[i].Positions = append(out[i].Positions, pos)
			}
			if score != nil && (out[i].MaxScore == nil || *score > *out[i].MaxScore) {
				out[i].MaxScore = score
			}
		}
		prows.Close()
	}
	return out, nil
}

func (s *Store) CreateLead(companyID int64, runID int64, positions []string, score *int, qualified bool, status, reason string) (int64, error) {
	pj, _ := json.Marshal(positions)
	res, err := s.DB.Exec(`INSERT INTO leads
		(company_id, run_id, positions, score, qualified, status, reason, created_at)
		VALUES (?,?,?,?,?,?,?,datetime('now'))`,
		companyID, runID, string(pj), score, qualified, status, reason)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// DeliveredWithin reports whether the company had any successful delivery
// in the last `days` days.
func (s *Store) DeliveredWithin(companyID int64, days int) (bool, error) {
	var n int
	err := s.DB.QueryRow(`SELECT COUNT(*) FROM deliveries d
		JOIN leads l ON l.id = d.lead_id
		WHERE l.company_id = ? AND d.status = 'ok'
		  AND d.delivered_at > datetime('now', ?)`,
		companyID, "-"+itoa(days)+" days").Scan(&n)
	return n > 0, err
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	digits := []byte{}
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
```

- [ ] **Step 4: Implement BuildLeads**

`internal/qualify/qualify.go`:

```go
// Package qualify turns enriched companies with fresh offers into
// deliverable leads, applying suppression and qualification rules.
package qualify

import (
	"fmt"
	"strings"

	"github.com/hrkono/lead-engine/internal/store"
)

type Config struct {
	SuppressionDays     int
	ScoreThreshold      int
	ExcludedPKDPrefixes []string
}

type Stats struct {
	Created          int
	SuppressedPKD    int
	SuppressedRecent int
	Unqualified      int
}

func BuildLeads(st *store.Store, cfg Config, runID int64) (Stats, error) {
	var stats Stats
	cands, err := st.LeadCandidates()
	if err != nil {
		return stats, fmt.Errorf("qualify: %w", err)
	}
	for _, cand := range cands {
		c := cand.Company

		if pref := excludedPKD(c.PKDMain, cfg.ExcludedPKDPrefixes); pref != "" {
			if _, err := st.CreateLead(c.ID, runID, cand.Positions, cand.MaxScore,
				false, "suppressed", "agency PKD "+c.PKDMain); err != nil {
				return stats, err
			}
			stats.SuppressedPKD++
			continue
		}
		recent, err := st.DeliveredWithin(c.ID, cfg.SuppressionDays)
		if err != nil {
			return stats, err
		}
		if recent {
			if _, err := st.CreateLead(c.ID, runID, cand.Positions, cand.MaxScore,
				false, "suppressed", "delivered recently"); err != nil {
				return stats, err
			}
			stats.SuppressedRecent++
			continue
		}
		// Scored (gov) leads must clear the threshold; unscored (OLX-only)
		// leads qualify by default — OLX has no scorer yet.
		qualified := cand.MaxScore == nil || *cand.MaxScore >= cfg.ScoreThreshold
		if _, err := st.CreateLead(c.ID, runID, cand.Positions, cand.MaxScore,
			qualified, "new", ""); err != nil {
			return stats, err
		}
		stats.Created++
		if !qualified {
			stats.Unqualified++
		}
	}
	return stats, nil
}

func excludedPKD(pkd string, prefixes []string) string {
	pkd = strings.ReplaceAll(pkd, ".", "")
	for _, p := range prefixes {
		if p != "" && strings.HasPrefix(pkd, p) {
			return p
		}
	}
	return ""
}
```

- [ ] **Step 5: Run tests, verify pass**

Run: `go test ./internal/qualify/ ./internal/store/ -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/qualify/ internal/store/leads.go
git commit -m "feat: lead building with PKD/suppression rules and qualification"
```

---

### Task 14: Digest renderer (golden file)

**Files:**
- Create: `internal/deliver/digest.go`
- Create: `internal/deliver/testdata/digest.golden`
- Test: `internal/deliver/digest_test.go`

- [ ] **Step 1: Write the failing test**

`internal/deliver/digest_test.go`:

```go
package deliver

import (
	"flag"
	"os"
	"testing"
)

var update = flag.Bool("update", false, "rewrite golden files")

func TestRenderDigest(t *testing.T) {
	hi := 92
	verified := []LeadView{{
		Company: "Stalmet Sp. z o.o.", NIP: "1234567890",
		Positions: []string{"Operator maszyn CNC", "Operator wtryskarki"},
		Location:  "Warszawa", Phone: "+48221112233",
		Email: "biuro@stalmet.example", Website: "stalmet.example",
		Score: &hi, Board: []string{"JAN KOWALSKI (PREZES ZARZĄDU)"},
	}}
	unverified := []LeadView{{
		Company: "Mała Firma Jan Nowak", Positions: []string{"Monter"},
		Location: "Radom", Phone: "+48501502503",
	}}
	stats := RunStats{OffersCBOP: 120, OffersOLX: 45, SpendPLN: 2.5, CapPLN: 10, Warnings: []string{"olx scrape: 1 page failed"}}

	got := RenderDigest("2026-06-10", verified, unverified, stats)

	golden := "testdata/digest.golden"
	if *update {
		os.WriteFile(golden, []byte(got), 0o644)
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("read golden (run once with -update): %v", err)
	}
	if got != string(want) {
		t.Errorf("digest mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}
```

- [ ] **Step 2: Implement renderer**

`internal/deliver/digest.go`:

```go
// Package deliver renders and sends the daily outputs: Signal digest and
// Pipedrive pushes.
package deliver

import (
	"fmt"
	"strings"
)

type LeadView struct {
	Company   string
	NIP       string
	Positions []string
	Location  string
	Phone     string
	Email     string
	Website   string
	Score     *int
	Board     []string
}

type RunStats struct {
	OffersCBOP int
	OffersOLX  int
	SpendPLN   float64
	CapPLN     float64
	Warnings   []string
}

func RenderDigest(date string, verified, unverified []LeadView, stats RunStats) string {
	var b strings.Builder
	fmt.Fprintf(&b, "LEADY %s\n", date)
	fmt.Fprintf(&b, "Zweryfikowane: %d | Niezweryfikowane: %d\n\n", len(verified), len(unverified))

	if len(verified) > 0 {
		b.WriteString("== ZWERYFIKOWANE ==\n")
		for i, l := range verified {
			fmt.Fprintf(&b, "%d. %s", i+1, l.Company)
			if l.Score != nil {
				fmt.Fprintf(&b, " [score %d]", *l.Score)
			}
			b.WriteString("\n")
			fmt.Fprintf(&b, "   Szuka: %s\n", strings.Join(l.Positions, "; "))
			if l.Location != "" {
				fmt.Fprintf(&b, "   Lokalizacja: %s\n", l.Location)
			}
			fmt.Fprintf(&b, "   NIP: %s\n", l.NIP)
			if l.Phone != "" {
				fmt.Fprintf(&b, "   Tel: %s\n", l.Phone)
			}
			if l.Email != "" {
				fmt.Fprintf(&b, "   Email: %s\n", l.Email)
			}
			if l.Website != "" {
				fmt.Fprintf(&b, "   WWW: %s\n", l.Website)
			}
			if len(l.Board) > 0 {
				fmt.Fprintf(&b, "   Zarząd: %s\n", strings.Join(l.Board, ", "))
			}
			b.WriteString("\n")
		}
	}

	if len(unverified) > 0 {
		b.WriteString("== NIEZWERYFIKOWANE (brak NIP) ==\n")
		for i, l := range unverified {
			fmt.Fprintf(&b, "%d. %s — %s", i+1, l.Company, strings.Join(l.Positions, "; "))
			if l.Location != "" {
				fmt.Fprintf(&b, " (%s)", l.Location)
			}
			b.WriteString("\n")
			if l.Phone != "" {
				fmt.Fprintf(&b, "   Tel: %s\n", l.Phone)
			}
		}
		b.WriteString("\n")
	}

	fmt.Fprintf(&b, "— ofert: CBOP %d, OLX %d | BizRaport: %.2f/%.2f PLN\n",
		stats.OffersCBOP, stats.OffersOLX, stats.SpendPLN, stats.CapPLN)
	for _, w := range stats.Warnings {
		fmt.Fprintf(&b, "⚠ %s\n", w)
	}
	return b.String()
}
```

- [ ] **Step 3: Generate golden, then verify pass**

Run: `go test ./internal/deliver/ -run TestRenderDigest -update && go test ./internal/deliver/ -v`
Expected: PASS; inspect `testdata/digest.golden` by eye — it should read like a sane sales digest.

- [ ] **Step 4: Commit**

```bash
git add internal/deliver/
git commit -m "feat: Signal digest renderer with golden-file test"
```

---

### Task 15: Signal client + delivery stage

**Files:**
- Create: `internal/deliver/signal.go`
- Test: `internal/deliver/signal_test.go`

- [ ] **Step 1: Write the failing test**

`internal/deliver/signal_test.go`:

```go
package deliver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestSignalSend(t *testing.T) {
	var got []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/send" {
			http.NotFound(w, r)
			return
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		got = append(got, body)
		w.WriteHeader(201)
	}))
	defer srv.Close()

	c := &SignalClient{APIURL: srv.URL, Number: "+48111222333", Recipients: []string{"group.abc"}}
	if err := c.Send(context.Background(), "hello team"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(got) != 1 || got[0]["message"] != "hello team" || got[0]["number"] != "+48111222333" {
		t.Errorf("payload = %+v", got)
	}

	// Long messages split into <=4000-char parts.
	got = nil
	if err := c.Send(context.Background(), strings.Repeat("x", 9000)); err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Errorf("parts = %d, want 3", len(got))
	}

	// Polish (multi-byte) text: no part may end mid-rune.
	got = nil
	if err := c.Send(context.Background(), "a"+strings.Repeat("ł", 4500)); err != nil {
		t.Fatal(err)
	}
	for i, m := range got {
		if !utf8.ValidString(m["message"].(string)) {
			t.Errorf("part %d is not valid UTF-8", i)
		}
	}
}

func TestSignalRetriesThenFails(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		http.Error(w, "boom", 500)
	}))
	defer srv.Close()
	c := &SignalClient{APIURL: srv.URL, Number: "+48111222333",
		Recipients: []string{"g"}, Backoff: time.Millisecond}
	if err := c.Send(context.Background(), "x"); err == nil {
		t.Fatal("expected error")
	}
	if calls != 3 {
		t.Errorf("calls = %d, want 3", calls)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/deliver/ -run TestSignal -v`
Expected: FAIL.

- [ ] **Step 3: Implement**

`internal/deliver/signal.go`:

```go
package deliver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
	"unicode/utf8"
)

const signalMaxPart = 4000

type SignalClient struct {
	APIURL     string
	Number     string
	Recipients []string
	HTTP       *http.Client
	Backoff    time.Duration // base backoff; default 2s
}

func (c *SignalClient) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 60 * time.Second}
}

func (c *SignalClient) backoff() time.Duration {
	if c.Backoff > 0 {
		return c.Backoff
	}
	return 2 * time.Second
}

func splitMessage(msg string, max int) []string {
	if len(msg) <= max {
		return []string{msg}
	}
	var parts []string
	for len(msg) > 0 {
		n := max
		if n >= len(msg) {
			n = len(msg)
		} else {
			// Never split a multi-byte UTF-8 rune across parts.
			for n > 0 && !utf8.RuneStart(msg[n]) {
				n--
			}
			if n == 0 {
				n = max
			}
		}
		parts = append(parts, msg[:n])
		msg = msg[n:]
	}
	for i := range parts {
		parts[i] = fmt.Sprintf("(%d/%d)\n%s", i+1, len(parts), parts[i])
	}
	return parts
}

func (c *SignalClient) Send(ctx context.Context, msg string) error {
	for _, part := range splitMessage(msg, signalMaxPart) {
		body, _ := json.Marshal(map[string]any{
			"message":    part,
			"number":     c.Number,
			"recipients": c.Recipients,
		})
		var lastErr error
		for attempt := 0; attempt < 3; attempt++ {
			if attempt > 0 {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(c.backoff() << attempt):
				}
			}
			req, err := http.NewRequestWithContext(ctx, http.MethodPost,
				c.APIURL+"/v2/send", bytes.NewReader(body))
			if err != nil {
				return err
			}
			req.Header.Set("Content-Type", "application/json")
			resp, err := c.http().Do(req)
			if err != nil {
				lastErr = err
				continue
			}
			resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				lastErr = nil
				break
			}
			lastErr = fmt.Errorf("signal: status %d", resp.StatusCode)
		}
		if lastErr != nil {
			return fmt.Errorf("signal send: %w", lastErr)
		}
	}
	return nil
}
```

- [ ] **Step 4: Run tests, verify pass**

Run: `go test ./internal/deliver/ -v`
Expected: PASS (digest + signal tests). Note: retry count in the failing test is 3 calls per part — the first part fails all 3 attempts and Send returns, so total calls = 3.

- [ ] **Step 5: Commit**

```bash
git add internal/deliver/signal.go internal/deliver/signal_test.go
git commit -m "feat: Signal client (signal-cli-rest-api) with splitting and retries"
```

---

### Task 16: Pipedrive client + push stage

**Files:**
- Create: `internal/deliver/pipedrive.go`
- Create: `internal/deliver/pipedrive_push.go`
- Test: `internal/deliver/pipedrive_test.go`

- [ ] **Step 1: Write the failing test**

`internal/deliver/pipedrive_test.go`:

```go
package deliver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// fakePipedrive implements just enough of the v1 API:
// org search (miss then hit), org create, deal list/create, note create.
type fakePipedrive struct {
	orgs  map[string]int64 // nip -> org id
	deals map[int64]int64  // org id -> open deal id
	notes []string
	next  int64
}

func (f *fakePipedrive) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/organizations/search":
			nip := r.URL.Query().Get("term")
			if id, ok := f.orgs[nip]; ok {
				fmt.Fprintf(w, `{"success":true,"data":{"items":[{"item":{"id":%d}}]}}`, id)
			} else {
				fmt.Fprint(w, `{"success":true,"data":{"items":[]}}`)
			}
		case r.URL.Path == "/v1/organizations" && r.Method == "POST":
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			f.next++
			f.orgs[body["nip_key"].(string)] = f.next
			fmt.Fprintf(w, `{"success":true,"data":{"id":%d}}`, f.next)
		case r.URL.Path == "/v1/deals" && r.Method == "GET":
			// org_id query; return open deal if present
			var orgID int64
			fmt.Sscanf(r.URL.Query().Get("org_id"), "%d", &orgID)
			if id, ok := f.deals[orgID]; ok {
				fmt.Fprintf(w, `{"success":true,"data":[{"id":%d,"status":"open"}]}`, id)
			} else {
				fmt.Fprint(w, `{"success":true,"data":[]}`)
			}
		case r.URL.Path == "/v1/deals" && r.Method == "POST":
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			f.next++
			f.deals[int64(body["org_id"].(float64))] = f.next
			fmt.Fprintf(w, `{"success":true,"data":{"id":%d}}`, f.next)
		case r.URL.Path == "/v1/notes" && r.Method == "POST":
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			f.notes = append(f.notes, body["content"].(string))
			fmt.Fprint(w, `{"success":true,"data":{"id":1}}`)
		default:
			http.NotFound(w, r)
		}
	}
}

func newTestPD(srvURL string) *PipedriveClient {
	return &PipedriveClient{
		BaseURL: srvURL, Token: "tok",
		FieldKeys: map[string]string{"nip": "nip_key", "regon": "regon_key",
			"krs": "krs_key", "pkd": "pkd_key", "board_members": "board_key", "source": "source_key"},
	}
}

func TestPushCreatesOrgAndDeal(t *testing.T) {
	f := &fakePipedrive{orgs: map[string]int64{}, deals: map[int64]int64{}}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()

	pd := newTestPD(srv.URL)
	res, err := pd.PushLead(context.Background(), PipedriveLead{
		Company: "Stalmet Sp. z o.o.", NIP: "1234567890",
		Positions: []string{"Operator CNC"}, NoteContent: "details...",
	})
	if err != nil {
		t.Fatalf("PushLead: %v", err)
	}
	if res.OrgID == 0 || res.DealID == 0 || !res.OrgCreated {
		t.Errorf("res = %+v", res)
	}
}

func TestPushExistingOrgOpenDealAddsNote(t *testing.T) {
	f := &fakePipedrive{orgs: map[string]int64{"1234567890": 7}, deals: map[int64]int64{7: 42}}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()

	pd := newTestPD(srv.URL)
	res, err := pd.PushLead(context.Background(), PipedriveLead{
		Company: "Stalmet", NIP: "1234567890",
		Positions: []string{"Monter"}, NoteContent: "hiring again",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.OrgCreated || res.DealCreated || res.DealID != 42 {
		t.Errorf("res = %+v", res)
	}
	if len(f.notes) != 1 {
		t.Errorf("notes = %v", f.notes)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/deliver/ -run TestPush -v`
Expected: FAIL.

- [ ] **Step 3: Implement the client**

`internal/deliver/pipedrive.go`:

```go
package deliver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type PipedriveClient struct {
	BaseURL   string // default https://api.pipedrive.com
	Token     string
	StageID   int64
	FieldKeys map[string]string // nip, regon, krs, pkd, board_members, source
	HTTP      *http.Client
}

type PipedriveLead struct {
	Company     string
	NIP         string
	REGON       string
	KRS         string
	PKD         string
	Address     string
	Website     string
	Board       []string
	Positions   []string
	NoteContent string
}

type PushResult struct {
	OrgID       int64
	DealID      int64
	OrgCreated  bool
	DealCreated bool
}

func (c *PipedriveClient) base() string {
	if c.BaseURL != "" {
		return c.BaseURL
	}
	return "https://api.pipedrive.com"
}

func (c *PipedriveClient) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 30 * time.Second}
}

func (c *PipedriveClient) do(ctx context.Context, method, path string, query url.Values, body any, out any) error {
	if query == nil {
		query = url.Values{}
	}
	query.Set("api_token", c.Token)
	var rdr *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base()+path+"?"+query.Encode(), rdr)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http().Do(req)
	if err != nil {
		return fmt.Errorf("pipedrive %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("pipedrive %s: status %d", path, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *PipedriveClient) FindOrgByNIP(ctx context.Context, nip string) (int64, bool, error) {
	var out struct {
		Data struct {
			Items []struct {
				Item struct {
					ID int64 `json:"id"`
				} `json:"item"`
			} `json:"items"`
		} `json:"data"`
	}
	q := url.Values{"term": {nip}, "fields": {"custom_fields"}, "exact_match": {"true"}}
	if err := c.do(ctx, http.MethodGet, "/v1/organizations/search", q, nil, &out); err != nil {
		return 0, false, err
	}
	if len(out.Data.Items) == 0 {
		return 0, false, nil
	}
	return out.Data.Items[0].Item.ID, true, nil
}

func (c *PipedriveClient) CreateOrg(ctx context.Context, l PipedriveLead) (int64, error) {
	body := map[string]any{"name": l.Company, "address": l.Address}
	set := func(field, val string) {
		if key := c.FieldKeys[field]; key != "" && val != "" {
			body[key] = val
		}
	}
	set("nip", l.NIP)
	set("regon", l.REGON)
	set("krs", l.KRS)
	set("pkd", l.PKD)
	set("board_members", strings.Join(l.Board, ", "))
	set("source", "lead-engine")
	var out struct {
		Data struct {
			ID int64 `json:"id"`
		} `json:"data"`
	}
	if err := c.do(ctx, http.MethodPost, "/v1/organizations", nil, body, &out); err != nil {
		return 0, err
	}
	return out.Data.ID, nil
}

func (c *PipedriveClient) OpenDealID(ctx context.Context, orgID int64) (int64, bool, error) {
	var out struct {
		Data []struct {
			ID     int64  `json:"id"`
			Status string `json:"status"`
		} `json:"data"`
	}
	q := url.Values{"org_id": {fmt.Sprint(orgID)}, "status": {"open"}}
	if err := c.do(ctx, http.MethodGet, "/v1/deals", q, nil, &out); err != nil {
		return 0, false, err
	}
	if len(out.Data) == 0 {
		return 0, false, nil
	}
	return out.Data[0].ID, true, nil
}

func (c *PipedriveClient) CreateDeal(ctx context.Context, orgID int64, title string) (int64, error) {
	body := map[string]any{"title": title, "org_id": orgID}
	if c.StageID != 0 {
		body["stage_id"] = c.StageID
	}
	var out struct {
		Data struct {
			ID int64 `json:"id"`
		} `json:"data"`
	}
	if err := c.do(ctx, http.MethodPost, "/v1/deals", nil, body, &out); err != nil {
		return 0, err
	}
	return out.Data.ID, nil
}

func (c *PipedriveClient) AddNote(ctx context.Context, dealID, orgID int64, content string) error {
	body := map[string]any{"content": content}
	if dealID != 0 {
		body["deal_id"] = dealID
	} else {
		body["org_id"] = orgID
	}
	var out struct {
		Data struct {
			ID int64 `json:"id"`
		} `json:"data"`
	}
	return c.do(ctx, http.MethodPost, "/v1/notes", nil, body, &out)
}

// PushLead applies the spec's duplicate policy: never duplicate orgs; new
// deal only when no open deal exists; otherwise note the open deal.
func (c *PipedriveClient) PushLead(ctx context.Context, l PipedriveLead) (PushResult, error) {
	var res PushResult
	orgID, found, err := c.FindOrgByNIP(ctx, l.NIP)
	if err != nil {
		return res, err
	}
	if !found {
		orgID, err = c.CreateOrg(ctx, l)
		if err != nil {
			return res, err
		}
		res.OrgCreated = true
	}
	res.OrgID = orgID

	title := strings.Join(l.Positions, ", ") + " — " + l.Company
	if found {
		if dealID, open, err := c.OpenDealID(ctx, orgID); err != nil {
			return res, err
		} else if open {
			res.DealID = dealID
			return res, c.AddNote(ctx, dealID, orgID, l.NoteContent)
		}
	}
	dealID, err := c.CreateDeal(ctx, orgID, title)
	if err != nil {
		return res, err
	}
	res.DealID = dealID
	res.DealCreated = true
	return res, c.AddNote(ctx, dealID, orgID, l.NoteContent)
}
```

- [ ] **Step 4: Run tests, verify pass**

Run: `go test ./internal/deliver/ -v`
Expected: PASS (all deliver tests).

- [ ] **Step 5: Add the org-fields setup helper**

Append to `internal/deliver/pipedrive.go`:

```go
// EnsureOrgFields creates the custom Organization fields lead-engine needs
// and returns name -> field key. Used by `lead-engine pipedrive setup`;
// run once, then persist the keys in config [pipedrive.field_keys].
func (c *PipedriveClient) EnsureOrgFields(ctx context.Context) (map[string]string, error) {
	wanted := map[string]string{ // config name -> Pipedrive field label
		"nip": "NIP", "regon": "REGON", "krs": "KRS",
		"pkd": "PKD", "board_members": "Zarząd", "source": "Lead Source",
	}
	keys := map[string]string{}
	for name, label := range wanted {
		body := map[string]any{"name": label, "field_type": "varchar"}
		var out struct {
			Data struct {
				Key string `json:"key"`
			} `json:"data"`
		}
		if err := c.do(ctx, http.MethodPost, "/v1/organizationFields", nil, body, &out); err != nil {
			return keys, fmt.Errorf("create field %s: %w", label, err)
		}
		keys[name] = out.Data.Key
	}
	return keys, nil
}
```

Run: `go build ./...` — expected: clean build.

- [ ] **Step 6: Commit**

```bash
git add internal/deliver/pipedrive.go internal/deliver/pipedrive_test.go
git commit -m "feat: Pipedrive client with org-dedup push policy and field setup"
```

---

### Task 17: Runner — stages, resume, dry-run, scraper subprocesses

**Files:**
- Create: `internal/run/runner.go`
- Create: `internal/store/runs.go`
- Test: `internal/run/runner_test.go`

- [ ] **Step 1: Write the failing test**

`internal/run/runner_test.go`:

```go
package run

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/hrkono/lead-engine/internal/store"
)

func testStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "leads.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestRunnerRecordsStagesAndResumes(t *testing.T) {
	st := testStore(t)
	calls := map[string]int{}
	mk := func(name string, fail bool) Stage {
		return Stage{Name: name, Fn: func(ctx context.Context) error {
			calls[name]++
			if fail {
				return errors.New("boom")
			}
			return nil
		}}
	}

	r := &Runner{Store: st, Stages: []Stage{mk("a", false), mk("b", true), mk("c", false)}}
	err := r.Run(context.Background())
	if err == nil {
		t.Fatal("expected failure from stage b")
	}
	if calls["a"] != 1 || calls["b"] != 1 || calls["c"] != 1 {
		t.Errorf("calls = %v (failures must not stop later stages)", calls)
	}

	// Resume: a and c completed, only b re-runs.
	r2 := &Runner{Store: st, Stages: []Stage{mk("a", false), mk("b", false), mk("c", false)}, Resume: true}
	r2.Stages[1].Fn = func(ctx context.Context) error { calls["b"]++; return nil }
	if err := r2.Run(context.Background()); err != nil {
		t.Fatalf("resume run: %v", err)
	}
	if calls["a"] != 1 || calls["b"] != 2 || calls["c"] != 1 {
		t.Errorf("after resume calls = %v", calls)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/run/ -v`
Expected: FAIL.

- [ ] **Step 3: Implement run bookkeeping + runner**

`internal/store/runs.go`:

```go
package store

import "database/sql"

func (s *Store) StartRun() (int64, error) {
	res, err := s.DB.Exec(`INSERT INTO runs (started_at) VALUES (datetime('now'))`)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) FinishRun(runID int64, status string) error {
	_, err := s.DB.Exec(`UPDATE runs SET finished_at=datetime('now'), status=? WHERE id=?`, status, runID)
	return err
}

func (s *Store) RecordStage(runID int64, stage, status, detail string) error {
	_, err := s.DB.Exec(`INSERT INTO run_stages (run_id, stage, status, detail, ended_at)
		VALUES (?,?,?,?,datetime('now'))
		ON CONFLICT(run_id, stage) DO UPDATE SET
		  status=excluded.status, detail=excluded.detail, ended_at=excluded.ended_at`,
		runID, stage, status, detail)
	return err
}

// LastFailedRun returns the most recent run with status 'failed' plus the
// set of stages that completed ok in it. ok==false when there is none.
func (s *Store) LastFailedRun() (int64, map[string]bool, bool, error) {
	var runID int64
	err := s.DB.QueryRow(`SELECT id FROM runs WHERE status='failed'
		ORDER BY id DESC LIMIT 1`).Scan(&runID)
	if err == sql.ErrNoRows {
		return 0, nil, false, nil
	}
	if err != nil {
		return 0, nil, false, err
	}
	rows, err := s.DB.Query(`SELECT stage FROM run_stages WHERE run_id=? AND status='ok'`, runID)
	if err != nil {
		return 0, nil, false, err
	}
	defer rows.Close()
	done := map[string]bool{}
	for rows.Next() {
		var st string
		if err := rows.Scan(&st); err != nil {
			return 0, nil, false, err
		}
		done[st] = true
	}
	return runID, done, true, rows.Err()
}
```

`internal/run/runner.go`:

```go
// Package run sequences the pipeline stages with per-stage status
// recording. The pipeline degrades rather than dies: a failing stage is
// recorded and the run continues, finishing with a non-zero error so cron
// alerts — but later stages still execute on whatever data exists.
package run

import (
	"context"
	"fmt"
	"log"

	"github.com/hrkono/lead-engine/internal/store"
)

type Stage struct {
	Name string
	Fn   func(ctx context.Context) error
}

type Runner struct {
	Store  *store.Store
	Stages []Stage
	Resume bool
}

func (r *Runner) Run(ctx context.Context) error {
	var skipDone map[string]bool
	if r.Resume {
		_, done, ok, err := r.Store.LastFailedRun()
		if err != nil {
			return err
		}
		if ok {
			skipDone = done
		}
	}
	runID, err := r.Store.StartRun()
	if err != nil {
		return err
	}
	var failures []string
	for _, stage := range r.Stages {
		if skipDone[stage.Name] {
			r.Store.RecordStage(runID, stage.Name, "ok", "skipped (resume)")
			continue
		}
		log.Printf("stage %s: start", stage.Name)
		if err := stage.Fn(ctx); err != nil {
			log.Printf("stage %s: FAILED: %v", stage.Name, err)
			r.Store.RecordStage(runID, stage.Name, "failed", err.Error())
			failures = append(failures, fmt.Sprintf("%s: %v", stage.Name, err))
			continue
		}
		r.Store.RecordStage(runID, stage.Name, "ok", "")
		log.Printf("stage %s: ok", stage.Name)
	}
	if len(failures) > 0 {
		r.Store.FinishRun(runID, "failed")
		return fmt.Errorf("run %d: %d stage(s) failed: %v", runID, len(failures), failures)
	}
	return r.Store.FinishRun(runID, "ok")
}
```

- [ ] **Step 4: Run tests, verify pass**

Run: `go test ./internal/run/ -v`
Expected: PASS.

- [ ] **Step 5: Add the scraper subprocess stage helper**

Append to `internal/run/runner.go`:

```go
// ScraperStage runs an external scraper command and verifies its export
// file exists afterwards. cmd[0] is the binary, the rest are args.
func ScraperStage(name string, cmd []string, exportPath string) Stage {
	return Stage{Name: name, Fn: func(ctx context.Context) error {
		if len(cmd) == 0 {
			return fmt.Errorf("%s: no command configured", name)
		}
		c := exec.CommandContext(ctx, cmd[0], cmd[1:]...)
		out, err := c.CombinedOutput()
		if err != nil {
			return fmt.Errorf("%s: %w (output: %.500s)", name, err, string(out))
		}
		if _, err := os.Stat(exportPath); err != nil {
			return fmt.Errorf("%s: export file missing: %w", name, err)
		}
		return nil
	}}
}
```

(add `os` and `os/exec` to imports). Run `go build ./...` — clean.

- [ ] **Step 6: Commit**

```bash
git add internal/run/ internal/store/runs.go
git commit -m "feat: stage runner with degrade-don't-die semantics and resume"
```

---

### Task 18: CLI binary, stage wiring, deploy docs, end-to-end dry run

**Files:**
- Create: `cmd/lead-engine/main.go`
- Create: `internal/cli/root.go`, `internal/cli/run.go`, `internal/cli/pipedrive_setup.go`
- Create: `docs/DEPLOY.md`, `config.example.toml`
- Test: end-to-end dry run against fixtures

- [ ] **Step 1: Add cobra and the CLI skeleton**

```bash
go get github.com/spf13/cobra@latest
```

`cmd/lead-engine/main.go`:

```go
package main

import (
	"fmt"
	"os"

	"github.com/hrkono/lead-engine/internal/cli"
)

func main() {
	if err := cli.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
```

`internal/cli/root.go`:

```go
// Package cli wires config, store, clients, and stages into the
// lead-engine command tree: `run` and `pipedrive setup`.
package cli

import "github.com/spf13/cobra"

var rootCmd = &cobra.Command{
	Use:           "lead-engine",
	Short:         "Unified B2B lead pipeline: scrape, unify, enrich, deliver",
	SilenceUsage:  true,
	SilenceErrors: true,
}

func Execute() error {
	rootCmd.AddCommand(newRunCmd(), newPipedriveCmd())
	return rootCmd.Execute()
}
```

`internal/cli/run.go`:

```go
package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/hrkono/lead-engine/internal/config"
	"github.com/hrkono/lead-engine/internal/deliver"
	"github.com/hrkono/lead-engine/internal/enrich"
	"github.com/hrkono/lead-engine/internal/enrich/bizraport"
	"github.com/hrkono/lead-engine/internal/enrich/krs"
	"github.com/hrkono/lead-engine/internal/enrich/regon"
	"github.com/hrkono/lead-engine/internal/ingest"
	"github.com/hrkono/lead-engine/internal/match"
	"github.com/hrkono/lead-engine/internal/qualify"
	"github.com/hrkono/lead-engine/internal/run"
	"github.com/hrkono/lead-engine/internal/store"
)

func newRunCmd() *cobra.Command {
	var cfgPath string
	var dryRun, resume, skipScrape bool
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Execute the daily pipeline",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return err
			}
			st, err := store.Open(cfg.DBPath)
			if err != nil {
				return err
			}
			defer st.Close()
			return runPipeline(cmd.Context(), cfg, st, dryRun, resume, skipScrape, cmd)
		},
	}
	cmd.Flags().StringVar(&cfgPath, "config", "/etc/lead-engine/config.toml", "config file")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "no Signal/Pipedrive sends; digest to stdout")
	cmd.Flags().BoolVar(&resume, "resume", false, "skip stages completed in the last failed run")
	cmd.Flags().BoolVar(&skipScrape, "skip-scrape", false, "ingest existing export files without scraping")
	return cmd
}

func runPipeline(ctx context.Context, cfg *config.Config, st *store.Store, dryRun, resume, skipScrape bool, cmd *cobra.Command) error {
	runID, _ := nextRunID(st)
	stats := deliver.RunStats{CapPLN: cfg.Bizraport.DailyCapPLN}
	warn := func(format string, a ...any) {
		stats.Warnings = append(stats.Warnings, fmt.Sprintf(format, a...))
	}

	bz := bizraport.New(bizraport.Options{Email: cfg.Bizraport.Email, Password: cfg.Bizraport.Password})
	rg := &regon.Client{APIKey: cfg.Regon.APIKey, Endpoint: cfg.Regon.Endpoint}
	kc := &krs.Client{}

	stages := []run.Stage{}
	if !skipScrape {
		stages = append(stages,
			run.ScraperStage("scrape-gov", cfg.Scrapers.GovCmd, cfg.Scrapers.GovExport),
			run.ScraperStage("scrape-olx", cfg.Scrapers.OlxCmd, cfg.Scrapers.OlxExport),
		)
	}
	stages = append(stages,
		run.Stage{Name: "ingest", Fn: func(ctx context.Context) error {
			n1, err1 := ingest.Ingest(st, cfg.Scrapers.GovExport)
			stats.OffersCBOP = n1
			n2, err2 := ingest.Ingest(st, cfg.Scrapers.OlxExport)
			stats.OffersOLX = n2
			if err1 != nil {
				warn("gov ingest: %v", err1)
			}
			if err2 != nil {
				warn("olx ingest: %v", err2)
			}
			if err1 != nil && err2 != nil {
				return fmt.Errorf("both ingests failed: %v / %v", err1, err2)
			}
			return nil
		}},
		run.Stage{Name: "match", Fn: func(ctx context.Context) error {
			_, err := match.Attach(st)
			return err
		}},
		run.Stage{Name: "resolve-nip", Fn: func(ctx context.Context) error {
			if !bz.HasCredentials() {
				warn("bizraport: no credentials, skipping NIP resolution")
				return nil
			}
			rs, err := enrich.ResolveNIPs(ctx, st, bz, enrich.ResolveConfig{
				DailyCapPLN:   cfg.Bizraport.DailyCapPLN,
				CostPerRowPLN: cfg.Bizraport.CostPerRowPLN,
				MaxCandidates: cfg.Bizraport.MaxCandidates,
			})
			if rs.SkippedBudget > 0 {
				warn("bizraport: %d companies skipped (budget cap)", rs.SkippedBudget)
			}
			return err
		}},
		run.Stage{Name: "enrich", Fn: func(ctx context.Context) error {
			es, err := enrich.Enrich(ctx, st, rg, kc)
			if es.Errors > 0 {
				warn("enrichment: %d lookups failed (partial data shipped)", es.Errors)
			}
			return err
		}},
		run.Stage{Name: "qualify", Fn: func(ctx context.Context) error {
			_, err := qualify.BuildLeads(st, qualify.Config{
				SuppressionDays:     cfg.SuppressionDays,
				ScoreThreshold:      cfg.ScoreThreshold,
				ExcludedPKDPrefixes: cfg.ExcludedPKDPrefixes,
			}, runID)
			return err
		}},
		run.Stage{Name: "deliver", Fn: func(ctx context.Context) error {
			return deliverStage(ctx, cfg, st, runID, &stats, dryRun, cmd)
		}},
	)

	r := &run.Runner{Store: st, Stages: stages, Resume: resume}
	return r.Run(ctx)
}

func nextRunID(st *store.Store) (int64, error) {
	var id int64
	err := st.DB.QueryRow(`SELECT COALESCE(MAX(id),0)+1 FROM runs`).Scan(&id)
	return id, err
}
```

- [ ] **Step 2: Implement the deliver stage**

Append to `internal/cli/run.go`:

```go
// deliverStage renders the digest, sends Signal, pushes verified qualified
// leads to Pipedrive, and records deliveries. In dry-run mode everything is
// rendered to stdout and lead state is left untouched.
func deliverStage(ctx context.Context, cfg *config.Config, st *store.Store, runID int64, stats *deliver.RunStats, dryRun bool, cmd *cobra.Command) error {
	spent, _ := st.SpendToday("bizraport")
	stats.SpendPLN = spent

	leads, err := st.DeliverableLeads(runID)
	if err != nil {
		return err
	}
	var verified, unverified []deliver.LeadView
	for _, l := range leads {
		v := leadView(l)
		if l.Company.NIPStatus == "verified" {
			verified = append(verified, v)
		} else {
			unverified = append(unverified, v)
		}
	}
	digest := deliver.RenderDigest(timeNowDate(), verified, unverified, *stats)

	if dryRun {
		fmt.Fprintln(cmd.OutOrStdout(), digest)
		fmt.Fprintf(cmd.OutOrStdout(), "[dry-run] would push %d leads to Pipedrive\n", len(verified))
		return nil
	}

	sig := &deliver.SignalClient{APIURL: cfg.Signal.APIURL, Number: cfg.Signal.Number,
		Recipients: []string{cfg.Signal.GroupID}}
	if err := sig.Send(ctx, digest); err != nil {
		return err // leads stay 'new' and roll into tomorrow's digest
	}

	pd := &deliver.PipedriveClient{BaseURL: cfg.Pipedrive.BaseURL, Token: cfg.Pipedrive.APIToken,
		StageID: cfg.Pipedrive.StageID, FieldKeys: cfg.Pipedrive.FieldKeys}
	for _, l := range leads {
		if err := st.MarkLeadDelivered(l.LeadID, "signal", 0, 0); err != nil {
			return err
		}
		if l.Company.NIPStatus != "verified" || !l.Qualified || cfg.Pipedrive.APIToken == "" {
			continue
		}
		res, err := pd.PushLead(ctx, pipedriveLead(l))
		if err != nil {
			stats.Warnings = append(stats.Warnings, fmt.Sprintf("pipedrive %s: %v", l.Company.Name, err))
			continue
		}
		if err := st.MarkLeadDelivered(l.LeadID, "pipedrive", res.OrgID, res.DealID); err != nil {
			return err
		}
	}
	return nil
}
```

Add the three store/view helpers. `internal/store/leads.go` gains:

```go
// DeliverableLead joins a 'new' lead with its company for delivery.
type DeliverableLead struct {
	LeadID    int64
	Company   Company
	Positions []string
	Score     *int
	Qualified bool
}

func (s *Store) DeliverableLeads(runID int64) ([]DeliverableLead, error) {
	rows, err := s.DB.Query(`
		SELECT l.id, l.positions, l.score, l.qualified,
		       c.id, COALESCE(c.nip,''), c.name, c.normalized_name, c.nip_status,
		       c.address, c.regon, c.krs, c.legal_form, c.pkd_main, c.company_size,
		       c.website, c.email, c.phone, c.board_members, c.first_seen, c.last_seen
		FROM leads l JOIN companies c ON c.id = l.company_id
		WHERE l.status = 'new' AND l.qualified = 1
		   OR (l.status = 'new' AND c.nip_status IN ('pending','unresolved'))
		ORDER BY l.score DESC NULLS LAST, c.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DeliverableLead
	for rows.Next() {
		var d DeliverableLead
		var posJSON string
		var c Company
		if err := rows.Scan(&d.LeadID, &posJSON, &d.Score, &d.Qualified,
			&c.ID, &c.NIP, &c.Name, &c.NormalizedName, &c.NIPStatus,
			&c.Address, &c.REGON, &c.KRS, &c.LegalForm, &c.PKDMain, &c.CompanySize,
			&c.Website, &c.Email, &c.Phone, &c.BoardMembers, &c.FirstSeen, &c.LastSeen); err != nil {
			return nil, err
		}
		json.Unmarshal([]byte(posJSON), &d.Positions)
		d.Company = c
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Store) MarkLeadDelivered(leadID int64, channel string, orgID, dealID int64) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var o, d any
	if orgID != 0 {
		o = orgID
	}
	if dealID != 0 {
		d = dealID
	}
	if _, err := tx.Exec(`INSERT INTO deliveries
		(lead_id, channel, delivered_at, pipedrive_org_id, pipedrive_deal_id, status)
		VALUES (?,?,datetime('now'),?,?,'ok')`, leadID, channel, o, d); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE leads SET status='delivered' WHERE id=?`, leadID); err != nil {
		return err
	}
	return tx.Commit()
}
```

(SQLite note: `NULLS LAST` requires SQLite 3.30+, which modernc.org/sqlite provides.) And in `internal/cli/run.go` add the view mappers + date helper:

```go
func leadView(l store.DeliverableLead) deliver.LeadView {
	var board []krs.BoardMember
	if l.Company.BoardMembers != "" {
		_ = json.Unmarshal([]byte(l.Company.BoardMembers), &board)
	}
	var boardStr []string
	for _, m := range board {
		boardStr = append(boardStr, m.Name+" ("+m.Role+")")
	}
	return deliver.LeadView{
		Company: l.Company.Name, NIP: l.Company.NIP, Positions: l.Positions,
		Location: l.Company.Address, Phone: l.Company.Phone, Email: l.Company.Email,
		Website: l.Company.Website, Score: l.Score, Board: boardStr,
	}
}

func pipedriveLead(l store.DeliverableLead) deliver.PipedriveLead {
	var board []krs.BoardMember
	if l.Company.BoardMembers != "" {
		_ = json.Unmarshal([]byte(l.Company.BoardMembers), &board)
	}
	var boardStr []string
	for _, m := range board {
		boardStr = append(boardStr, m.Name+" ("+m.Role+")")
	}
	return deliver.PipedriveLead{
		Company: l.Company.Name, NIP: l.Company.NIP, REGON: l.Company.REGON,
		KRS: l.Company.KRS, PKD: l.Company.PKDMain, Address: l.Company.Address,
		Website: l.Company.Website, Board: boardStr, Positions: l.Positions,
		NoteContent: "Source: lead-engine | positions: " + strings.Join(l.Positions, "; "),
	}
}

func timeNowDate() string { return time.Now().Format("2006-01-02") }
```

Offer-level contact fallback (OLX direct phone numbers matter for unverified leads): in `DeliverableLeads`' SQL, select the company phone/email with an offer-level fallback instead of the bare columns — `COALESCE(NULLIF(c.phone,''), (SELECT o.phone FROM raw_offers o WHERE o.company_id=c.id AND o.phone<>'' LIMIT 1), '')` and the same pattern for email — so `leadView` stays a pure mapper.

`internal/cli/pipedrive_setup.go`:

```go
package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/hrkono/lead-engine/internal/config"
	"github.com/hrkono/lead-engine/internal/deliver"
)

func newPipedriveCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "pipedrive", Short: "Pipedrive helpers"}
	var cfgPath string
	setup := &cobra.Command{
		Use:   "setup",
		Short: "Create lead-engine custom Organization fields; prints field_keys TOML",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return err
			}
			pd := &deliver.PipedriveClient{BaseURL: cfg.Pipedrive.BaseURL, Token: cfg.Pipedrive.APIToken}
			keys, err := pd.EnsureOrgFields(cmd.Context())
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "[pipedrive.field_keys]")
			for name, key := range keys {
				fmt.Fprintf(cmd.OutOrStdout(), "%s = %q\n", name, key)
			}
			return nil
		},
	}
	setup.Flags().StringVar(&cfgPath, "config", "/etc/lead-engine/config.toml", "config file")
	cmd.AddCommand(setup)
	return cmd
}
```

- [ ] **Step 3: Build and fix until clean**

Run: `go build ./... && go vet ./... && go test ./...`
Expected: clean build, all tests pass.

- [ ] **Step 4: Write `config.example.toml` and `docs/DEPLOY.md`**

`config.example.toml`:

```toml
db_path = "/opt/lead-engine/data/leads.db"
suppression_days = 30
score_threshold = 50
excluded_pkd_prefixes = ["77", "78"]

[scrapers]
gov_cmd = ["/opt/gov_api/venv/bin/python", "/opt/gov_api/main.py", "--voivodeships", "14,30,24"]
gov_export = "/opt/gov_api/exports/raw-leads-cbop-latest.json"
olx_cmd = ["/opt/olx-printing-press/bin/sync-and-export.sh"]
olx_export = "/opt/olx-printing-press/data/exports/raw-leads-olx-latest.json"

[bizraport]
email = ""
password = ""
daily_cap_pln = 10.0
cost_per_row_pln = 0.5
max_candidates = 5

[regon]
api_key = ""

[signal]
api_url = "http://127.0.0.1:8080"
number = "+48000000000"
group_id = "group.xxxx"

[pipedrive]
api_token = ""
stage_id = 0
[pipedrive.field_keys]
# filled in by: lead-engine pipedrive setup
```

Note: the olx scraper needs *two* commands (sync, then export), so `olx_cmd` points at the wrapper script `/opt/olx-printing-press/bin/sync-and-export.sh` (DEPLOY.md §4); same pattern for gov if its export ever needs an extra step. `exec.Command` does **not** invoke a shell — the wrapper must have a `#!/bin/sh` shebang and the execute bit, or the run fails with `exec format error` / `permission denied`.

`docs/DEPLOY.md`:

```markdown
# Deploy — lead-engine on the VPS

## 1. Signal infrastructure (one-time)
docker run -d --name signal-api --restart=always \
  -p 127.0.0.1:8080:8080 \
  -v /opt/signal-cli-config:/home/.local/share/signal-cli \
  -e MODE=normal bbernhard/signal-cli-rest-api:latest

Register the bot number (QR link flow):
  curl -X GET 'http://127.0.0.1:8080/v1/qrcodelink?device_name=lead-engine'
Find the team group id:
  curl 'http://127.0.0.1:8080/v1/groups/+48<botnumber>'

## 2. Pipedrive custom fields (one-time)
  lead-engine pipedrive setup --config /etc/lead-engine/config.toml
Paste the printed [pipedrive.field_keys] block into the config.

## 3. Build & install
  GOOS=linux GOARCH=amd64 go build -o bin/lead-engine ./cmd/lead-engine
  scp bin/lead-engine user@vps:/opt/lead-engine/bin/

## 4. Scraper wrapper scripts
/opt/olx-printing-press/bin/sync-and-export.sh:
  #!/bin/sh
  set -e
  /opt/olx-printing-press/bin/olx-pp-cli sync
  /opt/olx-printing-press/bin/olx-pp-cli export --kind raw-leads --format json \
    --out /opt/olx-printing-press/data/exports/raw-leads-olx-latest.json

Make it executable (lead-engine execs it directly, without a shell):
  chmod +x /opt/olx-printing-press/bin/sync-and-export.sh

## 5. Cron (the single entry point)
  0 5 * * * /opt/lead-engine/bin/lead-engine run --config /etc/lead-engine/config.toml >> /var/log/lead-engine/run.log 2>&1
05:00: CBOP fetch window is 17:00–07:00 and the digest must precede the workday.
Set MAILTO in the crontab for failure alerts (non-zero exit on any stage failure).

## 6. Smoke test
  lead-engine run --config config.toml --skip-scrape --dry-run
```

- [ ] **Step 5: End-to-end dry run against fixtures**

```bash
mkdir -p /tmp/le-test
cp internal/contract/testdata/raw-leads-cbop.json /tmp/le-test/
cp internal/contract/testdata/raw-leads-olx.json /tmp/le-test/
cat > /tmp/le-test/config.toml <<'EOF'
db_path = "/tmp/le-test/leads.db"
[scrapers]
gov_export = "/tmp/le-test/raw-leads-cbop.json"
olx_export = "/tmp/le-test/raw-leads-olx.json"
EOF
go run ./cmd/lead-engine run --config /tmp/le-test/config.toml --skip-scrape --dry-run
```

Expected: a digest printed to stdout containing "Stalmet" under ZWERYFIKOWANE (the OLX offer merged into the same NIP company via name match), offer counts CBOP 1 / OLX 1, and `[dry-run] would push 1 leads to Pipedrive`. The resolve-nip stage logs a warning (no BizRaport credentials) but the run exits 0.

- [ ] **Step 6: Commit**

```bash
git add cmd/ internal/cli/ internal/store/leads.go docs/DEPLOY.md config.example.toml
git commit -m "feat: lead-engine CLI with full pipeline wiring, dry-run, and deploy docs"
```

---

## Final verification (after all tasks)

- [ ] `go test ./...` in lead-engine — all pass.
- [ ] `pytest tests/ -v` in gov_api — all pass (including the new exporter test).
- [ ] `make test` in printing-press/olx/src — all pass.
- [ ] The Task 18 Step 5 dry run produces the expected digest.
- [ ] Use superpowers:verification-before-completion before claiming done; then superpowers:finishing-a-development-branch for the two scraper-repo branches.

## Deferred (explicitly out of scope, per spec §11)

- gov_api Go rewrite, competitor analytics, web dashboard, additional sources.
- OLX-side scoring (OLX leads qualify by default until a scorer exists — revisit when volume data accumulates).
