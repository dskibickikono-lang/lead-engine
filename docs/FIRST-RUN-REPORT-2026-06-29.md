# First Live Run — Report (2026-06-29)

First production run of `lead-engine` on the VPS (`vps-7be86863`), deployed to
`/opt`, delivering live to Signal + Pipedrive. This documents what was deployed,
what the run produced, what went well, what went wrong, and the conclusions.

---

## 1. Summary

| | |
|---|---|
| Deploy target | `/opt/lead-engine` (binaries, config at `/etc/lead-engine/config.toml`) |
| Run id | `4` — **all 6 stages `ok`** |
| Wall-clock | 19:16:34 → 19:58:19 ≈ **42 min** |
| Raw offers ingested | **2,083** (CBOP 666, OLX 1,417) |
| Companies | **838 verified**, 399 unresolved |
| Leads built | 1,237 (823 delivered) |
| Delivered to **Signal** | **823** leads (digest) |
| Delivered to **Pipedrive** | **424** orgs + deals (verified & qualified) |
| Go test suite | `go test ./...` → **all pass** |
| Pipedrive NIP search / dedup | **verified** (checklist #3 passes) |

The pipeline works end-to-end. Both sources scrape, unify, resolve, enrich,
qualify and deliver. The headline problem was a **mis-set BizRaport
`max_candidates`** that made NIP resolution unusably slow and expensive until
corrected mid-session.

---

## 2. Stage timing (run 4)

| Stage | Duration | Notes |
|---|---|---|
| ingest | ~1 s | 2,083 offers upserted into `raw_offers` |
| match | <1 s | offers → companies (NIP / normalized-name) |
| **resolve-nip** | **~28 min** | 701 BizRaport name searches (the bottleneck) |
| enrich | ~4.5 min | REGON (838) + KRS board (633), free APIs |
| qualify | ~1 s | leads built, suppression + score + PKD |
| deliver | ~9 min | 1 Signal digest + 424 Pipedrive pushes |

---

## 3. Results detail

**NIP resolution (BizRaport, OLX companies):** 309 of ~708 OLX companies
resolved to a confident NIP (**~44 %**); 399 stayed unresolved. CBOP offers
already carry NIP (666/666), so those 529 companies were verified without
BizRaport. Total verified after resolve: **838**.

**Delivery split:**
- 424 **verified + qualified** → Signal **and** Pipedrive (full data: NIP, REGON,
  KRS, board, address).
- 399 **unresolved** OLX → Signal **only**, flagged unverified (never pushed to
  Pipedrive — by design).
- 414 verified companies did **not** qualify (below score / PKD-excluded) and
  were not delivered.

**Enrichment coverage (of 838 verified):**

| Field | Coverage |
|---|---|
| Address | 669 (80 %) |
| KRS number | 648 (77 %) |
| Board members | 606 (72 %) |
| Email | 281 (34 %) |
| Phone | 253 (30 %) |
| Website | 234 (28 %) |

**Score distribution (823 delivered leads):** 80+ → 9 · 65–79 → 86 · 50–64 → 27
· **<50 or null → 701**. Most delivered leads are low/unscored (OLX has no
scorer; CBOP scores skew low).

**Pipedrive verification:** org `3643` (`KRKA-POLSKA Sp. z o.o.`) confirmed via
API; `organizations/search` by NIP `5261031829` returns exactly **1** match →
the NIP custom field is searchable and de-dup will hold on subsequent runs.

---

## 4. What went well

- **Deploy strategy.** Deployed the *tested* working tree rather than a fresh
  `git clone`, avoiding a `gov_api` submodule regression (the committed pointer
  was behind the `raw-leads-export` feature actually in use).
- **pip bootstrap without `apt`.** `python3-venv` lacked `ensurepip`; solved with
  `python3 -m venv --without-pip` + `get-pip.py` — no root package install.
- **cwd-safe gov launcher.** Added `/opt/gov_api/run-gov.sh` (a `cd`-then-exec
  wrapper) so the gov scraper's relative `export_dir` / `.env` / dedup-db resolve
  deterministically. Without it the export would have landed in the wrong place
  under cron and the scrape stage would have failed its file-exists check.
- **Graceful-degradation design held.** Per-item failures counted, stage errors
  combined at the end; the run completed and reconciled cleanly.
- **End-to-end success after the fix:** all 6 stages `ok`, both channels
  delivered, all unit tests pass, Pipedrive dedup verified.

---

## 5. What went wrong

1. **`max_candidates = 5000` (should be 5) — critical.** Each BizRaport name
   search fetches a KRS profile **per candidate**, so 66 searches spawned 3,446
   profile fetches. Effects: single-company "cost" of **1,171 PLN**, ~2,432 PLN
   over the first 15 companies, resolve ETA **~7 hours**, and **~0 confident
   resolutions** (too many candidates → no single match). After lowering to
   **5**: ~10× faster (~31 companies/min), **44 % resolution**, and the full
   resolve finished in ~28 min.
2. **`daily_cap_pln = 10000`** (example default is 10). Combined with #1 this
   authorised a runaway. Raised to 1,000,000 only because BizRaport is on a
   **free 14-day trial** (see §6). This is *not* a safe steady-state value.
3. **`--dry-run` is not side-effect-free.** It only skips Signal/Pipedrive; it
   still runs the **paid** `resolve-nip` and the REGON/KRS calls. `CLAUDE.md`
   claims "zero external side effects" — the code (`internal/cli/run.go`) does
   not match. A "dry run" can spend money.
4. **Low contact-info coverage.** REGON BIR returned phone/email/website for only
   ~30 % of verified companies. Board/KRS/address are well covered (72–80 %).
5. **Score threshold doesn't gate OLX-resolved leads.** OLX-resolved verified
   companies have a `null` score yet are delivered (302 sub-50 verified leads
   shipped). High volume, uneven quality — may or may not be intended.
6. **OLX cold first sync is very slow** (~1 page/50 s; per-company www enrichment
   at 1 req/s). The full 7-day sync was cut short at 1,417 offers and exported
   from the warm cache. Subsequent nightly syncs should be faster (cache reuse).
7. **Environment quirk:** `python3-venv` shipped without `ensurepip`
   (Debian/Ubuntu split package) — worked around, noted above.

---

## 6. Conclusions & recommendations

- **Pin `max_candidates = 5` everywhere** (`/etc/lead-engine/config.toml` already
  fixed; also fix `config.example.toml` in the repo). Never run 5000.
- **BizRaport free trial ends ~2026-07-13.** Before then, set
  `daily_cap_pln` back to a real budget (e.g. 20–50 PLN). At normal pricing this
  run would have "cost" ~8,324 PLN — the cap matters.
- **Fix `--dry-run`** to skip `resolve-nip` (or update `CLAUDE.md`) so it is
  genuinely free of external side effects. *(code change — deferred)*
- **Commit the deploy artifacts** so deploys are reproducible: `run-gov.sh`,
  the gov `.env` template, and the `max_candidates`/cap fixes in
  `config.example.toml`.
- **Investigate qualification of OLX-resolved leads** (null score → delivered).
  Decide whether to score them or hold them.
- **Remaining deploy step: cron.** Not yet installed. For normal-sized future
  days the standard single nightly entry is appropriate:
  ```
  MAILTO=dskibicki.kono@gmail.com
  0 5 * * * /opt/lead-engine/bin/lead-engine run --config /etc/lead-engine/config.toml >> /var/log/lead-engine/run.log 2>&1
  ```
- **Expect lower volume going forward.** This first run was a backlog dump
  (7 days of OLX + full CBOP). Daily incremental runs will be far smaller.

---

## 7. Deployment layout (as built)

```
/opt/lead-engine/                 deployed tree + bin/lead-engine, data/leads.db
/opt/gov_api -> /opt/lead-engine/gov_api   (symlink; venv, .env, run-gov.sh, exports/)
/opt/olx-printing-press -> .../olx-printing-press  (symlink; bin/olx-pp-cli, data/)
/etc/lead-engine/config.toml      chmod 600, secrets + field_keys
/var/log/lead-engine/             run.log, scrape logs
signal-cli REST API               docker, 127.0.0.1:8080 (healthy)
```
