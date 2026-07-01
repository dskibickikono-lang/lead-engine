package store

// CompaniesPendingNIP returns pending or previously-unresolved companies;
// unresolved ones are retried because cached lookups make retries free within
// the cache TTL.
func (s *Store) CompaniesPendingNIP() ([]Company, error) {
	return s.queryCompanies(`SELECT ` + companyCols + ` FROM companies
		WHERE nip_status IN ('pending','unresolved') ORDER BY id`)
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
		"headcount": true, "share_capital": true, "registered_since": true,
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

// CompaniesNeedingEnrichment returns verified companies missing any
// enrichment field that the free APIs can supply. headcount/share_capital/
// registered_since are included so the existing company base backfills the
// business fields; companies that legitimately lack them (sole traders, firms
// with no share capital) stay selected but do no real work — the cached REGON/
// KRS responses make the repeated lookups free, matching the existing
// unresolved-NIP retry policy.
func (s *Store) CompaniesNeedingEnrichment() ([]Company, error) {
	return s.queryCompanies(`SELECT ` + companyCols + ` FROM companies
		WHERE nip_status = 'verified'
		  AND (phone='' OR email='' OR website='' OR address='' OR krs='' OR regon=''
		       OR board_members='' OR headcount='' OR share_capital='' OR registered_since='')
		ORDER BY id`)
}
