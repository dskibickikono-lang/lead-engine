# CLAUDE.md — lead-engine

Orchestrator for a B2B lead-generation pipeline serving a blue-collar staffing
agency (HR KONO / APT Work). This Go binary unifies two scrapers, deduplicates
and enriches companies, and delivers a daily lead digest to Signal + Pipedrive.

> **Monorepo with submodules.** This repo is the orchestrator. The two scrapers
> live in `gov_api/` and `olx-printing-press/` as git submodules — each has its
> own `CLAUDE.md`. Read those before touching scraper code; this file covers the
> orchestrator (`cmd/`, `internal/`).

---

## What this does (data flow)

```
scrape (gov + olx)  →  ingest  →  match  →  resolve-nip  →  enrich  →  qualify  →  deliver
   subprocess           JSON      company    BizRaport      REGON+KRS   leads +     Signal +
   → export JSON      → raw_offers  rows     (paid, capped) (free)      suppression Pipedrive
```

- **scrape**: runs each scraper as an external subprocess (`Scrapers.GovCmd` /
  `OlxCmd`), then verifies its export file exists. Skipped with `--skip-scrape`.
- **ingest**: parses the versioned **raw-leads JSON contract** and upserts into
  `raw_offers` (idempotent).
- **match**: attaches offers to `companies`. NIP is canonical identity; NIP-less
  offers (OLX) fall back to normalized-name matching.
- **resolve-nip**: for `nip_status='pending'` companies, queries BizRaport (paid)
  to find a confident NIP, bounded by a daily PLN spend cap.
- **enrich**: fills registry fields on verified companies via **free** APIs —
  REGON BIR (phone/email/address/KRS number) then MS KRS (board members).
- **qualify**: turns enriched companies with fresh offers into leads, applying
  PKD exclusion + recent-delivery suppression + score threshold.
- **deliver**: renders the digest, sends Signal, pushes verified+qualified leads
  to Pipedrive, records deliveries.

The design rationale lives in `docs/superpowers/specs/2026-06-11-lead-generator-design.md`
and the plan in `docs/superpowers/plans/`. `REVIEW.md` is an architecture review.

---

## Layout

```
cmd/lead-engine/main.go          entry point → cli.Execute()
internal/
  cli/        root.go, run.go (pipeline wiring), pipedrive_setup.go
  config/     TOML config load + defaults/validation
  contract/   raw-leads JSON contract (v1) + testdata/ fixtures (normative)
  ingest/     contract file → raw_offers
  match/      offer → company attach; normalize.go (name canonicalization)
  enrich/     resolve.go (NIP via BizRaport), enrich.go (REGON+KRS), extras.go
    regon/    REGON BIR SOAP client (+ envelopes.go)
    krs/      MS KRS REST client (board members)
    bizraport/ BizRaport client (paid NIP resolution)
  qualify/    company → lead (suppression + qualification)
  deliver/    digest.go, signal.go, pipedrive.go
  run/        runner.go — stage sequencer with status recording
  store/      SQLite leads.db: schema + per-table ops
config.example.toml
docs/         DEPLOY.md, LIVE-INTEGRATION-CHECKLIST.md, superpowers/
```

## Stack
- **Go 1.25**, `github.com/hrkono/lead-engine` (note: module path ≠ GitHub org).
- `spf13/cobra` (CLI), `BurntSushi/toml` (config), `modernc.org/sqlite` (pure-Go,
  no cgo).
- SQLite store `leads.db` owned by this repo (WAL, `foreign_keys=1`, single writer).

---

## Build, test, run

```bash
make setup        # git submodule update --init --recursive
make build        # → bin/lead-engine
make test         # go test ./...
go vet ./...

# Smoke test without scraping or external sends:
lead-engine run --config config.toml --skip-scrape --dry-run
```

`run` flags: `--config` (default `/etc/lead-engine/config.toml`), `--dry-run`
(digest to stdout, no Signal/Pipedrive, lead state untouched), `--resume` (skip
stages that succeeded in the last failed run), `--skip-scrape`.

Other command: `lead-engine pipedrive setup` — creates the custom Organization
fields and prints the `[pipedrive.field_keys]` TOML block for the config.

Deployment (VPS, single cron entry point at 05:00) is documented in
`docs/DEPLOY.md`; complete `docs/LIVE-INTEGRATION-CHECKLIST.md` before the first
production run.

---

## The raw-leads contract (the seam between scrapers and orchestrator)

