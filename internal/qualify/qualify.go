// Package qualify turns enriched companies with fresh offers into
// deliverable leads, applying suppression and qualification rules.
package qualify

import (
	"fmt"
	"strings"

	"github.com/hrkono/lead-engine/internal/store"
)

type Config struct {
	SuppressionDays     int
	ScoreThreshold      int
	ExcludedPKDPrefixes []string
}

type Stats struct {
	Created          int
	SuppressedPKD    int
	SuppressedRecent int
	Unqualified      int
}

func BuildLeads(st *store.Store, cfg Config, runID int64) (Stats, error) {
	var stats Stats
	cands, err := st.LeadCandidates()
	if err != nil {
		return stats, fmt.Errorf("qualify: %w", err)
	}
	for _, cand := range cands {
		c := cand.Company

		if pref := excludedPKD(c.PKDMain, cfg.ExcludedPKDPrefixes); pref != "" {
			if _, err := st.CreateLead(c.ID, runID, cand.Positions, cand.MaxScore,
				false, "suppressed", "agency PKD "+c.PKDMain); err != nil {
				return stats, err
			}
			stats.SuppressedPKD++
			continue
		}
		recent, err := st.DeliveredWithin(c.ID, cfg.SuppressionDays)
		if err != nil {
			return stats, err
		}
		if recent {
			if _, err := st.CreateLead(c.ID, runID, cand.Positions, cand.MaxScore,
				false, "suppressed", "delivered recently"); err != nil {
				return stats, err
			}
			stats.SuppressedRecent++
			continue
		}
		// Scored (gov) leads must clear the threshold; unscored (OLX-only)
		// leads qualify by default — OLX has no scorer yet.
		qualified := cand.MaxScore == nil || *cand.MaxScore >= cfg.ScoreThreshold
		if _, err := st.CreateLead(c.ID, runID, cand.Positions, cand.MaxScore,
			qualified, "new", ""); err != nil {
			return stats, err
		}
		stats.Created++
		if !qualified {
			stats.Unqualified++
		}
	}
	return stats, nil
}

func excludedPKD(pkd string, prefixes []string) string {
	pkd = strings.ReplaceAll(pkd, ".", "")
	for _, p := range prefixes {
		if p != "" && strings.HasPrefix(pkd, p) {
			return p
		}
	}
	return ""
}
