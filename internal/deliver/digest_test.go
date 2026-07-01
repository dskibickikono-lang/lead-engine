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
		Score:           &hi,
		LegalForm:       "spółka z ograniczoną odpowiedzialnością",
		Employment:      "85",
		ShareCapital:    "500000,00 PLN",
		PKD:             "25.62.Z",
		RegisteredSince: "2009-03-12",
		ContactPerson:   "Justyna Paczkowska",
		WorkLocation:    "Tczew, pomorskie",
	}}
	unverified := []LeadView{{
		Company: "Mała Firma Jan Nowak", Positions: []string{"Monter"},
		Location: "Radom", Phone: "+48501502503",
		Email: "kontakt@malafirma.example",
		URL:   "https://www.olx.pl/oferta/monter-ID987654.html",
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
