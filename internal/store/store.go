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
  headcount        TEXT NOT NULL DEFAULT '', -- liczba zatrudnionych (REGON BIR)
  share_capital    TEXT NOT NULL DEFAULT '', -- kapitał zakładowy (KRS dział 1)
  registered_since TEXT NOT NULL DEFAULT '', -- data wpisu/powstania (REGON BIR)
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

// Store wraps the SQLite database for leads.db.
type Store struct {
	DB *sql.DB
}

// Open opens (or creates) the SQLite database at path and applies the schema.
// It is safe to call multiple times on the same file.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("init schema: %w", err)
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	// Single sequential writer; one connection avoids SQLITE_BUSY between
	// statements and keeps WAL checkpointing simple.
	db.SetMaxOpenConns(1)
	return &Store{DB: db}, nil
}

// migrate applies additive column changes to tables that already exist on a
// live DB. The schema const uses CREATE TABLE IF NOT EXISTS, which never alters
// an existing table, so columns added after first deploy need an explicit
// ALTER TABLE here. Each add is guarded by a PRAGMA table_info check so the
// function is idempotent.
func migrate(db *sql.DB) error {
	cols, err := tableColumns(db, "companies")
	if err != nil {
		return err
	}
	adds := []struct{ name, ddl string }{
		{"headcount", `ALTER TABLE companies ADD COLUMN headcount TEXT NOT NULL DEFAULT ''`},
		{"share_capital", `ALTER TABLE companies ADD COLUMN share_capital TEXT NOT NULL DEFAULT ''`},
		{"registered_since", `ALTER TABLE companies ADD COLUMN registered_since TEXT NOT NULL DEFAULT ''`},
	}
	for _, a := range adds {
		if cols[a.name] {
			continue
		}
		if _, err := db.Exec(a.ddl); err != nil {
			return fmt.Errorf("add column %s: %w", a.name, err)
		}
	}
	return nil
}

// tableColumns returns the set of column names present on a table.
func tableColumns(db *sql.DB, table string) (map[string]bool, error) {
	rows, err := db.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		cols[name] = true
	}
	return cols, rows.Err()
}

// Close closes the underlying database connection.
func (s *Store) Close() error { return s.DB.Close() }
