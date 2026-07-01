package store

import (
	"path/filepath"
	"testing"
)

// TestDeliverableLeadsSurfacesOfferContactFields verifies that the per-offer
// url / contact_person / work_location columns flow through DeliverableLeads via
// the offer-fallback subselects, for both a verified (CBOP) and an unverified
// (OLX) lead.
func TestDeliverableLeadsSurfacesOfferContactFields(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "leads.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	// Verified CBOP company: contact person + work location on the offer.
	govID, _ := st.CreateCompany("1111111111", "GoodCo", "goodco", "verified")
	if err := st.UpsertRawOffer(RawOffer{
		Source: "cbop", ExternalID: "cbop:1", CompanyName: "GoodCo",
		ContactPerson: "Justyna Paczkowska", WorkLocation: "Tczew, pomorskie",
		Website: "www.goodco.example", Phone: "+48221112233", ScrapedAt: "2026-06-10T05:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.AttachOffer("cbop", "cbop:1", govID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateLead(govID, 1, []string{"Spawacz"}, ptrInt(80), true, "new", ""); err != nil {
		t.Fatal(err)
	}

	// Unverified OLX company: url is the only trigger (no phone/email).
	olxID, _ := st.CreateCompany("", "OlxCo", "olxco", "pending")
	if err := st.UpsertRawOffer(RawOffer{
		Source: "olx", ExternalID: "olx:2", CompanyName: "OlxCo",
		URL: "https://www.olx.pl/oferta/monter-ID987654.html", ScrapedAt: "2026-06-10T05:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.AttachOffer("olx", "olx:2", olxID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateLead(olxID, 1, []string{"Monter"}, nil, false, "new", ""); err != nil {
		t.Fatal(err)
	}

	leads, err := st.DeliverableLeads()
	if err != nil {
		t.Fatalf("DeliverableLeads: %v", err)
	}
	byName := map[string]DeliverableLead{}
	for _, l := range leads {
		byName[l.Company.Name] = l
	}

	good, ok := byName["GoodCo"]
	if !ok {
		t.Fatal("verified GoodCo lead not returned")
	}
	if good.ContactPerson != "Justyna Paczkowska" {
		t.Errorf("ContactPerson = %q", good.ContactPerson)
	}
	if good.WorkLocation != "Tczew, pomorskie" {
		t.Errorf("WorkLocation = %q", good.WorkLocation)
	}
	// Website has no company-row value, so it falls back to the offer's.
	if good.Company.Website != "www.goodco.example" {
		t.Errorf("Website = %q, want offer fallback www.goodco.example", good.Company.Website)
	}

	olx, ok := byName["OlxCo"]
	if !ok {
		t.Fatal("unverified OlxCo lead not returned (url trigger should keep it deliverable)")
	}
	if olx.URL != "https://www.olx.pl/oferta/monter-ID987654.html" {
		t.Errorf("URL = %q", olx.URL)
	}
	// OLX lead has no phone/email: the url is what saves it from suppression.
	if olx.Company.Phone != "" || olx.Company.Email != "" {
		t.Errorf("OLX lead unexpectedly has phone/email: %+v", olx.Company)
	}
}

func ptrInt(v int) *int { return &v }
