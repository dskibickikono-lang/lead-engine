# Live-Integration Checklist — lead-engine

Pre-production verifications that depend on **live external systems** and therefore could not be
covered by unit tests. Run each once before the first production cutover. Each item lists: what to
verify, the exact command(s), the expected result, and the code to change if the check fails.

Status legend: ☐ not yet run · ☑ verified

---

## ☐ 1. KRS — replace the synthetic `OdpisAktualny` fixture with a real one

**Why:** the management board is the only field sourced from KRS, and the test fixture
(`internal/enrich/krs/testdata/odpis.json`) is hand-written with two simple members. Real extracts
carry multi-part surnames and edge cases the synthetic fixture doesn't exercise.

**Auth:** none — the MS registry API is public.

**Run:**
```sh
# Pick a real joint-stock/limited company KRS number, e.g. 0000006666
curl 'https://api-krs.ms.gov.pl/api/krs/OdpisAktualny/0000006666?rejestr=P&format=json' \
  -o internal/enrich/krs/testdata/odpis.json
go test ./internal/enrich/krs/...
```

**Expected:** the file is a real `odpis` document; `go test` still passes (adjust the test's expected
board to match the real members).

**Watch — multi-part surnames:** the parser
(`internal/enrich/krs/client.go:40-58`, `:84-90`) only reads `nazwisko.nazwiskoICzlon`. Real entries
with a hyphenated/two-part surname also populate **`nazwisko.drugiCzlonNazwiska`**, which is currently
**dropped** — such a board member's surname would be truncated. If the recorded response contains
`drugiCzlonNazwiska`, add it to the `Nazwisko` struct and concatenate it in `FetchBoard` before
re-running the test.

---

## ☐ 2. REGON — verify `praw_*` field casing against a live BIR1.1 call

**Why:** the REGON enrichment maps a set of exact `praw_*` keys from the GUS BIR1.1 legal-person
report. GUS occasionally differs in casing between docs and live payloads; a mismatch silently yields
empty phone/email/website/KRS/address.

**Auth:** a GUS BIR API key (set in config; sandbox key works).

**Keys consumed** — `internal/enrich/regon/client.go:197-206`:
`praw_numerTelefonu`, `praw_adresEmail`, `praw_adresStronyinternetowej`,
`praw_numerWRejestrzeEwidencji`, `praw_adSiedzUlica_Nazwa`, `praw_adSiedzNumerNieruchomosci`,
`praw_adSiedzKodPocztowy`, `praw_adSiedzMiejscowosc_Nazwa`.

**Run:** make one live `BIR11OsPrawna` report call for a known REGON (via the client or the GUS test
console), dump the returned keys, and diff their casing against the literals above.

**If any differ:** update the map keys in `client.go` and the expectations in
`internal/enrich/regon/client_test.go`, then `go test ./internal/enrich/regon/...`.

---

## ☐ 3. Pipedrive — verify the NIP custom field is searchable

**Why:** organization de-duplication depends on finding an existing org by NIP.
`FindOrgByNIP` (`internal/deliver/pipedrive.go:89-107`) calls
`/v1/organizations/search` with `fields=custom_fields&exact_match=true`. If the NIP varchar custom
field is not indexed/searchable, the search silently returns nothing and **every run creates duplicate
organizations**.

**Auth:** a Pipedrive sandbox API token (config `[pipedrive]`).

**Run:**
```sh
# 1. Create the custom fields (prints the [pipedrive.field_keys] block) and put them in config:
lead-engine pipedrive setup --config config.toml

# 2. Create a test org carrying a NIP via the API or UI, then search for that NIP:
curl -s "$PD_BASE/v1/organizations/search?term=<NIP>&fields=custom_fields&exact_match=true&api_token=$PD_TOKEN" \
  | jq '.data.items[].item.id'
```

**Expected:** the search returns the test org's id (non-empty).

**If empty:** the NIP field isn't searchable — in Pipedrive, ensure the field is a searchable text
field (recreate it if needed) and re-run. `EnsureOrgFields`
(`internal/deliver/pipedrive.go:216+`) creates the field; adjust its definition there if the field
type needs to change.

---

After all three are ☑, the pipeline is cleared for the first live production run
(see `DEPLOY.md` for the cron entry point and smoke test).
