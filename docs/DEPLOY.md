# Deploy — lead-engine on the VPS

> Before the first production run, complete [LIVE-INTEGRATION-CHECKLIST.md](LIVE-INTEGRATION-CHECKLIST.md)
> — the live-system verifications (KRS fixture, REGON field casing, Pipedrive NIP searchability).

## 1. Signal infrastructure (one-time)
```
docker run -d --name signal-api --restart=always \
  -p 127.0.0.1:8080:8080 \
  -v /opt/signal-cli-config:/home/.local/share/signal-cli \
  -e MODE=normal bbernhard/signal-cli-rest-api:latest
```

Register the bot number (QR link flow):
```
curl -X GET 'http://127.0.0.1:8080/v1/qrcodelink?device_name=lead-engine'
```
Find the team group id:
```
curl 'http://127.0.0.1:8080/v1/groups/+48<botnumber>'
```

## 2. Pipedrive custom fields (one-time)
```
lead-engine pipedrive setup --config /etc/lead-engine/config.toml
```
Paste the printed [pipedrive.field_keys] block into the config.

## 3. Clone & setup (development / CI)
```
git clone --recurse-submodules https://github.com/dskibickikono-lang/lead-engine.git
# or after a plain git pull:
make setup   # runs: git submodule update --init --recursive
```

## 4. Build & install
```
GOOS=linux GOARCH=amd64 go build -o bin/lead-engine ./cmd/lead-engine
scp bin/lead-engine user@vps:/opt/lead-engine/bin/
```

## 5. Scraper commands

**gov_cmd must go through a wrapper.** `gov_api`'s `export_dir`, `.env` and dedup
db are all **relative to cwd**, and `ScraperStage` sets no working directory — so
under cron (cwd `/home/...`) the export lands in the wrong place and the
file-exists check fails the scrape stage. Point `gov_cmd` at `deploy/run-gov.sh`
(installed at `/opt/gov_api/run-gov.sh`, `chmod +x`), which `cd`s into the gov
dir before exec'ing python:
```
gov_cmd = ["/opt/gov_api/run-gov.sh", "--voivodeships", "14,30,24"]
```
The gov `.env` (chmod 600) holds `REGON_API_KEY`, `EXPORT_DIR=/opt/gov_api/exports`,
`DB_PATH=/opt/gov_api/gov_leads.db`. `olx-pp-cli` resolves its paths from the
binary location, so it is cwd-safe and needs no wrapper:

OLX combines sync + export in one subcommand (`config.example.toml`):
```
olx_cmd = ["/opt/olx-printing-press/bin/olx-pp-cli", "sync-and-export",
           "--out", "/opt/olx-printing-press/data/exports/raw-leads-olx-latest.json"]
```
```
/opt/olx-printing-press/bin/olx-pp-cli sync-and-export \
  --out /opt/olx-printing-press/data/exports/raw-leads-olx-latest.json   # verify manually
```

## 6. Cron (the single entry point)
```
0 5 * * * /opt/lead-engine/bin/lead-engine run --config /etc/lead-engine/config.toml >> /var/log/lead-engine/run.log 2>&1
```
05:00: CBOP fetch window is 17:00–07:00 and the digest must precede the workday.
Set MAILTO in the crontab for failure alerts (non-zero exit on any stage failure).

## 7. Smoke test
```
lead-engine run --config config.toml --skip-scrape --dry-run
```
Note: `--dry-run` skips Signal/Pipedrive **but still runs the paid `resolve-nip`
stage** — it is not free of external side effects.

## 8. BizRaport cost safety (read before first run)
`[bizraport].max_candidates` multiplies cost **and** time: each name-search
candidate triggers a KRS profile fetch. Keep it at **5** (the example default).
A value like 5000 makes single-company lookups cost ~1000 PLN, resolve takes
hours, and resolution quality collapses (no confident match among thousands).
Keep `daily_cap_pln` at a real budget (e.g. 10–50). See
`FIRST-RUN-REPORT-2026-06-29.md` for the incident this note came from.