`internal/contract/contract.go` defines `contractVersion: 1`, `source` is
`"cbop"` or `"olx"`, and an `offers[]` array. **The fixtures in
`internal/contract/testdata/` are normative** — scraper exporter tests assert
against the same shapes.

Conventions:
- `externalId` and `companyName` are **required**; missing either fails parse.
- For string fields, JSON `null` and `""` are equivalent ("unknown").
- Unknown keys are ignored → fields can be added within v1 without breaking older
  parsers. **Non-schema data goes in the `extra` map**, never as new top-level keys.
- `score` is `*int` (null for OLX-only, which has no scorer); `salaryFrom/To` are
  `*float64`.

If you change the contract, update `contract.go`, both `testdata/` fixtures, and
the exporters in **both** scraper submodules in lockstep.

---

## Store / schema (`internal/store`)

Tables: `raw_offers`, `companies`, `leads`, `deliveries`, `api_cache`,
`spend_log`, `runs`, `run_stages`. Schema is the `schema` const in `store.go`,
applied idempotently on `Open` via `CREATE TABLE IF NOT EXISTS`.

- **`companies.nip_status`**: `pending` → `verified` (has NIP) → `unresolved`.
  Only `verified` companies are enriched and pushed to Pipedrive.
- `companies.board_members` is a JSON array `[{"name":..,"role":..}]`.
- `leads.score` is NULL for OLX-only leads; `status` is `new | delivered | suppressed`.
- `api_cache` is the generic cache for REGON/KRS/BizRaport (keyed `api`+`identifier`;
  resolve uses a 90-day TTL). `spend_log` backs the BizRaport daily cap.
- Single writer: `db.SetMaxOpenConns(1)` to avoid `SQLITE_BUSY`. Don't raise this
  without adding app-level lock-retry handling.

**Schema changes**: edit the `schema` const. Because it's `IF NOT EXISTS`-only,
adding a column to an existing table on a live DB requires an explicit
`ALTER TABLE` migration — `CREATE TABLE IF NOT EXISTS` will not migrate an
already-created table.

---

## Conventions & hard rules

- **Graceful degradation, not fail-fast.** A failing stage is recorded in
  `run_stages` and the run *continues*; the runner returns one combined error at
  the end so cron alerts (non-zero exit + `MAILTO`). Don't convert stage errors
  into early `return`s that abort the whole pipeline. See `run/runner.go`.
- **Per-item failures are non-blocking.** Enrichment/resolution failures are
  counted (`stats.Errors`), the company ships partial, and it's retried next run.
  Never abort a batch because one company's API lookup failed.
- **Cost safety is mandatory.** Any paid API (BizRaport) must respect the daily
  PLN cap via `spend_log`/`SpendToday`. `ResolveNIPs` refuses to run with a
  non-positive `MaxCandidates`/`CostPerRowPLN` rather than do an unbounded paid
  search. Free APIs (REGON, KRS) have no cap but must be cached in `api_cache`.
- **Identity rules**: NIP is canonical. NIP-less name matching can mis-merge
  similarly-named businesses — an accepted trade-off for OLX. Unresolved-NIP
  (OLX) leads are delivered to Signal flagged unverified but **never** pushed to
  Pipedrive and **never** registry-enriched.
- **`--dry-run` must have zero external side effects** and must not mutate lead
  state — keep it that way.
- **`ScraperStage` execs the binary directly (no shell).** Wrapper scripts need a
  shebang and the execute bit. cmd[0] is the binary, the rest are args.
- Wrap errors with `fmt.Errorf("...: %w", err)` and context (stage/source/id), as
  the existing code does. No silent error swallowing.
- Secrets (BizRaport, REGON, Pipedrive, Signal) live only in `config.toml` / env,
  never in code or logs.

## Tests
- Standard Go `*_test.go` beside each package; run with `make test`.
- Store-backed tests open a fresh DB in `t.TempDir()` and ingest the
  `internal/contract/testdata/` fixtures — reuse that pattern for new pipeline
  tests rather than hand-building rows.
- API clients (REGON/KRS/BizRaport) are behind interfaces (`RegonLookup`,
  `KRSLookup`) for table-driven tests with `httptest`/fakes — no live calls.

## Git
- Work on the designated feature branch; branch + PR, never push to `main`.
- `bin/`, `*.db*`, and `/data/` are gitignored.
- Submodules can drift: after a plain `git pull`, run `make setup`. A stale
  submodule can produce an invalid contract that fails ingest.
