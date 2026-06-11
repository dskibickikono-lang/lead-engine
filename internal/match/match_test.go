package match

import (
	"fmt"
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

func TestAttachEmptyNormNeverMatches(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "leads.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for i, name := range []string{"Sp. z o.o.", "S.A."} {
		if err := st.UpsertRawOffer(store.RawOffer{
			Source: "olx", ExternalID: fmt.Sprintf("olx:e%d", i), CompanyName: name,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Attach(st); err != nil {
		t.Fatal(err)
	}
	var companies int
	st.DB.QueryRow(`SELECT COUNT(*) FROM companies`).Scan(&companies)
	if companies != 2 {
		t.Errorf("companies = %d, want 2 (suffix-only names must not merge)", companies)
	}
}
