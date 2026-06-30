package enrich

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/hrkono/lead-engine/internal/enrich/krs"
	"github.com/hrkono/lead-engine/internal/enrich/regon"
	"github.com/hrkono/lead-engine/internal/store"
)

// RegonLookup and KRSLookup abstract the API clients for testability.
type RegonLookup interface {
	LookupByNIP(ctx context.Context, nip string) (*regon.Report, error)
}

type KRSLookup interface {
	FetchProfile(ctx context.Context, krsNum string) (*krs.Profile, error)
}

type Stats struct {
	Enriched int
	Errors   int
}

// Enrich fills missing registry fields on verified companies using the free
// APIs: REGON for contact/address/KRS number plus business fields (headcount,
// legal form, registration date), then KRS for the board and share capital.
// Failures are per-company and non-blocking: the company ships partial and
// is retried on the next run.
func Enrich(ctx context.Context, st *store.Store, rg RegonLookup, kc KRSLookup) (Stats, error) {
	var stats Stats
	if _, err := FillFromGovExtras(st); err != nil {
		return stats, fmt.Errorf("enrich: gov extras: %w", err)
	}
	companies, err := st.CompaniesNeedingEnrichment()
	if err != nil {
		return stats, fmt.Errorf("enrich: %w", err)
	}
	for _, c := range companies {
		if err := ctx.Err(); err != nil {
			return stats, err
		}
		wrote := false // did this company actually gain registry data this run?
		if c.Phone == "" || c.Email == "" || c.Website == "" || c.Address == "" ||
			c.KRS == "" || c.REGON == "" || c.Headcount == "" || c.LegalForm == "" || c.RegisteredSince == "" {
			rep, err := lookupRegonCached(ctx, st, rg, c.NIP)
			if err != nil {
				stats.Errors++
			} else if rep != nil {
				if err := st.FillCompanyFields(c.ID, map[string]string{
					"regon": rep.REGON, "krs": rep.KRS, "phone": rep.Phone,
					"email": rep.Email, "website": rep.Website, "address": rep.Address,
					"headcount": rep.Headcount, "legal_form": rep.LegalForm,
					"registered_since": rep.RegisteredSince,
				}); err != nil {
					return stats, err
				}
				// Count only a genuine first-time fill, not a no-op re-read of an
				// already-complete company selected because a field BIR can't
				// supply (e.g. headcount for a sole trader) stays empty.
				if c.REGON == "" {
					wrote = true
				}
			}
		}
		// Re-read: REGON may have just supplied the KRS number.
		cur, err := st.FindCompanyByNIP(c.NIP)
		if err != nil || cur == nil {
			continue
		}
		if cur.KRS != "" && (cur.BoardMembers == "" || cur.ShareCapital == "") {
			prof, err := fetchProfileCached(ctx, st, kc, cur.KRS)
			if err != nil {
				stats.Errors++
			} else if prof != nil {
				fields := map[string]string{}
				if cur.BoardMembers == "" {
					b, _ := json.Marshal(prof.Board) // nil → "null"; normalize to []
					if len(prof.Board) == 0 {
						b = []byte("[]")
					}
					fields["board_members"] = string(b)
					wrote = true
				}
				if cur.ShareCapital == "" && prof.ShareCapital != "" {
					fields["share_capital"] = prof.ShareCapital
					wrote = true
				}
				if len(fields) > 0 {
					if err := st.FillCompanyFields(cur.ID, fields); err != nil {
						return stats, err
					}
				}
			}
		}
		// Count only companies that actually gained data — a lookup error or an
		// already-complete company is not an enrichment.
		if wrote {
			stats.Enriched++
		}
	}
	return stats, nil
}

// lookupRegonCached caches both hits and definitive not-found answers;
// transport/session errors are NOT cached so the next run retries.
func lookupRegonCached(ctx context.Context, st *store.Store, rg RegonLookup, nip string) (*regon.Report, error) {
	// Cache key carries a version suffix: bumping it invalidates reports cached
	// before the business fields (headcount/legal_form/registered_since) were
	// captured, so the existing company base backfills them on the next run.
	const cacheKey = "regon-nip-v2"
	if raw, ok, _ := st.CacheGet(cacheKey, nip, cacheTTL); ok {
		if string(raw) == "null" { // cached not-found
			return nil, nil
		}
		var rep regon.Report
		if err := json.Unmarshal(raw, &rep); err == nil {
			return &rep, nil
		}
	}
	rep, err := rg.LookupByNIP(ctx, nip)
	if errors.Is(err, regon.ErrNotFound) {
		st.CachePut(cacheKey, nip, []byte("null")) //nolint:errcheck
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if rep != nil {
		if raw, err := json.Marshal(rep); err == nil {
			st.CachePut(cacheKey, nip, raw) //nolint:errcheck
		}
	}
	return rep, nil
}

func fetchProfileCached(ctx context.Context, st *store.Store, kc KRSLookup, krsNum string) (*krs.Profile, error) {
	if raw, ok, _ := st.CacheGet("krs-profile", krsNum, cacheTTL); ok {
		var prof krs.Profile
		if err := json.Unmarshal(raw, &prof); err == nil {
			return &prof, nil
		}
	}
	prof, err := kc.FetchProfile(ctx, krsNum)
	if err != nil {
		return nil, err
	}
	if prof == nil { // 404 — no entity; nothing to cache
		return nil, nil
	}
	if raw, err := json.Marshal(prof); err == nil {
		st.CachePut("krs-profile", krsNum, raw) //nolint:errcheck
	}
	return prof, nil
}
