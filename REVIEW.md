# Lead-Engine Architecture & Code Review

### 1. Architecture Assessment
**Rating: 8/10**

The overall pipeline design is pragmatic and sound for a B2B lead generation system serving a staffing agency. The monorepo + submodule approach is a good choice to decouple scrapers from the central orchestrator while keeping everything easily deployable. Go is an excellent choice for the orchestrator due to its strict typing, performance, and strong concurrency for data processing, while Python is well-suited for scraping (praca.gov.pl) thanks to its mature ecosystem (Pydantic, Requests).

The data flow—scrape → ingest → enrich → qualify → deliver—is logically separated. Each step is modeled as a discrete stage in a DAG-like runner, which allows for robust retries and graceful degradation on failure.

However, there are a few architectural rough edges:
- **Database Concurrency:** The reliance on a single SQLite DB with `_pragma=busy_timeout(5000)` could potentially become a bottleneck if scrapers or enrichment scale up significantly and introduce concurrent writes, though it is currently sufficient.
- **Contract Drift:** The `v1` JSON contract between scrapers and the orchestrator relies on file exports. If a scraper's output schema changes or drifts, the orchestrator might fail during ingestion without early warning.

### 2. What Was Done Well
- **Resilient Runner:** The `internal/run/runner.go` is well-designed. It records stage outcomes in SQLite (`run_stages`) and supports resuming from the last failed stage. Failures are aggregated and do not stop the pipeline immediately. This is a critical operational feature for a daily cron job.
- **Cost Controls:** The BizRaport API integration (`internal/enrich/resolve.go`) includes a daily spend cap (`DailyCapPLN`). This is an excellent, production-ready pattern that prevents unbounded billing.
- **Idempotency & Deduplication:** The ingestion and matching logic (`internal/ingest` and `internal/match`) uses SQLite upserts and NIP/normalized name deduplication. This safely handles duplicate scraper runs and overlapping data.
- **Caching Strategy:** The API response caching (`internal/store/cache.go`) uses a generic `api_cache` table, which is an effective way to minimize external API calls (REGON, KRS, BizRaport) and save costs/avoid rate limits.
- **Clear Schema Definitions:** The raw-leads contract (`internal/contract/contract.go`) is strict and type-safe, validating required fields like `ExternalID` and `CompanyName`. The `Extra map[string]any` field is a great forward-compatibility convention.

### 3. What You Would Do Differently

#### a) Submodule Communication
- **Current Approach:** Scrapers write `.json` files to disk, which the orchestrator reads.
- **Problem:** File-based contracts are brittle, especially with submodules that can become out of sync. It adds I/O overhead and requires careful path configuration (`config.toml`).
- **Alternative:** Expose scrapers as internal HTTP services or CLI tools that output JSON to stdout. The orchestrator can execute them and consume the stdout stream directly (`json.NewDecoder`), avoiding intermediate files and reducing state. **Trade-off:** A file-based contract provides an auditable artifact on the VPS, allowing inspection of scraper output upon failure without parsing logs. A stdout pipe loses this direct artifact.

#### b) Python Scraper Output Contract
- **Current Approach:** `gov_api` scraper builds a dictionary manually (`_format_lead` in `exporter.py`) and writes to JSON.
- **Problem:** Python dicts are not type-safe. Pydantic models are used for scraping (`JobOffer`) but bypassed for the final export, risking schema drift.
- **Alternative:** Define a Pydantic model for the `v1` contract in `gov_api` and use `model_dump_json()`. This enforces the contract programmatically before export.

#### c) SQLite Concurrency Configuration
- **Current Approach:** `db.SetMaxOpenConns(1)` is used to avoid `SQLITE_BUSY`.
- **Problem:** While safe, it restricts the orchestrator to purely sequential operations, which limits future parallelization of API enrichment.
- **Alternative:** Once parallel enrichment is implemented (see roadmap), use SQLite in WAL mode with a higher `MaxOpenConns` (e.g., 4-8) to support concurrent writes, and handle `database is locked` retries at the application layer.

