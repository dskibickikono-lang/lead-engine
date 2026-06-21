// Package enrich resolves company identity and fills registry data
// post-merge, per the spec's cost-optimal sequencing.
package enrich

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/hrkono/lead-engine/internal/enrich/bizraport"
	"github.com/hrkono/lead-engine/internal/match"
	"github.com/hrkono/lead-engine/internal/store"
)

const cacheTTL = 90 * 24 * time.Hour

// ResolveConfig controls cost and scope of a single ResolveNIPs run.
type ResolveConfig struct {
	DailyCapPLN   float64
	CostPerRowPLN float64
	MaxCandidates int
}

// ResolveStats summarises the outcome of a ResolveNIPs call.
type ResolveStats struct {
	Resolved      int
	Unresolved    int
	SkippedBudget int
	Errors        int // per-company lookup failures; the company is retried next run
}

// ResolveNIPs iterates over companies with nip_status='pending', queries
// BizRaport to find a confident NIP match, and writes verified identity back
// to the store. It stops processing a company (but does not fail) when
// adding another candidate would exceed the daily spend cap.
func ResolveNIPs(ctx context.Context, st *store.Store, bz *bizraport.Client, cfg ResolveConfig) (ResolveStats, error) {
	var stats ResolveStats
	if cfg.MaxCandidates <= 0 || cfg.CostPerRowPLN <= 0 {
		return stats, fmt.Errorf("resolve: MaxCandidates and CostPerRowPLN must be positive (got %d, %v) — refusing unbounded paid search", cfg.MaxCandidates, cfg.CostPerRowPLN)
	}
	pending, err := st.CompaniesPendingNIP()
	if err != nil {
		return stats, fmt.Errorf("resolve: %w", err)
	}
	for _, c := range pending {
		// Assumes the vendor honors the search limit; over-returned rows would bill past the reservation.
		// Worst case: search returns MaxCandidates rows + we fetch profiles.
		worst := float64(2*cfg.MaxCandidates) * cfg.CostPerRowPLN
		spent, err := st.SpendToday("bizraport")
		if err != nil {
			return stats, err
		}
		if spent+worst > cfg.DailyCapPLN {
			stats.SkippedBudget++
			continue // company stays pending; retried tomorrow
		}
		profile, paidRows, err := resolveByName(ctx, st, bz, c.Name, cfg.MaxCandidates)
		// Record any rows already billed before handling the error, so a paid
		// call that failed mid-way is still accounted for. NOTE: spend is logged
		// after the call returns — a crash in this window loses ≤ one company's
		// billing record (bounded, accepted; see Phase-1 audit M2).
		if paidRows > 0 {
			if serr := st.AddSpend("bizraport", float64(paidRows)*cfg.CostPerRowPLN); serr != nil {
				return stats, serr
			}
		}
		if err != nil {
			// Per-item API failure is non-blocking: count it, leave the company
			// pending, and let the next run retry it. Never strand the rest of
			// the batch because one BizRaport lookup failed.
			stats.Errors++
			continue
		}
		if profile == nil || profile.NIP == "" {
			if err := st.MarkCompanyUnresolved(c.ID); err != nil {
				return stats, err
			}
			stats.Unresolved++
			continue
		}
		targetID := c.ID
		existing, ferr := st.FindCompanyByNIP(profile.NIP)
		if ferr != nil {
			stats.Errors++
			continue
		}
		if existing != nil && existing.ID != c.ID {
			if err := st.MergeCompanies(c.ID, existing.ID); err != nil {
				return stats, err
			}
			targetID = existing.ID
		} else if err := st.SetCompanyNIP(c.ID, profile.NIP); err != nil {
			return stats, err
		}
		if err := applyProfile(st, targetID, profile); err != nil {
			return stats, err
		}
		stats.Resolved++
	}
	return stats, nil
}

// resolveByName mirrors the olx module's resolveProfile: bounded paid
// search, then verify the registry name matches before trusting a hit.
// Returns the profile (nil if no confident match) and the number of
// billable rows consumed. Both the search result list and each per-KRS
// profile are cached so that retries of unresolved companies are free
// within the cache TTL.
func resolveByName(ctx context.Context, st *store.Store, bz *bizraport.Client, name string, maxCandidates int) (*bizraport.CompanyProfile, int, error) {
	normalizedName := match.Normalize(name)
	paid := 0

	// Try to load the search result list from cache.
	var krsList []string
	if raw, ok, _ := st.CacheGet("bizraport-search", normalizedName, cacheTTL); ok {
		// Cache hit — search results are free.
		_ = json.Unmarshal(raw, &krsList)
	} else {
		// Cache miss — call the paid API.
		var err error
		krsList, _, err = bz.Search(ctx, name, maxCandidates)
		if err != nil {
			return nil, 0, err
		}
		paid = len(krsList) // /api/szukaj bills per returned row
		// Cache even an empty result: a definitive "no candidates" answer
		// must not be re-billed daily.
		if b, merr := json.Marshal(krsList); merr == nil {
			st.CachePut("bizraport-search", normalizedName, b)
		}
	}

	want := normalizedName
	var err error
	for _, krs := range krsList {
		var p *bizraport.CompanyProfile
		if raw, ok, _ := st.CacheGet("bizraport-krs", krs, cacheTTL); ok {
			p, err = bizraport.ParseProfile(raw)
		} else {
			p, err = bz.GetByKRS(ctx, krs)
			if err == nil && p != nil {
				paid++ // /api/dane bills the returned row
				st.CachePut("bizraport-krs", krs, p.Raw)
			}
		}
		if err != nil {
			return nil, paid, err
		}
		if p == nil {
			continue
		}
		if match.Normalize(p.Info.Nazwa) == want {
			return p, paid, nil
		}
	}
	return nil, paid, nil
}

// applyProfile writes non-empty BizRaport profile fields into the company row,
// never overwriting data that is already set.
func applyProfile(st *store.Store, companyID int64, p *bizraport.CompanyProfile) error {
	addr := strings.TrimSpace(strings.Join(nonEmpty(
		p.Info.Ulica, p.Info.KodPocztowy, p.Info.Miejscowosc), ", "))
	return st.FillCompanyFields(companyID, map[string]string{
		"regon":      p.Info.REGON,
		"krs":        p.KRS,
		"legal_form": p.Info.FormaPrawna,
		"pkd_main":   p.KodPKD,
		"website":    p.Info.StronaWWW,
		"email":      p.Info.Email,
		"address":    addr,
	})
}

func nonEmpty(parts ...string) []string {
	var out []string
	for _, p := range parts {
		if strings.TrimSpace(p) != "" {
			out = append(out, p)
		}
	}
	return out
}
