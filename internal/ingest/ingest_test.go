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
