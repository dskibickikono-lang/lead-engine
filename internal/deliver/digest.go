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
	// Business fields (verified leads); each rendered only when non-empty.
	LegalForm       string // forma prawna
	Employment      string // liczba zatrudnionych / wielkość zatrudnienia
	ShareCapital    string // kapitał zakładowy
	PKD             string // przeważające PKD
	RegisteredSince string // w rejestrze od
	// Per-offer contact fields. URL is the OLX listing (unverified trigger);
	// ContactPerson (name) + WorkLocation come from CBOP (verified).
	URL           string
	ContactPerson string
	WorkLocation  string
}

type RunStats struct {
	OffersCBOP int
	OffersOLX  int
	SpendPLN   float64
	CapPLN     float64
	Warnings   []string
}

const digestRule = "━━━━━━━━━━━━━━━━━━━━━━━"

// RenderDigest builds the Signal message. Verified leads carry full registry +
// business detail; unverified (OLX, no NIP) leads carry the actionable trigger
// (phone/email). Every field line is emitted only when present.
func RenderDigest(date string, verified, unverified []LeadView, stats RunStats) string {
	var b strings.Builder
	fmt.Fprintf(&b, "📦 LEADY %s\n", date)
	fmt.Fprintf(&b, "✅ Zweryfikowane: %d | ❓ Niezweryfikowane: %d\n", len(verified), len(unverified))

	line := func(format string, a ...any) { fmt.Fprintf(&b, "   "+format+"\n", a...) }

	if len(verified) > 0 {
		fmt.Fprintf(&b, "\n%s\n✅ ZWERYFIKOWANE\n%s\n\n", digestRule, digestRule)
		for i, l := range verified {
			fmt.Fprintf(&b, "%d. 🏛️ %s\n", i+1, l.Company)
			if l.Location != "" {
				line("📍 %s", l.Location)
			}
			if l.WorkLocation != "" {
				line("🗺️ Miejsce pracy: %s", l.WorkLocation)
			}
			line("🔍 Szuka: %s", strings.Join(l.Positions, "; "))
			line("🪪 NIP: %s", l.NIP)
			if l.ContactPerson != "" {
				line("🧑 Kontakt: %s", l.ContactPerson)
			}
			if l.Phone != "" {
				line("📞 %s", l.Phone)
			}
			if l.Email != "" {
				line("📧 %s", l.Email)
			}
			if l.Website != "" {
				line("🌐 %s", l.Website)
			}
			if l.LegalForm != "" {
				line("🏷️ %s", l.LegalForm)
			}
			if l.Employment != "" {
				line("👥 Zatrudnienie: %s", l.Employment)
			}
			if l.ShareCapital != "" {
				line("💰 Kapitał: %s", l.ShareCapital)
			}
			if l.PKD != "" {
				line("🏭 PKD: %s", l.PKD)
			}
			if l.RegisteredSince != "" {
				line("📅 W rejestrze od: %s", l.RegisteredSince)
			}
			if l.Score != nil {
				line("⭐ Score: %d", *l.Score)
			}
			b.WriteString("\n")
		}
	}

	if len(unverified) > 0 {
		fmt.Fprintf(&b, "%s\n❓ NIEZWERYFIKOWANE (brak NIP)\n%s\n\n", digestRule, digestRule)
		for i, l := range unverified {
			fmt.Fprintf(&b, "%d. 🏢 %s — %s", i+1, l.Company, strings.Join(l.Positions, "; "))
			if l.Location != "" {
				fmt.Fprintf(&b, " (%s)", l.Location)
			}
			b.WriteString("\n")
			if l.Phone != "" {
				line("📞 %s", l.Phone)
			}
			if l.Email != "" {
				line("📧 %s", l.Email)
			}
			if l.URL != "" {
				line("🔗 %s", l.URL)
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
