package store

func (s *Store) CompaniesPendingNIP() ([]Company, error) {
	rows, err := s.DB.Query(`SELECT ` + companyCols + ` FROM companies
		WHERE nip_status = 'pending' ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Company
	for rows.Next() {
		var c Company
		if err := rows.Scan(&c.ID, &c.NIP, &c.Name, &c.NormalizedName, &c.NIPStatus,
			&c.Address, &c.REGON, &c.KRS, &c.LegalForm, &c.PKDMain, &c.CompanySize,
			&c.Website, &c.Email, &c.Phone, &c.BoardMembers, &c.FirstSeen, &c.LastSeen); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) MarkCompanyUnresolved(id int64) error {
	_, err := s.DB.Exec(`UPDATE companies SET nip_status='unresolved' WHERE id=?`, id)
	return err
}

func (s *Store) SetCompanyNIP(id int64, nip string) error {
	_, err := s.DB.Exec(`UPDATE companies SET nip=?, nip_status='verified' WHERE id=?`, nip, id)
	return err
}

// FillCompanyFields sets each non-empty value only where the current column
// is still empty — enrichment never overwrites earlier data.
func (s *Store) FillCompanyFields(id int64, f map[string]string) error {
	allowed := map[string]bool{
		"address": true, "regon": true, "krs": true, "legal_form": true,
		"pkd_main": true, "company_size": true, "website": true,
		"email": true, "phone": true, "board_members": true,
	}
	for col, val := range f {
		if val == "" || !allowed[col] {
			continue
		}
		if _, err := s.DB.Exec(
			`UPDATE companies SET `+col+` = ? WHERE id = ? AND `+col+` = ''`,
			val, id); err != nil {
			return err
		}
	}
	return nil
}
