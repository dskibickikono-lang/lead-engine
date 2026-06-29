#!/usr/bin/env bash
# Deterministic launcher for the CBOP/gov_api scraper. lead-engine's
# ScraperStage execs this directly (no shell), so it needs a shebang + +x.
# cd into the gov_api dir so the relative .env, export_dir and dedup db
# resolve the same way under cron (cwd=/home/...) as under manual runs.
set -euo pipefail
cd /opt/gov_api
exec ./venv/bin/python ./main.py "$@"
