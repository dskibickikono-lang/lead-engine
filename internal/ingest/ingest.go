// Package ingest loads raw-leads contract files into raw_offers.
package ingest

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/hrkono/lead-engine/internal/contract"
	"github.com/hrkono/lead-engine/internal/store"
)

// Ingest upserts every offer from the contract file at path. On error it
// returns the number upserted so far; re-running is safe (idempotent upsert).
func Ingest(st *store.Store, path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("ingest %s: %w", path, err)
	}
	f, err := contract.Parse(data)
	if err != nil {
		return 0, fmt.Errorf("ingest %s: %w", path, err)
	}
	n := 0
	for _, o := range f.Offers {
		payload, _ := json.Marshal(o)
		err := st.UpsertRawOffer(store.RawOffer{
			Source:        f.Source,
			ExternalID:    o.ExternalID,
			NIP:           o.NIP,
			CompanyName:   o.CompanyName,
			Position:      o.Position,
			Location:      o.Location,
			Vacancies:     o.Vacancies,
			SalaryFrom:    o.SalaryFrom,
			SalaryTo:      o.SalaryTo,
			Phone:         o.Phone,
			Email:         o.Email,
			URL:           o.URL,
			ContactPerson: o.ContactPerson,
			WorkLocation:  o.WorkLocation,
			Website:       o.Website,
			Score:         o.Score,
			ScrapedAt:     o.ScrapedAt,
			Payload:       string(payload),
		})
		if err != nil {
			return n, fmt.Errorf("ingest %s offer %s: %w", path, o.ExternalID, err)
		}
		n++
	}
	return n, nil
}
