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
	FetchBoard(ctx context.Context, krsNum string) ([]krs.BoardMember, error)
}

type Stats struct {
	Enriched int
	Errors   int
}

// Enrich fills missing registry fields on verified companies using the
// free APIs: REGON for contact/address/KRS number, then KRS for the board.
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
		if c.Phone == "" || c.Email == "" || c.Website == "" || c.Address == "" || c.KRS == "" || c.REGON == "" {
			rep, err := lookupRegonCached(ctx, st, rg, c.NIP)
			if err != nil {
				stats.Errors++
			} else if rep != nil {
				if err := st.FillCompanyFields(c.ID, map[string]string{
					"regon": rep.REGON, "krs": rep.KRS, "phone": rep.Phone,
					"email": rep.Email, "website": rep.Website, "address": rep.Address,
				}); err != nil {
					return stats, err
				}
				wrote = true
			}
		}
		// Re-read: REGON may have just supplied the KRS number.
		cur, err := st.FindCompanyByNIP(c.NIP)
		if err != nil || cur == nil {
			continue
		}
		if cur.KRS != "" && cur.BoardMembers == "" {
			board, err := fetchBoardCached(ctx, st, kc, cur.KRS)
			if err != nil {
				stats.Errors++
			} else {
				b, _ := json.Marshal(board) // nil → "null"; normalize to []
				if len(board) == 0 {
					b = []byte("[]")
				}
				if err := st.FillCompanyFields(cur.ID, map[string]string{"board_members": string(b)}); err != nil {
					return stats, err
				}
				wrote = true
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
	if raw, ok, _ := st.CacheGet("regon-nip", nip, cacheTTL); ok {
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
		st.CachePut("regon-nip", nip, []byte("null")) //nolint:errcheck
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if rep != nil {
		if raw, err := json.Marshal(rep); err == nil {
			st.CachePut("regon-nip", nip, raw) //nolint:errcheck
		}
	}
	return rep, nil
}

func fetchBoardCached(ctx context.Context, st *store.Store, kc KRSLookup, krsNum string) ([]krs.BoardMember, error) {
	if raw, ok, _ := st.CacheGet("krs-board", krsNum, cacheTTL); ok {
		var board []krs.BoardMember
		if err := json.Unmarshal(raw, &board); err == nil {
			return board, nil
		}
	}
	board, err := kc.FetchBoard(ctx, krsNum)
	if err != nil {
		return nil, err
	}
	if raw, err := json.Marshal(board); err == nil {
		st.CachePut("krs-board", krsNum, raw) //nolint:errcheck
	}
	return board, nil
}
