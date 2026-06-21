package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/hrkono/lead-engine/internal/config"
	"github.com/hrkono/lead-engine/internal/deliver"
	"github.com/hrkono/lead-engine/internal/enrich"
	"github.com/hrkono/lead-engine/internal/enrich/bizraport"
	"github.com/hrkono/lead-engine/internal/enrich/krs"
	"github.com/hrkono/lead-engine/internal/enrich/regon"
	"github.com/hrkono/lead-engine/internal/ingest"
	"github.com/hrkono/lead-engine/internal/match"
	"github.com/hrkono/lead-engine/internal/qualify"
	"github.com/hrkono/lead-engine/internal/run"
	"github.com/hrkono/lead-engine/internal/store"
)

func newRunCmd() *cobra.Command {
	var cfgPath string
	var dryRun, resume, skipScrape bool
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Execute the daily pipeline",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return err
			}
			st, err := store.Open(cfg.DBPath)
			if err != nil {
				return err
			}
			defer st.Close()
			return runPipeline(cmd.Context(), cfg, st, dryRun, resume, skipScrape, cmd)
		},
	}
	cmd.Flags().StringVar(&cfgPath, "config", "/etc/lead-engine/config.toml", "config file")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "no Signal/Pipedrive sends; digest to stdout")
	cmd.Flags().BoolVar(&resume, "resume", false, "skip stages completed in the last failed run")
	cmd.Flags().BoolVar(&skipScrape, "skip-scrape", false, "ingest existing export files without scraping")
	return cmd
}

func runPipeline(ctx context.Context, cfg *config.Config, st *store.Store, dryRun, resume, skipScrape bool, cmd *cobra.Command) error {
	stats := deliver.RunStats{CapPLN: cfg.Bizraport.DailyCapPLN}
	warn := func(format string, a ...any) {
		stats.Warnings = append(stats.Warnings, fmt.Sprintf(format, a...))
	}

	bz := bizraport.New(bizraport.Options{Email: cfg.Bizraport.Email, Password: cfg.Bizraport.Password})
	rg := &regon.Client{APIKey: cfg.Regon.APIKey, Endpoint: cfg.Regon.Endpoint}
	kc := &krs.Client{}

	stages := []run.Stage{}
	if !skipScrape {
		stages = append(stages,
			run.ScraperStage("scrape-gov", cfg.Scrapers.GovCmd, cfg.Scrapers.GovExport),
			run.ScraperStage("scrape-olx", cfg.Scrapers.OlxCmd, cfg.Scrapers.OlxExport),
		)
	}
	stages = append(stages,
		run.Stage{Name: "ingest", Fn: func(ctx context.Context, _ int64) error {
			n1, err1 := ingest.Ingest(st, cfg.Scrapers.GovExport)
			stats.OffersCBOP = n1
			n2, err2 := ingest.Ingest(st, cfg.Scrapers.OlxExport)
			stats.OffersOLX = n2
			if err1 != nil {
				warn("gov ingest: %v", err1)
			}
			if err2 != nil {
				warn("olx ingest: %v", err2)
			}
			if err1 != nil && err2 != nil {
				return fmt.Errorf("both ingests failed: %v / %v", err1, err2)
			}
			return nil
		}},
		run.Stage{Name: "match", Fn: func(ctx context.Context, _ int64) error {
			_, err := match.Attach(st)
			return err
		}},
		run.Stage{Name: "resolve-nip", Fn: func(ctx context.Context, _ int64) error {
			if !bz.HasCredentials() {
				warn("bizraport: no credentials, skipping NIP resolution")
				return nil
			}
			rs, err := enrich.ResolveNIPs(ctx, st, bz, enrich.ResolveConfig{
				DailyCapPLN:   cfg.Bizraport.DailyCapPLN,
				CostPerRowPLN: cfg.Bizraport.CostPerRowPLN,
				MaxCandidates: cfg.Bizraport.MaxCandidates,
			})
			if rs.SkippedBudget > 0 {
				warn("bizraport: %d companies skipped (budget cap)", rs.SkippedBudget)
			}
			if rs.Errors > 0 {
				warn("bizraport: %d companies failed resolution (retried next run)", rs.Errors)
			}
			return err
		}},
		run.Stage{Name: "enrich", Fn: func(ctx context.Context, _ int64) error {
			es, err := enrich.Enrich(ctx, st, rg, kc)
			if es.Errors > 0 {
				warn("enrichment: %d lookups failed (partial data shipped)", es.Errors)
			}
			return err
		}},
		run.Stage{Name: "qualify", Fn: func(ctx context.Context, runID int64) error {
			_, err := qualify.BuildLeads(st, qualify.Config{
				SuppressionDays:     cfg.SuppressionDays,
				ScoreThreshold:      cfg.ScoreThreshold,
				ExcludedPKDPrefixes: cfg.ExcludedPKDPrefixes,
			}, runID)
			return err
		}},
		run.Stage{Name: "deliver", Fn: func(ctx context.Context, runID int64) error {
			return deliverStage(ctx, cfg, st, runID, &stats, dryRun, cmd)
		}},
	)

	r := &run.Runner{Store: st, Stages: stages, Resume: resume}
	return r.Run(ctx)
}

