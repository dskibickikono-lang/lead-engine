# lead-engine

B2B lead generation pipeline for a blue-collar staffing agency.
Scrapes job offers from two sources, enriches company data, and delivers daily leads to Signal and Pipedrive.

## Components

| Submodule | Role |
|-----------|------|
| [gov_api](https://github.com/dskibickikono-lang/gov_api) | Scraper for government job API (praca.gov.pl / CBOS) — Python |
| [olx-printing-press](https://github.com/dskibickikono-lang/olx-printing-press) | Scraper for OLX blue-collar job listings — Go |

The `lead-engine` Go binary is the orchestrator: it ingests raw-leads exports from both scrapers, deduplicates, enriches (REGON, KRS, BizRaport), and delivers to Signal + Pipedrive.

## Clone

```bash
git clone --recurse-submodules https://github.com/dskibickikono-lang/lead-engine.git
cd lead-engine
```

After a plain `git pull`, reinitialize submodules if needed:
```bash
make setup
```

## Run

```bash
lead-engine run --config config.toml
lead-engine run --config config.toml --dry-run   # no external writes
```

See [`docs/DEPLOY.md`](docs/DEPLOY.md) for VPS deployment.
