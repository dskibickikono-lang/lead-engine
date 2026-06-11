// Package contract defines the raw-leads JSON interchange format (v1)
// produced by both scrapers and consumed by lead-engine. The fixtures in
// testdata/ are the normative examples; scraper exporter tests assert
// against the same shapes (parse-compatible, not byte-identical).
//
// Contract conventions:
//   - For string fields, JSON null and "" are equivalent ("unknown");
//     exporters may emit either.
//   - Unknown top-level/offer keys are ignored, so fields can be added
//     within v1 without breaking older parsers; non-schema data belongs
//     in "extra".
package contract

import (
	"encoding/json"
	"fmt"
)

type File struct {
	ContractVersion int     `json:"contractVersion"`
	Source          string  `json:"source"` // "cbop" | "olx"
	ExportedAt      string  `json:"exportedAt"`
	Offers          []Offer `json:"offers"`
}

type Offer struct {
	ExternalID  string         `json:"externalId"`
	NIP         string         `json:"nip"` // "" when unknown (JSON null)
	CompanyName string         `json:"companyName"`
	Position    string         `json:"position"`
	Location    string         `json:"location"`
	Vacancies   int            `json:"vacancies"`
	SalaryFrom  *float64       `json:"salaryFrom"`
	SalaryTo    *float64       `json:"salaryTo"`
	Phone       string         `json:"phone"`
	Email       string         `json:"email"`
	Score       *int           `json:"score"`
	ScrapedAt   string         `json:"scrapedAt"`
	Extra       map[string]any `json:"extra"`
}

func Parse(data []byte) (*File, error) {
	var f File
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("contract: %w", err)
	}
	if f.ContractVersion != 1 {
		return nil, fmt.Errorf("contract: unsupported contractVersion %d", f.ContractVersion)
	}
	if f.Source != "cbop" && f.Source != "olx" {
		return nil, fmt.Errorf("contract: unknown source %q", f.Source)
	}
	for i, o := range f.Offers {
		if o.ExternalID == "" {
			return nil, fmt.Errorf("contract: offers[%d] missing externalId", i)
		}
		if o.CompanyName == "" {
			return nil, fmt.Errorf("contract: offers[%d] missing companyName", i)
		}
	}
	return &f, nil
}
