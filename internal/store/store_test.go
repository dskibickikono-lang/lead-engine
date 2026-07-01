package store

import (
	"database/sql"
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

// TestMigrateAddsBusinessColumns simulates a live DB created before the
// headcount/share_capital/registered_since columns existed and verifies Open's
// ALTER migration backfills them without data loss.
func TestMigrateAddsBusinessColumns(t *testing.T) {
	p := filepath.Join(t.TempDir(), "leads.db")

	// Pre-create a companies table with the old (pre-business-fields) shape and
	// a row, mimicking an already-deployed database.
	db, err := sql.Open("sqlite", p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE companies (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		nip TEXT UNIQUE, name TEXT NOT NULL, normalized_name TEXT NOT NULL,
		nip_status TEXT NOT NULL DEFAULT 'pending', address TEXT NOT NULL DEFAULT '',
		regon TEXT NOT NULL DEFAULT '', krs TEXT NOT NULL DEFAULT '',
		legal_form TEXT NOT NULL DEFAULT '', pkd_main TEXT NOT NULL DEFAULT '',
		company_size TEXT NOT NULL DEFAULT '', website TEXT NOT NULL DEFAULT '',
		email TEXT NOT NULL DEFAULT '', phone TEXT NOT NULL DEFAULT '',
		board_members TEXT NOT NULL DEFAULT '',
		first_seen TEXT NOT NULL, last_seen TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO companies (name, normalized_name, first_seen, last_seen)
		VALUES ('Stalmet', 'stalmet', datetime('now'), datetime('now'))`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	// Open runs the schema + migration; the new columns must now exist and the
	// pre-existing row must still be readable through the full company scan.
	st, err := Open(p)
	if err != nil {
		t.Fatalf("Open (migrate): %v", err)
	}
	defer st.Close()

	cols, err := tableColumns(st.DB, "companies")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []string{"headcount", "share_capital", "registered_since"} {
		if !cols[c] {
			t.Errorf("column %s not added by migration", c)
		}
	}

	c, err := st.FindCompanyByNormalizedName("stalmet")
	if err != nil || c == nil {
		t.Fatalf("read migrated row: %v (company=%v)", err, c)
	}
	if c.ShareCapital != "" || c.Headcount != "" {
		t.Errorf("new columns should default empty, got headcount=%q capital=%q", c.Headcount, c.ShareCapital)
	}

	// Idempotent: opening again must not fail on already-present columns.
	st2, err := Open(p)
	if err != nil {
		t.Fatalf("second Open after migration: %v", err)
	}
	st2.Close()
}

// TestMigrateAddsRawOfferColumns simulates a live DB created before the
// url/contact_person/work_location columns existed on raw_offers and verifies
// Open's ALTER migration backfills them without dropping the existing row.
func TestMigrateAddsRawOfferColumns(t *testing.T) {
	p := filepath.Join(t.TempDir(), "leads.db")

	// Pre-create raw_offers with the old (pre-contact-fields) shape + a row,
	// mimicking an already-deployed database.
	db, err := sql.Open("sqlite", p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE raw_offers (
		source TEXT NOT NULL, external_id TEXT NOT NULL, nip TEXT,
		company_name TEXT NOT NULL, position TEXT, location TEXT, vacancies INTEGER,
		salary_from REAL, salary_to REAL, phone TEXT, email TEXT, score INTEGER,
		scraped_at TEXT, ingested_at TEXT NOT NULL, company_id INTEGER, payload TEXT,
		PRIMARY KEY (source, external_id))`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO raw_offers (source, external_id, company_name, ingested_at)
		VALUES ('olx', 'olx:1', 'Stalmet', datetime('now'))`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	// Open runs the schema + migration; the new columns must now exist.
	st, err := Open(p)
	if err != nil {
		t.Fatalf("Open (migrate): %v", err)
	}
	defer st.Close()

	cols, err := tableColumns(st.DB, "raw_offers")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []string{"url", "contact_person", "work_location", "website"} {
		if !cols[c] {
			t.Errorf("column raw_offers.%s not added by migration", c)
		}
	}

	// Pre-existing row survives and the new column defaults to NULL/"".
	var name, url string
	err = st.DB.QueryRow(`SELECT company_name, COALESCE(url,'') FROM raw_offers WHERE external_id='olx:1'`).Scan(&name, &url)
	if err != nil {
		t.Fatalf("read migrated raw_offers row: %v", err)
	}
	if name != "Stalmet" || url != "" {
		t.Errorf("unexpected migrated row: name=%q url=%q", name, url)
	}

	// Idempotent: opening again must not fail on already-present columns.
	st2, err := Open(p)
	if err != nil {
		t.Fatalf("second Open after migration: %v", err)
	}
	st2.Close()
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
