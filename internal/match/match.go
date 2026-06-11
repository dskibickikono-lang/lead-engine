// Package match attaches raw offers to unified company rows.
package match

import (
	"fmt"

	"github.com/hrkono/lead-engine/internal/store"
)

type Stats struct {
	Attached int
}

// Attach assigns every unattached raw offer to a company row. NIP is the
// canonical identity; NIP-less offers fall back to normalized-name matching,
// which can mis-attach if two different businesses normalize identically —
// an accepted trade-off for the NIP-less OLX source.
// Each offer commits independently; on mid-batch error a re-run safely
// continues (only company_id IS NULL offers are processed).
func Attach(st *store.Store) (Stats, error) {
	var stats Stats
	offers, err := st.UnattachedOffers()
	if err != nil {
		return stats, fmt.Errorf("match: %w", err)
	}
	for _, o := range offers {
		norm := Normalize(o.CompanyName)
		var c *store.Company
		if o.NIP != "" {
			c, err = st.FindCompanyByNIP(o.NIP)
		} else if norm != "" {
			c, err = st.FindCompanyByNormalizedName(norm)
		}
		if err != nil {
			return stats, fmt.Errorf("match %s/%s: %w", o.Source, o.ExternalID, err)
		}
		if c == nil && o.NIP != "" {
			// A NIP-less provisional row with the same name may already exist.
			if norm != "" {
				c, err = st.FindCompanyByNormalizedName(norm)
				if err != nil {
					return stats, err
				}
			}
			if c != nil && c.NIP == "" {
				if err := st.PromoteCompanyNIP(c.ID, o.NIP); err != nil {
					return stats, err
				}
			} else {
				c = nil
			}
		}
		if c == nil {
			status := "pending"
			if o.NIP != "" {
				status = "verified"
			}
			id, err := st.CreateCompany(o.NIP, o.CompanyName, norm, status)
			if err != nil {
				return stats, err
			}
			c = &store.Company{ID: id}
		}
		if err := st.AttachOffer(o.Source, o.ExternalID, c.ID); err != nil {
			return stats, err
		}
		if err := st.TouchCompany(c.ID); err != nil {
			return stats, err
		}
		stats.Attached++
	}
	return stats, nil
}
