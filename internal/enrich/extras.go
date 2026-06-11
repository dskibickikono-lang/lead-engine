package enrich

import (
	"encoding/json"
	"fmt"

	"github.com/hrkono/lead-engine/internal/store"
)

// FillFromGovExtras copies registry data the gov_api export already carries
// (extra.regon in each cbop offer payload) onto the company rows — free and
// local, so the PKD agency filter never depends on a live REGON call.
//
// The gov_api exporter embeds a "regon" object inside each offer's extra field
// with keys: pkdMain, companySize, registeredSince, legalForm.
// These are filled onto companies.pkd_main, company_size, legal_form (and
// regon number is NOT in this block — it comes from the REGON API separately).
//
// FillCompanyFields never overwrites existing values, so this is idempotent.
func FillFromGovExtras(st *store.Store) (int, error) {
	rows, err := st.DB.Query(`
		SELECT c.id, o.payload
		FROM companies c
		JOIN raw_offers o ON o.company_id = c.id AND o.source = 'cbop' AND o.payload != ''
		WHERE c.pkd_main='' OR c.company_size='' OR c.legal_form=''
	`)
	if err != nil {
		return 0, fmt.Errorf("fill_gov_extras: query: %w", err)
	}
	// Drain the cursor before writing: the store runs on a single connection
	// (SetMaxOpenConns(1)), so an Exec while rows are open would deadlock.
	type pair struct {
		companyID int64
		payload   string
	}
	var pairs []pair
	seen := make(map[int64]struct{})
	for rows.Next() {
		var p pair
		if err := rows.Scan(&p.companyID, &p.payload); err != nil {
			rows.Close()
			return 0, fmt.Errorf("fill_gov_extras: scan: %w", err)
		}
		// Only the first offer per company — FillCompanyFields never
		// overwrites, so subsequent offers would be harmless but wasteful.
		if _, already := seen[p.companyID]; already {
			continue
		}
		seen[p.companyID] = struct{}{}
		pairs = append(pairs, p)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, fmt.Errorf("fill_gov_extras: rows: %w", err)
	}
	rows.Close()

	count := 0
	for _, p := range pairs {
		fields, err := extractGovRegonFields(p.payload)
		if err != nil || len(fields) == 0 {
			continue
		}
		if err := st.FillCompanyFields(p.companyID, fields); err != nil {
			return count, fmt.Errorf("fill_gov_extras: fill company %d: %w", p.companyID, err)
		}
		count++
	}
	return count, nil
}

// extractGovRegonFields parses a cbop offer payload JSON and returns any
// non-empty company fields present in extra.regon.
// Keys in the gov_api export:
//
//	pkdMain       → pkd_main
//	companySize   → company_size
//	legalForm     → legal_form
//
// registeredSince has no companies column, so it is skipped.
// pkdSegment comes from the scoring breakdown, not REGON data, and is also skipped.
// Returns nil, nil when extra.regon is absent or null.
func extractGovRegonFields(payload string) (map[string]string, error) {
	var offer struct {
		Extra map[string]any `json:"extra"`
	}
	if err := json.Unmarshal([]byte(payload), &offer); err != nil {
		return nil, fmt.Errorf("extractGovRegonFields: unmarshal: %w", err)
	}
	if offer.Extra == nil {
		return nil, nil
	}
	regonRaw, ok := offer.Extra["regon"]
	if !ok || regonRaw == nil {
		return nil, nil
	}
	regon, ok := regonRaw.(map[string]any)
	if !ok {
		return nil, nil
	}

	result := make(map[string]string)
	if v := strVal(regon, "pkdMain"); v != "" {
		result["pkd_main"] = v
	}
	if v := strVal(regon, "companySize"); v != "" {
		result["company_size"] = v
	}
	if v := strVal(regon, "legalForm"); v != "" {
		result["legal_form"] = v
	}
	if len(result) == 0 {
		return nil, nil
	}
	return result, nil
}

// strVal reads a string value from a map[string]any, returning "" for missing
// or non-string entries.
func strVal(m map[string]any, key string) string {
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return s
}
