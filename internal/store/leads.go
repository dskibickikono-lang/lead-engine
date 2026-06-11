package store

import (
	"encoding/json"
	"strconv"
)

// LeadCandidate is a company with offers not yet covered by any lead.
type LeadCandidate struct {
	Company   Company
	Positions []string
	MaxScore  *int // nil when no offer carries a score (OLX-only)
}

// LeadCandidates returns companies that have attached offers newer than the
// company's most recent lead (or that never had a lead). Positions and score
// aggregate over the trigger window (offers newer than the company's latest
// lead), not the full offer history.
func (s *Store) LeadCandidates() ([]LeadCandidate, error) {
	rows, err := s.DB.Query(`
		SELECT c.id, COALESCE(c.nip,''), c.name, c.normalized_name, c.nip_status,
		       c.address, c.regon, c.krs, c.legal_form, c.pkd_main, c.company_size,
		       c.website, c.email, c.phone, c.board_members, c.first_seen, c.last_seen
		FROM companies c
		WHERE EXISTS (
		  SELECT 1 FROM raw_offers o
		  WHERE o.company_id = c.id
		    AND o.ingested_at > COALESCE(
		      (SELECT MAX(l.created_at) FROM leads l WHERE l.company_id = c.id), '')
		)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LeadCandidate
	for rows.Next() {
		var c Company
		if err := rows.Scan(&c.ID, &c.NIP, &c.Name, &c.NormalizedName, &c.NIPStatus,
			&c.Address, &c.REGON, &c.KRS, &c.LegalForm, &c.PKDMain, &c.CompanySize,
			&c.Website, &c.Email, &c.Phone, &c.BoardMembers, &c.FirstSeen, &c.LastSeen); err != nil {
			return nil, err
		}
		out = append(out, LeadCandidate{Company: c})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		prows, err := s.DB.Query(`SELECT DISTINCT COALESCE(position,''), score
			FROM raw_offers
			WHERE company_id = ?
			  AND ingested_at > COALESCE(
			    (SELECT MAX(l.created_at) FROM leads l WHERE l.company_id = raw_offers.company_id), '')`,
			out[i].Company.ID)
		if err != nil {
			return nil, err
		}
		seen := map[string]bool{}
		for prows.Next() {
			var pos string
			var score *int
			if err := prows.Scan(&pos, &score); err != nil {
				prows.Close()
				return nil, err
			}
			if pos != "" && !seen[pos] {
				seen[pos] = true
				out[i].Positions = append(out[i].Positions, pos)
			}
			if score != nil && (out[i].MaxScore == nil || *score > *out[i].MaxScore) {
				out[i].MaxScore = score
			}
		}
		prows.Close()
	}
	return out, nil
}

func (s *Store) CreateLead(companyID int64, runID int64, positions []string, score *int, qualified bool, status, reason string) (int64, error) {
	pj, _ := json.Marshal(positions)
	res, err := s.DB.Exec(`INSERT INTO leads
		(company_id, run_id, positions, score, qualified, status, reason, created_at)
		VALUES (?,?,?,?,?,?,?,datetime('now'))`,
		companyID, runID, string(pj), score, qualified, status, reason)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// DeliveredWithin reports whether the company had any successful delivery
// in the last `days` days.
func (s *Store) DeliveredWithin(companyID int64, days int) (bool, error) {
	var n int
	err := s.DB.QueryRow(`SELECT COUNT(*) FROM deliveries d
		JOIN leads l ON l.id = d.lead_id
		WHERE l.company_id = ? AND d.status = 'ok'
		  AND d.delivered_at > datetime('now', ?)`,
		companyID, "-"+strconv.Itoa(days)+" days").Scan(&n)
	return n > 0, err
}
