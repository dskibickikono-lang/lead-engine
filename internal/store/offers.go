package store

import "database/sql"

type RawOffer struct {
	Source      string
	ExternalID  string
	NIP         string
	CompanyName string
	Position    string
	Location    string
	Vacancies   int
	SalaryFrom  *float64
	SalaryTo    *float64
	Phone       string
	Email       string
	Score       *int
	ScrapedAt   string
	Payload     string
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func (s *Store) UpsertRawOffer(o RawOffer) error {
	_, err := s.DB.Exec(`INSERT INTO raw_offers
		(source, external_id, nip, company_name, position, location, vacancies,
		 salary_from, salary_to, phone, email, score, scraped_at, ingested_at, payload)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,datetime('now'),?)
		ON CONFLICT(source, external_id) DO UPDATE SET
		 nip=excluded.nip, company_name=excluded.company_name,
		 position=excluded.position, location=excluded.location,
		 vacancies=excluded.vacancies, salary_from=excluded.salary_from,
		 salary_to=excluded.salary_to, phone=excluded.phone,
		 email=excluded.email, score=excluded.score,
		 scraped_at=excluded.scraped_at, payload=excluded.payload`,
		o.Source, o.ExternalID, nullIfEmpty(o.NIP), o.CompanyName, o.Position,
		o.Location, o.Vacancies, o.SalaryFrom, o.SalaryTo, o.Phone, o.Email,
		o.Score, o.ScrapedAt, o.Payload)
	return err
}

// scanNullStr reads a nullable TEXT column into "".
func scanNullStr(ns sql.NullString) string {
	if ns.Valid {
		return ns.String
	}
	return ""
}
