# Lead Generator — Unified Pipeline Design

**Date:** 2026-06-11
**Status:** Approved (brainstorm with D, 2026-06-10)
**Scope:** New `lead-engine` orchestrator unifying `gov_api` (Python, CBOP) and `printing-press/olx` (Go, OLX) into one daily B2B lead pipeline for a blue-collar staffing agency, delivering to Signal and Pipedrive.

---

## 1. Context and corrections to prior assumptions

Findings from code exploration that override the root `CLAUDE.md`:

- **`gov_api` is Python 3.12** (not Go). Pipeline `fetcher → storage → enricher (REGON BIR1.1) → scorer → exporter`, SQLite, tested, with a documented JSON export format (`gov_api_v2`).
- **No KRS integration exists in `gov_api`** — nothing to fix; a KRS client must be built fresh. The free MS KRS API (`api-krs.ms.gov.pl`) is the source for KRS extracts and **board members** (required lead field; BizRaport does not return them).
- **BizRaport profile** returns KRS, NIP, REGON, PKD, address, legal form, share capital, email, website. The olx module's NIP-by-name resolution already bounds the paid search, verifies normalized-name matches, and caches per KRS.
- **No Signal or Pipedrive code exists** in either module; both are greenfield.
- **REGON BIR full report** (free) already yields phone, email, and the KRS number for legal entities.
- `gov_api` and `printing-press/olx` are separate git repos; `lead-engine` is a third, new repo.

## 2. Decisions (from brainstorm Q&A)

| Decision | Choice |
|---|---|
| Language topology | Polyglot: scrapers unchanged (Python + Go); new **Go orchestrator** |
| Orchestrator home | New repo `~/projects/lead-engine` |
| Unified store | New SQLite `leads.db` owned by lead-engine |
| Ingestion | **Versioned raw-leads JSON export contract**; orchestrator runs scrapers as subprocesses and ingests their export files |
| Signal | `bbernhard/signal-cli-rest-api` in Docker on the VPS, bot number, group message via `POST /v2/send` |
| Pipedrive duplicates | Match org by NIP custom field; new deal on existing org only if no open deal, else note on the open deal; never duplicate orgs |
| Unresolved-NIP OLX leads | Delivered in Signal flagged "unverified — no NIP"; **not** pushed to Pipedrive; not registry-enriched; retried next run |
| BizRaport spend | **Hard daily PLN cap** (config), enforced by a spend ledger; cap reached → unresolved path |

## 3. Enrichment-placement hypothesis: verdict

**Validated with one deliberate exception.** Identity-level and paid enrichment (BizRaport NIP resolution, KRS extract, board members, final field completion) happens **after** both sources merge into `leads.db` — post-merge is where dedup guarantees each company is enriched at most once.

**Exception:** `gov_api`'s internal REGON enrichment stays in `gov_api`. It is free, cached (`regon_cache`), and load-bearing: the scorer and the competitor-agency filter (PKD 77/78) need PKD *before* export. Moving it would be a rewrite with zero cost savings. The orchestrator has its own Go REGON client for OLX-side companies.

## 4. Unified data model (`leads.db`)

- **`raw_offers`** — every ingested offer. Unique `(source, external_id)`; `source` ∈ {`cbop`, `olx`}; nip (nullable), raw company name, position title, location, vacancies, salary range, contact phone/email (OLX direct), scraped_at, payload JSON. Upserts are idempotent.
- **`companies`** — one row per real-world company; **NIP is canonical identity** (unique when present). Fields: name, address, REGON, KRS, legal form, PKD main + segment, company size, website, email, phone, `board_members` (JSON array of names/roles), `nip_status` ∈ {`verified`, `unresolved`, `pending`}, per-field provenance/timestamps, sources seen, first/last seen.
- **`leads`** — deliverable unit: company + aggregated open positions for the day, score, qualified flag, status `new → enriched → delivered | suppressed`.
- **`deliveries`** — audit per channel: signal/pipedrive, timestamps, Pipedrive org/deal IDs. Basis for idempotent re-runs and suppression-over-time.
- **`api_cache`** — cached REGON/KRS/BizRaport responses, keyed `(api, identifier)`, TTL 90 days (registry data changes slowly).
- **`spend_log`** — per-day PLN spent on BizRaport; enforces the daily cap.
- **`runs`** — per-stage status for resumability.

## 5. Raw-leads JSON contract (v1)

Each scraper ends its run by writing `raw-leads-{source}-{date}.json`:

```json
{
  "contractVersion": 1,
  "source": "cbop | olx",
  "exportedAt": "ISO-8601",
  "offers": [{
    "externalId": "source-namespaced id",
    "nip": "string|null",
    "companyName": "string",
    "position": "string",
    "location": "string",
    "vacancies": 1,
    "salaryFrom": null, "salaryTo": null,
    "phone": "string|null", "email": "string|null",
    "score": null,
    "scrapedAt": "ISO-8601",
    "extra": { }
  }]
}
```

- `gov_api`: adapt the existing `gov_api_v2` exporter (additive; includes its score and REGON-derived fields in `extra`).
- `olx`: new export format in the existing `export` command (additive; includes OLX phone data and job-count analytics in `extra`).
- Contract fixtures live in lead-engine; both exporters are tested against the same fixtures.

