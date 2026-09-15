# Librarian

Agent-friendly CLI for the **Bayerische Staatsbibliothek OPAC+** catalog
(`https://opacplus.bsb-muenchen.de`), written in Go.

Every command supports `--help` — agents should call `<command> --help`
before first use. Global flags `--view/--lang/--json` work everywhere;
`--json` emits machine-readable output.

## Install

```bash
go build -o librarian .
```

## Workflow

```bash
librarian auth --help        # 1. log in (or stay anonymous for search)
librarian research --help    # 2. search for books, list media
librarian inspect --help     # 3. formats + availability of one record
librarian borrow --help      # 4. download digital media / request physical
```

## Commands

| Command | Auth | What it does |
|---|---|---|
| `auth login` | — | PDS login (Ausweisnummer + password) via `--user/--password`, `BSB_USER/BSB_PASSWORD` env or `--env-file .env`; saves JWT session (0600) |
| `auth status` | optional | verify session, show loans/holds/bookings counts |
| `auth logout` | — | delete saved session |
| `research <query>` | anonymous OK | catalog search (title, MMS-ID, types); `--field title\|creator\|…`, `--delivery` for availability, `--include/--exclude rtype:books` facet filters, `--facets` for aggregation buckets + highlights |
| `inspect --mms <id>` | anonymous OK, richer logged in | holdings, online links, titleServices request options, `request_path` (ovp/ngrs/electronic) + `physical_service_id`; `--detail` uses the physicalServiceId level |
| `borrow --mms <id>` | anonymous OK for public scans, login for requests | free MDZ/IIIF downloads to `--out-dir`; physical/fernleihe chain preview by default (readonly); `--offer physical\|digital\|eBook` = readonly NGRS best-offer; real request only with `--yes` (login required); `--pickup/--note`; `--cancel <id> --yes` cancels |

## Tech notes

- Backend: Ex Libris Primo VE (`/primaws/rest/pub|priv/…`), view `49BVB_BSB:VU1`, scope `MyInst_and_CI`.
- Login flow: `suprimaExtLogin` → POST `pdsHandleLogin` → `postLogin` → `loginId` → `loginJwtCache` → JWT (`Authorization: Bearer`).
- Anonymous search needs no token (guest JWT endpoint exists as fallback).
- Facet filters: `qInclude=facet_<name>,exact,<value>` joined with `|,|` (from bundle.js `facetToString` + HAR).
- Title detail: `titleServices/<mms>/svcId/<serviceId>?record-institution=…` returns per-holding `location-fields` (loc.summary/notes), `possibleFilters` and `rapidoData`.
- Digital downloads: MDZ resolver → IIIF presentation manifest → IIIF image API (`api.digitale-sammlungen.de`).
- Physical loan (OVP, from `bundle.js`): `pub/pnxs/L/alma<mms>` → `pub/getPhysicalService/<ilsId>` → `priv/titleServices/<mms>/svcId/<svcId>` → POST `priv/ILSServices/holdings/<svcId>` (items + `itemServices/…/AlmaItemRequest` links) → GET itemServices form (`services-arr`) → POST filled form (creates hold). Verify via `priv/myaccount/requests`, cancel via `priv/myaccount/cancel_requests`.
- Resource sharing (NGRS/Rapido, from `bundle.js`): GET `pub/ngrs/bestoffer/physical` (params per `getParamsForPhysicalBestOffer`) → POST `pub/ngrs/bestoffer/borrowingrequest` (payload per `createBorrowingRequest`); helpers `bestoffer/pickupLocation|email|copyRightsStatement`.
- Safety: every state-changing POST (`SubmitRequest`, `SubmitBorrowingRequest`, `CancelRequests`) is gated behind login + explicit `--yes`; without `--yes` the CLI only runs readonly steps.
