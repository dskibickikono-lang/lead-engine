package enrich

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"

	"github.com/hrkono/lead-engine/internal/enrich/bizraport"
	"github.com/hrkono/lead-engine/internal/store"
)

// fakeBizraport serves /api/szukaj (one KRS hit) and /api/dane (profile).
// Wire shapes match the real bizraport client:
//   - /api/szukaj: {"data":[{"krs":"..."}], "dane_uciete": false}
//   - /api/dane:   {"data":[{...complex...}]}  with informacje_o_firmie as
//     array of {nazwa_pola, wartosc} pairs.
func fakeBizraport(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(stalmetHandler())
}

// stalmetHandler returns a handler that serves STALMET profile data.
func stalmetHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeStalmetResponse(w, r)
	})
}

func writeStalmetResponse(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/api/szukaj":
		fmt.Fprint(w, `{"data":[{"krs":"0000123456","nazwa":"STALMET SP Z O O"}],"dane_uciete":false}`)
	case "/api/dane":
		fmt.Fprint(w, `{"data":[{
			"krs":"0000123456",
			"nip":"1234567890",
			"kod_pkd":"25.11.Z",
			"opis_pkd":"Produkcja konstrukcji metalowych",
			"informacje_o_firmie":[
				{"nazwa_pola":"nazwa","wartosc":"Stalmet Sp. z o.o."},
				{"nazwa_pola":"forma_prawna","wartosc":"sp. z o.o."},
				{"nazwa_pola":"regon","wartosc":"123456785"},
				{"nazwa_pola":"ulica","wartosc":"Prosta 1"},
				{"nazwa_pola":"kod_pocztowy","wartosc":"00-001"},
				{"nazwa_pola":"miejscowosc","wartosc":"Warszawa"},
				{"nazwa_pola":"email","wartosc":"biuro@stalmet.example"},
				{"nazwa_pola":"adres_strony_internetowej","wartosc":"stalmet.example"}
			]
		}]}`)
	default:
		http.NotFound(w, r)
	}
}

// countingHandler wraps an http.Handler and counts requests per path.
type countingHandler struct {
	mu      sync.Mutex
	counts  map[string]int
	wrapped http.Handler
}

func newCountingHandler(h http.Handler) *countingHandler {
	return &countingHandler{counts: make(map[string]int), wrapped: h}
}

func (ch *countingHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ch.mu.Lock()
	ch.counts[r.URL.Path]++
	ch.mu.Unlock()
	ch.wrapped.ServeHTTP(w, r)
}

