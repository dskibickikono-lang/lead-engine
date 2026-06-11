package store

import (
	"database/sql"
	"errors"
	"fmt"
)

type Company struct {
	ID             int64
	NIP            string
	Name           string
	NormalizedName string
	NIPStatus      string
	Address        string
	REGON          string
	KRS            string
	LegalForm      string
	PKDMain        string
	CompanySize    string
	Website        string
	Email          string
	Phone          string
	BoardMembers   string
	FirstSeen      string
	LastSeen       string
}

const companyCols = `id, COALESCE(nip,''), name, normalized_name, nip_status,
	address, regon, krs, legal_form, pkd_main, company_size, website, email,
	phone, board_members, first_seen, last_seen`

func scanCompany(row *sql.Row) (*Company, error) {
	var c Company
	err := row.Scan(&c.ID, &c.NIP, &c.Name, &c.NormalizedName, &c.NIPStatus,
		&c.Address, &c.REGON, &c.KRS, &c.LegalForm, &c.PKDMain, &c.CompanySize,
		&c.Website, &c.Email, &c.Phone, &c.BoardMembers, &c.FirstSeen, &c.LastSeen)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func (s *Store) FindCompanyByNIP(nip string) (*Company, error) {
	return scanCompany(s.DB.QueryRow(
		`SELECT `+companyCols+` FROM companies WHERE nip = ?`, nip))
}

func (s *Store) FindCompanyByNormalizedName(norm string) (*Company, error) {
	return scanCompany(s.DB.QueryRow(
		`SELECT `+companyCols+` FROM companies WHERE normalized_name = ? ORDER BY id LIMIT 1`, norm))
}

func (s *Store) CreateCompany(nip, name, norm, status string) (int64, error) {
	res, err := s.DB.Exec(`INSERT INTO companies
		(nip, name, normalized_name, nip_status, first_seen, last_seen)
		VALUES (?,?,?,?,datetime('now'),datetime('now'))`,
		nullIfEmpty(nip), name, norm, status)
	if err != nil {
		return 0, fmt.Errorf("create company %q: %w", name, err)
	}
	return res.LastInsertId()
}

func (s *Store) AttachOffer(source, externalID string, companyID int64) error {
	_, err := s.DB.Exec(`UPDATE raw_offers SET company_id = ?
		WHERE source = ? AND external_id = ?`, companyID, source, externalID)
	return err
}

// PromoteCompanyNIP sets the NIP on a provisional (NIP-less) company and
// marks it verified. Caller must ensure no other row holds this NIP.
func (s *Store) PromoteCompanyNIP(id int64, nip string) error {
	_, err := s.DB.Exec(`UPDATE companies SET nip=?, nip_status='verified' WHERE id=?`, nip, id)
	return err
}

func (s *Store) TouchCompany(id int64) error {
	_, err := s.DB.Exec(`UPDATE companies SET last_seen = datetime('now') WHERE id = ?`, id)
	return err
}

func (s *Store) UnattachedOffers() ([]RawOffer, error) {
	rows, err := s.DB.Query(`SELECT source, external_id, COALESCE(nip,''),
		company_name FROM raw_offers WHERE company_id IS NULL ORDER BY source, external_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RawOffer
	for rows.Next() {
		var o RawOffer
		if err := rows.Scan(&o.Source, &o.ExternalID, &o.NIP, &o.CompanyName); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// MergeCompanies repoints everything from src onto dst and deletes src.
// Used when NIP resolution discovers a provisional company is an existing one.
func (s *Store) MergeCompanies(srcID, dstID int64) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE raw_offers SET company_id=? WHERE company_id=?`, dstID, srcID); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE leads SET company_id=? WHERE company_id=?`, dstID, srcID); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM companies WHERE id=?`, srcID); err != nil {
		return err
	}
	return tx.Commit()
}