## 6. Pipeline stages (one `lead-engine run`)

1. **Scrape** — run `gov_api` (python, venv) and `olx-pp-cli sync` as subprocesses; each exports raw-leads JSON.
2. **Ingest** — upsert both files into `raw_offers`.
3. **Match & merge** — attach offers to `companies` by NIP; same NIP from both sources merges automatically. OLX offers without NIP attach to a provisional company row by normalized name.
4. **Resolve NIP** (OLX-only) — BizRaport verified-name search under the daily cap; one paid resolution also returns the full profile (NIP, KRS, REGON, address, email, website), so the paid call doubles as enrichment.
5. **Enrich** — free APIs fill the gaps (see §7).
6. **Deduplicate & qualify** — suppress companies delivered within the last N days (config, default 30); exclude agencies (PKD 77/78); apply score threshold.
7. **Deliver** — Signal digest + Pipedrive push (verified only).
8. **Report** — run stats appended to the digest.

## 7. Enrichment sequencing (cost-optimal, per source)

**Gov lead (NIP known):**
1. REGON data already in the export (PKD, size, address, phone, email, legal form) — free.
2. REGON report yields the KRS number → **KRS API** (free) → board members.
3. BizRaport never called. **Cost: 0 PLN.**

**OLX lead (no NIP):**
1. BizRaport name search → verified match → profile (paid, capped). Yields NIP + most registry fields.
2. KRS API (free) → board members.
3. REGON API (free) → remaining gaps (PKD, company size).
4. No match / cap exhausted → `unresolved`: Signal-only flagged lead, retried next run; cache prevents re-paying for the same search within TTL.

**KRS client (new, Go, in lead-engine):** `GET /api/krs/OdpisAktualny/{krs}?rejestr=P&format=json`; parse `dzial2` representation section for board member names/roles. No auth, no billing. Failures are non-blocking — the lead ships without board members.

## 8. Delivery

**Signal** — one plain-text group message per day via local `signal-cli-rest-api` (`POST /v2/send`):
- Block 1: verified leads — company, position(s), location, NIP, phone, email, website, score; ordered most-actionable first.
- Block 2: unverified — OLX leads without NIP (name, position, OLX phone).
- Footer: offers ingested per source, BizRaport spend vs. cap, stage failures.
- Split at Signal's size limit, numbered. Failure: 3 retries with backoff; persistent failure leaves leads `new` (roll into tomorrow) and exits non-zero.

**Pipedrive** — verified leads only, API token auth:
- One-time `lead-engine pipedrive setup` creates custom Organization fields (NIP, REGON, KRS, PKD, board members, source) and stores field keys in config.
- Per lead: search org by NIP field →
  - not found: create Organization (all enrichment fields) + Deal (`{position} — {company}`, configurable pipeline/stage) + Note (offer details, source link);
  - found, no open deal: new Deal on the existing org;
  - found, open deal: Note on that deal ("hiring again: {position}, {date}").
- `deliveries` stores org/deal IDs for idempotency. Daily volume ≪ rate limits.

## 9. Orchestration, error handling, testing

**Cron (single entry, as required):**
```
0 5 * * *  /opt/lead-engine/bin/lead-engine run --config /etc/lead-engine/config.toml
```
05:00 because CBOP only allows fetching 17:00–07:00; the digest lands before the workday. Config (TOML): scraper paths, API keys, Signal number/group, Pipedrive token + field keys, daily PLN cap, suppression window, thresholds.

**Degrade, don't die:**
- Stage status recorded in `runs`. A scraper failure (non-zero exit / missing export) skips that source with a prominent digest warning; one source never blocks the other.
- External APIs: bounded retries, exponential backoff. REGON/KRS outage → partial lead now, re-enrich next run. BizRaport error/cap → unresolved path.
- Only delivery failure blocks state transition (leads stay `new`, roll over). Upstream stages are idempotent; `lead-engine run --resume` continues from the last completed stage.
- Non-zero exit on any stage failure → cron MAILTO alerting. Structured logs to `/var/log/lead-engine/`.

**Testing:**
- Contract fixtures (shared by both scrapers' exporter tests).
- Pure-function unit tests: matcher, dedup, suppression, qualification.
- `httptest` fakes with recorded payloads: REGON, KRS, BizRaport, Pipedrive, Signal.
- Golden-file test for digest rendering.
- `lead-engine run --dry-run`: full pipeline on real exports, deliveries to stdout — the pre-deploy smoke test.

## 10. Non-negotiables — compliance check

| Requirement | How met |
|---|---|
| Enrichment after merge | §3 — validated; sole exception (gov_api REGON) justified |
| Reliable, cost-controlled NIP resolution | Verified-name BizRaport match + per-KRS cache + hard daily PLN cap + unresolved fallback |
| Single cron entry on VPS | §9 — one `lead-engine run` |
| Automated Signal + Pipedrive output | §8 |
| Reuse existing code, minimize rewrites | Scrapers untouched except additive JSON exporters; olx BizRaport client logic ported/reused in orchestrator |

## 11. Out of scope

- Rewriting `gov_api` in Go.
- Competitor-map analytics (separate function, closed PR #15 in gov_api).
- Web dashboard / multi-user access to `leads.db` (Postgres revisit trigger).
- Additional scrape sources (the contract makes adding them cheap later).
