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

func TestBuildLeadsDedupesPositions(t *testing.T) {
	st := testStore(t)
	cfg := Config{SuppressionDays: 30, ScoreThreshold: 50}
	id, _ := st.CreateCompany("5555555555", "DupCo", "dupco", "verified")
	s1, s2 := 60, 90
	addOffer(t, st, "cbop", "cbop:d1", id, "Spawacz", &s1)
	addOffer(t, st, "cbop", "cbop:d2", id, "Spawacz", &s2)

	if _, err := BuildLeads(st, cfg, 1); err != nil {
		t.Fatal(err)
	}
	var positions string
	var score int
	if err := st.DB.QueryRow(`SELECT positions, score FROM leads WHERE company_id=?`, id).
		Scan(&positions, &score); err != nil {
		t.Fatal(err)
	}
	if positions != `["Spawacz"]` {
		t.Errorf("positions = %s, want single Spawacz", positions)
	}
	if score != 90 {
		t.Errorf("score = %d, want 90 (max)", score)
	}
}
