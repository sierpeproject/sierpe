---
name: live-smoke
description: Run and interpret an end-to-end smoke of Sierpe against real testnet with a throwaway Postgres in Docker — the verification every PR that touches ingest, source/rpc, registry, extract, store or api needs before merge. Use when asked to "verify live", "smoke", "prove it works against testnet", or before merging any pipeline change.
---

# Live smoke against testnet

Unit tests with fakes are not enough for pipeline changes: both RPC races found so
far only showed up live. A smoke costs ~5 minutes and has caught a fatal-at-tip
bug twice. Do it before proposing the merge of anything under `internal/{ingest,
source,registry,extract,store,api}`.

## Setup (the user runs Docker; you prepare the commands)

1. Throwaway Postgres on a non-default port so nothing collides:
   ```bash
   docker run -d --name sierpe-smoke -p 5544:5432 -e POSTGRES_PASSWORD=smoke -e POSTGRES_DB=sierpe postgres:16
   ```
2. **Truncate / start empty.** If reusing a DB that ran the test suite, a residual
   cursor (e.g. 601) makes a fresh boot look like legitimate below-retention and the
   process exits. Fresh container = safest.
3. Env: `DATABASE_URL=postgres://postgres:smoke@localhost:5544/sierpe`,
   `NETWORK=testnet`, `ADMIN_TOKEN=<32+ random chars>` (entropy is validated at boot).
4. `go build -o /tmp/sierpe ./cmd/sierpe && /tmp/sierpe run` (or the Docker image
   when the change is in the Dockerfile).

## What to check, in order (each maps to a rule in CLAUDE.md)

- **Boot log**: config printed with secrets REDACTED (rule 12); migrations applied;
  "starting at tip N" on a fresh DB.
- **Live ingestion**: commits every ledger (~4 ms each); `/health` 200, `/ready`
  200 once at tip (503 during catch-up), `/status` cursor advancing, `/metrics`
  present.
- **Chain integrity in DB**: `select count(*) from ledger_hashes l join ledger_hashes p
  on l.previous_hash = p.hash` equals rows−1 (rule 1/6).
- **Resume**: kill, restart → "resuming from cursor" and the FIRST ledger after
  resume verifies continuity.
- **Sabotage** (only when touching ingest/store): corrupt `cursor.last_hash` by hand →
  exit 1 with "chain divergence" and ZERO new writes.
- **Registration + classification**: `POST /v1/contracts` with a known testnet
  contract (an OZ token → `wasm/spec_events` with mined event names; XLM SAC →
  `sac_builtin`; a fake id → 404).
- **Backfill**: descending chunks, watermark advancing, gap persisted BEFORE the
  retention clamp if the wall is hit; `scanStatus` reaches `OLDEST_REACHED` or
  `COMPLETE`.
- **API**: `topic0` filter narrows; page 2 via cursor has no overlap; cursor + explicit
  params → 400; `coverage` declared on every response; `/state` snapshot vs
  `/state/history` count (history ≥ live entries).
- **Soak at tip ≥ 4 minutes** when touching source/rpc: the two known races
  (`getLatestLedger` ahead of `getLedgers`; failed tip probe) must NOT exit the
  process — they log as transient and back off.

## Decoded values and the viewer (serve mode, real data, read-only)

Testnet rarely has the shapes that break a renderer; a mainnet database
does. Run `sierpe serve` (no ingestion) against a COPY of a real instance's
database — serve runs Migrate, so confirm first that
`git diff <last tag>..HEAD -- internal/store/migrations` is empty — with
`NETWORK=mainnet`, a throwaway `ADMIN_TOKEN`, a free `HTTP_PORT`, and check:

- **Default bytes unchanged**: for a sample of contracts, every XDR endpoint
  with no `xdrFormat` equals the same URL with `xdrFormat=base64`, and equals
  the live instance of the previous release after normalizing
  `latestLedger` and `coverage.indexedToLedger` (movements: strip the proven
  `asset`/`decimals` pair).
- **JSON equals the reference**: walk every row of every contract with
  `xdrFormat=json`; each `*Json` value must equal `stellar xdr decode --type
  ScVal --output json` of its base64 (movements: the topics/data of `stellar
  xdr decode --type ContractEvent` of `rawXdr`), after undoing
  encoding/json's `\u003c \u003e \u0026`. Removing the `*Json` keys must give
  the default row back, key order included.
- **Assets**: every movement labelled `asset` has a token equal to `stellar
  contract id asset --asset <it> --network-passphrase …` (computed offline);
  no unproven token is labelled.
- `sierpe_api_xdr_json_failures_total` is 0 at the end; `xdrFormat=xml` is a
  400 naming the parameter; a cursor minted with and without `xdrFormat`
  is the same string.

Viewer checklist (the embedded UI, same serve instance), with values known
from the mainnet pilot:

- CCFTFEMD…: 14 events, tw_init titled "Mainnet TAA Test"; three history
  diffs (milestone status pending→Progress, approved false→true, released
  false→true); 15 movements netting +21.71 USDC, as Stellar Expert shows;
  the movement tx TOID 254035113557024768 resolves to 3788efe1….
- CBZFTHRU… nets −25 USDC; CB72U3LZ… shows 1 XLM, native, SAC verified;
  CALDW6BM… renders a multi-path diff; CDCYN23N… renders a 25-row history
  page in under a second.
- `\xc2\xb7` displays as "·"; strings holding `'`, `<` or bidi controls
  render as text, never markup; a Hebrew string beside a number in a
  topic row or a collapsed preview leaves the number where its bytes put
  it; a variation selector or grapheme joiner shows as a `\u` escape.
- Searching "CCFT" gives one hit; the code facet reads 978/233/55/14/4/1.
- prev/next, back/forward, and a fast A→B click all land on the right
  contract; no horizontal scroll at 375 px; every row below coverage is
  marked and none is hidden.

## Teardown

`docker rm -f sierpe-smoke`; delete the scratch binary. Record in the PR body what
was verified live (the milestone PRs have the format).

## Done when

Every applicable check above is listed in the PR body with its observed value; any
new failure mode found becomes a regression test before merge.
