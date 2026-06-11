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
	Board     []string
}

type RunStats struct {
	OffersCBOP int
	OffersOLX  int
	SpendPLN   float64
	CapPLN     float64
	Warnings   []string
}

func RenderDigest(date string, verified, unverified []LeadView, stats RunStats) string {
	var b strings.Builder
	fmt.Fprintf(&b, "LEADY %s\n", date)
	fmt.Fprintf(&b, "Zweryfikowane: %d | Niezweryfikowane: %d\n\n", len(verified), len(unverified))

	if len(verified) > 0 {
		b.WriteString("== ZWERYFIKOWANE ==\n")
		for i, l := range verified {
			fmt.Fprintf(&b, "%d. %s", i+1, l.Company)
			if l.Score != nil {
				fmt.Fprintf(&b, " [score %d]", *l.Score)
			}
			b.WriteString("\n")
			fmt.Fprintf(&b, "   Szuka: %s\n", strings.Join(l.Positions, "; "))
			if l.Location != "" {
				fmt.Fprintf(&b, "   Lokalizacja: %s\n", l.Location)
			}
			fmt.Fprintf(&b, "   NIP: %s\n", l.NIP)
			if l.Phone != "" {
				fmt.Fprintf(&b, "   Tel: %s\n", l.Phone)
			}
			if l.Email != "" {
				fmt.Fprintf(&b, "   Email: %s\n", l.Email)
			}
			if l.Website != "" {
				fmt.Fprintf(&b, "   WWW: %s\n", l.Website)
			}
			if len(l.Board) > 0 {
				fmt.Fprintf(&b, "   Zarząd: %s\n", strings.Join(l.Board, ", "))
			}
			b.WriteString("\n")
		}
	}

	if len(unverified) > 0 {
		b.WriteString("== NIEZWERYFIKOWANE (brak NIP) ==\n")
		for i, l := range unverified {
			fmt.Fprintf(&b, "%d. %s — %s", i+1, l.Company, strings.Join(l.Positions, "; "))
			if l.Location != "" {
				fmt.Fprintf(&b, " (%s)", l.Location)
			}
			b.WriteString("\n")
			if l.Phone != "" {
				fmt.Fprintf(&b, "   Tel: %s\n", l.Phone)
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
