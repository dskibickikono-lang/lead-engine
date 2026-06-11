package enrich

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/hrkono/lead-engine/internal/enrich/krs"
	"github.com/hrkono/lead-engine/internal/enrich/regon"
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

// countingKRSStub wraps krsStub and counts FetchBoard calls.
type countingKRSStub struct {
	inner krsStub
	calls int
}

func (c *countingKRSStub) FetchBoard(ctx context.Context, krsNum string) ([]krs.BoardMember, error) {
	c.calls++
	return c.inner.FetchBoard(ctx, krsNum)
}

// TestEnrichCachesEmptyBoard verifies that an empty board result is written as
// "[]" sentinel so the company is excluded from future enrichment runs, and
// that the KRS stub is called exactly once across two consecutive Enrich runs.
func TestEnrichCachesEmptyBoard(t *testing.T) {
	st := testStore(t)
	id, err := st.CreateCompany("9876543210", "Pusta Firma Sp. z o.o.", "pusta-firma", "verified")
	if err != nil {
		t.Fatalf("CreateCompany: %v", err)
	}
	// Pre-fill all REGON fields so the enricher skips the REGON lookup and
	// goes straight to the board fetch.
	if err := st.FillCompanyFields(id, map[string]string{
		"regon":   "987654321",
		"krs":     "0000654321",
		"phone":   "123456789",
		"email":   "biuro@pusta.example",
		"website": "pusta.example",
		"address": "Pusta 1, 00-001 Warszawa",
	}); err != nil {
		t.Fatalf("FillCompanyFields: %v", err)
	}

	stub := &countingKRSStub{inner: krsStub{board: nil}} // empty board
	rg := regonStub{rep: nil}                             // REGON not needed

	// Run 1: board fetch returns empty; expect "[]" written.
	stats1, err := Enrich(context.Background(), st, rg, stub)
	if err != nil {
		t.Fatalf("run1 Enrich: %v", err)
	}
	if stats1.Enriched != 1 {
		t.Errorf("run1: Enriched = %d, want 1", stats1.Enriched)
	}
	c, _ := st.FindCompanyByNIP("9876543210")
	if c.BoardMembers != "[]" {
		t.Errorf("run1: board_members = %q, want \"[]\"", c.BoardMembers)
	}
	if stub.calls != 1 {
		t.Errorf("run1: KRS stub called %d times, want 1", stub.calls)
	}

	// Run 2: board_members = "[]" (non-empty) so WHERE clause excludes this
	// company; stub should not be called again.
	stats2, err := Enrich(context.Background(), st, rg, stub)
	if err != nil {
		t.Fatalf("run2 Enrich: %v", err)
	}
	if stats2.Enriched != 0 {
		t.Errorf("run2: Enriched = %d, want 0 (company excluded by sentinel)", stats2.Enriched)
	}
	if stub.calls != 1 {
		t.Errorf("total KRS stub calls = %d, want exactly 1 (cached in run1)", stub.calls)
	}
}