### 4. Conflict & Scalability Risk Analysis
- **Data Conflicts:** Matching relies on normalized names (`match.Normalize`) when NIP is missing (e.g., from OLX). This is a known trade-off but risks merging distinct companies with similar names (e.g., "Kowalski Sp. z o.o." vs. "Kowalski Jan").
- **Submodule Versioning:** Git submodules are prone to SHA drift. If `lead-engine` is deployed but `make setup` isn't run, it might use an old scraper version that produces an invalid JSON contract.
- **Scalability Bottlenecks:** Enrichment is sequential (`internal/enrich/enrich.go`). If the daily lead volume jumps 10x, processing sequentially could exceed the cron window. Parallelizing enrichment (with rate limits) will be necessary.
- **Operational Risks:** `cron` silently fails if the orchestrator binary crashes (e.g., OOM) before writing to `run.log`. Using a systemd service with restart policies and proper logging would be more robust.
- **Dependency Risks:** External APIs (REGON, KRS, BizRaport) are single points of failure. The current design gracefully degrades by shipping partial leads, which is good, but extended outages will degrade lead quality significantly.

### 5. Professional Code Review

#### Raw-Leads Export Contract (v1)
- **Correctness:** Good. `contract.Parse` enforces required fields.
- **Safety:** Using `*float64` and `*int` for nullable fields is correct in Go.
- **Forward Compatibility:** The `Extra map[string]any` field allows passing arbitrary data without breaking parsing. This is a very smart forward-compatibility choice.

#### Go Orchestrator
- **Ingestion:** `ingest.Ingest` is clean, but it marshals the entire `Offer` into a JSON string (`Payload`) in the DB. This duplicates data already stored in columns. If `Payload` serves as a "raw backup", it should be a documented design decision.
- **Error Handling:** `mustExec` in tests is fine, but in production code, errors are generally wrapped well (`fmt.Errorf("...: %w", err)`).

#### Python Scraper
- **Missing Error Handling:** In `exporter.py`, `_to_float` silently catches `ValueError` and `TypeError` and returns `None`. This might mask upstream parsing bugs.
- **Hardcoded Values:** `settings.chronic_vacancy_threshold` is referenced in `export_chronic_leads`, but the SQL query string formatting might be vulnerable if settings aren't strictly typed.

#### General Risks
- **Missing Tests:** While comprehensive unit tests exist (e.g., `enrich_test.go`, `runner_test.go`, `ingest_test.go`), end-to-end integration tests are missing. There should be an E2E test that runs the orchestrator with mocked scrapers and mock APIs.
- **Goroutine Leaks:** No obvious leaks, but `http.Client` in `krs` doesn't enforce response body closure strictly in all error paths (though `defer resp.Body.Close()` is used correctly when no error occurs).

### 6. Development Roadmap Recommendations

1. **Pydantic Export Contract Validation (S Effort, High Impact)**
   - **What:** Implement Pydantic models in `gov_api` and `olx-printing-press` for the output contract.
   - **Why:** Prevents runtime ingestion failures in the orchestrator by guaranteeing schema compliance at the source. This blocks an entire class of runtime errors with low effort.
   - **Implementation:** Replace the manual dictionary construction in `exporter.py` and Go JSON structs with formal models.

2. **Parallelize Enrichment (M Effort, High Impact)**
   - **What:** Update `internal/enrich/enrich.go` to use worker pools (e.g., `golang.org/x/sync/errgroup`) for API lookups.
   - **Why:** Reduces pipeline execution time, preventing overlap with business hours as lead volume scales.
   - **Implementation:** Spin up 5-10 workers reading from a channel of companies needing enrichment, ensuring rate limiters are respected.

3. **Systemd Daemonization (S Effort, Medium Impact)**
   - **What:** Replace `cron` with a `systemd` service and timer.
   - **Why:** Improves operational visibility, log management (via `journalctl`), and automatic restarts on failure. A quick win for operational resilience on a VPS.
   - **Implementation:** Provide a `.service` and `.timer` unit file in the `docs/` folder with deployment instructions.

4. **Improve Name Matching Heuristics (L Effort, Medium Impact)**
   - **What:** Introduce fuzzy matching (e.g., Levenshtein distance, Trigram) or ML-based entity resolution instead of strict exact-normalized-string matching.
   - **Why:** Reduces false negatives (missed deduplication) and false positives (incorrect merges) for NIP-less OLX leads.
   - **Implementation:** Integrate a lightweight fuzzy string matching library in Go and set a confidence threshold.

5. **Scoring for OLX Leads (M Effort, Medium Impact)**
   - **What:** Implement a scoring mechanism for OLX leads, similar to what exists for `gov_api`.
   - **Why:** Allows sales teams to prioritize high-value leads rather than treating all OLX leads equally.
   - **Implementation:** Extract text features (keywords, salary presence, company size) in Go and apply a simple weighted heuristic.