// deliverStage renders the digest, sends Signal, pushes verified qualified
// leads to Pipedrive, and records deliveries. In dry-run mode everything is
// rendered to stdout and lead state is left untouched.
func deliverStage(ctx context.Context, cfg *config.Config, st *store.Store, runID int64, stats *deliver.RunStats, dryRun bool, cmd *cobra.Command) error {
	spent, _ := st.SpendToday("bizraport")
	stats.SpendPLN = spent

	leads, err := st.DeliverableLeads(runID)
	if err != nil {
		return err
	}
	var verified, unverified []deliver.LeadView
	for _, l := range leads {
		v := leadView(l)
		if l.Company.NIPStatus == "verified" {
			verified = append(verified, v)
		} else {
			unverified = append(unverified, v)
		}
	}
	digest := deliver.RenderDigest(timeNowDate(), verified, unverified, *stats)

	if dryRun {
		fmt.Fprintln(cmd.OutOrStdout(), digest)
		fmt.Fprintf(cmd.OutOrStdout(), "[dry-run] would push %d leads to Pipedrive\n", len(verified))
		return nil
	}

	sig := &deliver.SignalClient{APIURL: cfg.Signal.APIURL, Number: cfg.Signal.Number,
		Recipients: []string{cfg.Signal.GroupID}}
	if err := sig.Send(ctx, digest); err != nil {
		return err // leads stay 'new' and roll into tomorrow's digest
	}

	pd := &deliver.PipedriveClient{BaseURL: cfg.Pipedrive.BaseURL, Token: cfg.Pipedrive.APIToken,
		StageID: cfg.Pipedrive.StageID, FieldKeys: cfg.Pipedrive.FieldKeys}
	for _, l := range leads {
		if err := st.MarkLeadDelivered(l.LeadID, "signal", 0, 0); err != nil {
			return err
		}
		if l.Company.NIPStatus != "verified" || !l.Qualified || cfg.Pipedrive.APIToken == "" {
			continue
		}
		res, err := pd.PushLead(ctx, pipedriveLead(l))
		if err != nil {
			log.Printf("pipedrive push failed for %s: %v", l.Company.Name, err)
			stats.Warnings = append(stats.Warnings, fmt.Sprintf("pipedrive %s: %v", l.Company.Name, err))
			continue
		}
		if err := st.MarkLeadDelivered(l.LeadID, "pipedrive", res.OrgID, res.DealID); err != nil {
			return err
		}
	}
	return nil
}

func leadView(l store.DeliverableLead) deliver.LeadView {
	var board []krs.BoardMember
	if l.Company.BoardMembers != "" {
		_ = json.Unmarshal([]byte(l.Company.BoardMembers), &board)
	}
	var boardStr []string
	for _, m := range board {
		boardStr = append(boardStr, m.Name+" ("+m.Role+")")
	}
	return deliver.LeadView{
		Company: l.Company.Name, NIP: l.Company.NIP, Positions: l.Positions,
		Location: l.Company.Address, Phone: l.Company.Phone, Email: l.Company.Email,
		Website: l.Company.Website, Score: l.Score, Board: boardStr,
	}
}

func pipedriveLead(l store.DeliverableLead) deliver.PipedriveLead {
	var board []krs.BoardMember
	if l.Company.BoardMembers != "" {
		_ = json.Unmarshal([]byte(l.Company.BoardMembers), &board)
	}
	var boardStr []string
	for _, m := range board {
		boardStr = append(boardStr, m.Name+" ("+m.Role+")")
	}
	return deliver.PipedriveLead{
		Company: l.Company.Name, NIP: l.Company.NIP, REGON: l.Company.REGON,
		KRS: l.Company.KRS, PKD: l.Company.PKDMain, Address: l.Company.Address,
		Website: l.Company.Website, Board: boardStr, Positions: l.Positions,
		NoteContent: "Source: lead-engine | positions: " + strings.Join(l.Positions, "; "),
	}
}

func timeNowDate() string { return time.Now().Format("2006-01-02") }
