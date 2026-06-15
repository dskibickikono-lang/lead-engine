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

## 5. Scraper commands (no wrapper scripts)
Both scrapers expose a single command that produces the raw-leads export, so
`gov_cmd` / `olx_cmd` invoke the binary directly — `lead-engine` execs them
without a shell, so no shebang or execute bit is involved.

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
