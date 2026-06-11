# Deploy — lead-engine on the VPS

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

## 3. Build & install
```
GOOS=linux GOARCH=amd64 go build -o bin/lead-engine ./cmd/lead-engine
scp bin/lead-engine user@vps:/opt/lead-engine/bin/
```

## 4. Scraper wrapper scripts
`/opt/olx-printing-press/bin/sync-and-export.sh`:
```sh
#!/bin/sh
set -e
/opt/olx-printing-press/bin/olx-pp-cli sync
/opt/olx-printing-press/bin/olx-pp-cli export --kind raw-leads --format json \
  --out /opt/olx-printing-press/data/exports/raw-leads-olx-latest.json
```

Make it executable (lead-engine execs it directly, without a shell):
```
chmod +x /opt/olx-printing-press/bin/sync-and-export.sh
```

## 5. Cron (the single entry point)
```
0 5 * * * /opt/lead-engine/bin/lead-engine run --config /etc/lead-engine/config.toml >> /var/log/lead-engine/run.log 2>&1
```
05:00: CBOP fetch window is 17:00–07:00 and the digest must precede the workday.
Set MAILTO in the crontab for failure alerts (non-zero exit on any stage failure).

## 6. Smoke test
```
lead-engine run --config config.toml --skip-scrape --dry-run
```