func (ch *countingHandler) Count(path string) int {
	ch.mu.Lock()
	defer ch.mu.Unlock()
	return ch.counts[path]
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

func TestResolveCacheHitDoesNotRebill(t *testing.T) {
	ch := newCountingHandler(stalmetHandler())
	srv := httptest.NewServer(ch)
	defer srv.Close()
	st := testStore(t)

	cfg := ResolveConfig{DailyCapPLN: 10, CostPerRowPLN: 0.5, MaxCandidates: 5}
	bz := bizraport.New(bizraport.Options{BaseURL: srv.URL, Email: "x", Password: "y"})

	// Run 1: resolve "Stalmet sp. z o.o." — expects 1 search + 1 profile row.
	st.CreateCompany("", "Stalmet sp. z o.o.", "stalmet", "pending")
	stats1, err := ResolveNIPs(context.Background(), st, bz, cfg)
	if err != nil {
		t.Fatalf("run1 ResolveNIPs: %v", err)
	}
	if stats1.Resolved != 1 {
		t.Errorf("run1: Resolved = %d, want 1", stats1.Resolved)
	}
	spendAfterRun1, _ := st.SpendToday("bizraport")
	// 1 search row + 1 profile row = 2 rows * 0.5 = 1.0
	const wantRun1Spend = 1.0
	if spendAfterRun1 != wantRun1Spend {
		t.Errorf("run1 spend = %v, want %v", spendAfterRun1, wantRun1Spend)
	}
	daneAfterRun1 := ch.Count("/api/dane")
	if daneAfterRun1 != 1 {
		t.Errorf("run1: /api/dane hit %d times, want 1", daneAfterRun1)
	}

	// Run 2: second company with same normalized name; profile served from cache.
	// "STALMET sp. z o.o." normalizes identically to "stalmet".
	st.CreateCompany("", "STALMET sp. z o.o.", "stalmet", "pending")
	stats2, err := ResolveNIPs(context.Background(), st, bz, cfg)
	if err != nil {
		t.Fatalf("run2 ResolveNIPs: %v", err)
	}
	if stats2.Resolved != 1 {
		t.Errorf("run2: Resolved = %d, want 1 (merge path)", stats2.Resolved)
	}

	// /api/dane must not have been called again — cache served the profile.
	daneAfterRun2 := ch.Count("/api/dane")
	if daneAfterRun2 != 1 {
		t.Errorf("/api/dane total hits = %d, want exactly 1 (cache should have served run2)", daneAfterRun2)
	}

	// Total spend: run1=1.0 (1 search + 1 profile), run2=0.5 (1 search only, profile cached).
	totalSpend, _ := st.SpendToday("bizraport")
	const wantTotalSpend = 1.5
	if totalSpend != wantTotalSpend {
		t.Errorf("total spend = %v, want %v", totalSpend, wantTotalSpend)
	}
}

func TestResolveUnresolvedStillBilled(t *testing.T) {
	// Server returns a profile whose nazwa does NOT match the queried company name.
	mismatchHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/szukaj":
			fmt.Fprint(w, `{"data":[{"krs":"0000999999"}],"dane_uciete":false}`)
		case "/api/dane":
			fmt.Fprint(w, `{"data":[{
				"krs":"0000999999",
				"nip":"9999999999",
				"informacje_o_firmie":[
					{"nazwa_pola":"nazwa","wartosc":"Inna Firma Sp. z o.o."}
				]
			}]}`)
		default:
			http.NotFound(w, r)
		}
	})
	srv := httptest.NewServer(mismatchHandler)
	defer srv.Close()
	st := testStore(t)

	bz := bizraport.New(bizraport.Options{BaseURL: srv.URL, Email: "x", Password: "y"})
	id, _ := st.CreateCompany("", "Stalmet sp. z o.o.", "stalmet", "pending")

	stats, err := ResolveNIPs(context.Background(), st, bz, ResolveConfig{
		DailyCapPLN: 10, CostPerRowPLN: 0.5, MaxCandidates: 5,
	})
	if err != nil {
		t.Fatalf("ResolveNIPs: %v", err)
	}
	if stats.Unresolved != 1 {
		t.Errorf("Unresolved = %d, want 1", stats.Unresolved)
	}

	// Company should be marked unresolved.
	c, err := st.FindCompanyByNIP("9999999999")
	if c != nil {
		t.Errorf("mismatched NIP should not have been saved; got company %+v", c)
	}
	// Check by ID that nip_status is 'unresolved'.
	companies, _ := st.CompaniesPendingNIP()
	for _, co := range companies {
		if co.ID == id {
			t.Errorf("company %d still in pending after unresolved run", id)
		}
	}
	_ = err

	// Spend must be > 0: the search row AND the profile row were billed despite no match.
	spent, _ := st.SpendToday("bizraport")
	if spent <= 0 {
		t.Errorf("spend = %v after unresolved run, want > 0 (search+profile rows billed)", spent)
	}
	// 1 search row + 1 profile row = 1.0 PLN
	const wantSpend = 1.0
	if spent != wantSpend {
		t.Errorf("spend = %v, want %v (1 search + 1 profile row)", spent, wantSpend)
	}
}

var _ = json.Marshal // silence unused import if shapes change
