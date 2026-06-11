package store

import (
	"database/sql"
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

// DeliverableLead joins a 'new' lead with its company for delivery.
type DeliverableLead struct {
	LeadID    int64
	Company   Company
	Positions []string
	Score     *int
	Qualified bool
}

// DeliverableLeads returns all 'new' qualified leads plus 'new' unverified
// leads (pending/unresolved NIP) for the given run, ordered by score desc then
// company name. The phone and email columns fall back to an offer-level contact
// when the company row has none — important for OLX-sourced unverified leads.
func (s *Store) DeliverableLeads(runID int64) ([]DeliverableLead, error) {
	rows, err := s.DB.Query(`
		SELECT l.id, l.positions, l.score, l.qualified,
		       c.id, COALESCE(c.nip,''), c.name, c.normalized_name, c.nip_status,
		       c.address, c.regon, c.krs, c.legal_form, c.pkd_main, c.company_size,
		       c.website,
		       COALESCE(NULLIF(c.email,''),   (SELECT o.email FROM raw_offers o WHERE o.company_id=c.id AND o.email<>''   LIMIT 1), ''),
		       COALESCE(NULLIF(c.phone,''),   (SELECT o.phone FROM raw_offers o WHERE o.company_id=c.id AND o.phone<>''   LIMIT 1), ''),
		       c.board_members, c.first_seen, c.last_seen
		FROM leads l JOIN companies c ON c.id = l.company_id
		WHERE (l.status = 'new' AND l.qualified = 1)
		   OR (l.status = 'new' AND c.nip_status IN ('pending','unresolved'))
		ORDER BY l.score DESC NULLS LAST, c.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DeliverableLead
	for rows.Next() {
		var d DeliverableLead
		var posJSON string
		var score sql.NullInt64
		var qualInt int
		c := &d.Company
		if err := rows.Scan(
			&d.LeadID, &posJSON, &score, &qualInt,
			&c.ID, &c.NIP, &c.Name, &c.NormalizedName, &c.NIPStatus,
			&c.Address, &c.REGON, &c.KRS, &c.LegalForm, &c.PKDMain, &c.CompanySize,
			&c.Website, &c.Email, &c.Phone, &c.BoardMembers, &c.FirstSeen, &c.LastSeen,
		); err != nil {
			return nil, err
		}
		if score.Valid {
			v := int(score.Int64)
			d.Score = &v
		}
		d.Qualified = qualInt != 0
		_ = json.Unmarshal([]byte(posJSON), &d.Positions)
		out = append(out, d)
	}
	return out, rows.Err()
}

// MarkLeadDelivered records a delivery event and transitions the lead to
// 'delivered'. orgID/dealID are optional (pass 0 to omit).
func (s *Store) MarkLeadDelivered(leadID int64, channel string, orgID, dealID int64) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var o, d any
	if orgID != 0 {
		o = orgID
	}
	if dealID != 0 {
		d = dealID
	}
	if _, err := tx.Exec(`INSERT INTO deliveries
		(lead_id, channel, delivered_at, pipedrive_org_id, pipedrive_deal_id, status)
		VALUES (?,?,datetime('now'),?,?,'ok')`, leadID, channel, o, d); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE leads SET status='delivered' WHERE id=?`, leadID); err != nil {
		return err
	}
	return tx.Commit()
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
