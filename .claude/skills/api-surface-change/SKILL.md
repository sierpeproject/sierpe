---
name: api-surface-change
description: Changing what Sierpe exposes — a v1 endpoint, a query parameter, cursor contents, response shape, scanStatus/coverage semantics, an admin route, or a metric — without breaking getEvents-v2 compatibility or the docs contract. Use when touching internal/api, internal/admin, internal/health metrics, docs/openapi.yaml or docs/METRICS.md.
---

# API surface change

The public surface is a promise to people who self-host this: once the first tag
exists it cannot move casually. Even before the tag, treat it that way.

## Non-negotiables

- **getEvents v2 compatibility is a v1 requirement** (D7): filter names
  (`topic0..3`, `startLedger`, `limit`, `cursor`), the id format
  `{toid}-{event_index}` and the `scanStatus` vocabulary
  (`HAS_MORE | WAITING_FOR_LEDGERS | OLDEST_REACHED | COMPLETE`) follow the
  upstream discussion (stellar/go#1872 area). Diverge only additively.
- **Cursors are opaque and self-describing**: they embed the whole query and their
  `kind`. Cursor + explicit params → 400. A cursor must never be valid on a
  different endpoint. Changing the codec invalidates every cursor in the wild — do it
  only with a version byte and a stated policy.
- **Coverage and scanStatus are honest or absent.** Never report `COMPLETE` for a
  range with an open gap; never imply coverage a query did not check (rule 7).
- **Business time is `closed_at`** in filters and sorts; `ingested_at` never leaks
  into query semantics (rule 8).
- Admin routes: bearer auth, idempotent, pull-not-push (rule 11).
- **Presentation parameters never enter cursors.** `xdrFormat` changes how a
  page is written, not which rows it holds: parse it outside the cursor
  branch, keep it out of the banned-with-cursor lists and out of the codec,
  so `cursor=…&xdrFormat=json` works and the cursor minted is the same in
  both formats (RPC semantics: the client repeats it on every page).
- **JSON views are additive.** Every base64 ScVal field `f` may get an `fJson`
  sibling under `xdrFormat=json`; the base64 stays. The default response
  must not change by a byte — `omitempty` on every `*Json` field, and the
  guard test that a default body contains no `Json"`. Absent means no value;
  `null` means a value that could not be rendered (counted in
  `sierpe_api_xdr_json_failures_total`).
- **Never serve an unchecked `json.RawMessage`.** `writeJSON` sends the 200
  before encoding, so one invalid raw value truncates the response to an
  empty body with status 200. Route every rendering through the
  `json.Valid` guard in `internal/api/xdrformat.go`.
- **Only the ScVal JSON shape is promised.** It is pinned byte for byte by
  `internal/scvaljson/testdata/golden.tsv`, whose expected column comes
  from the reference (`stellar xdr decode`, RPC's own xdrFormat=json) —
  never from the encoder. Never render an envelope (ContractEvent,
  LedgerKey): stellar-xdr's serde for those has already changed between
  versions.

## Procedure

1. Classify: additive (new optional param/field/endpoint) vs breaking (rename,
   removed field, changed semantics, cursor codec). Breaking before the first tag is
   allowed but must be called out in CHANGELOG; after the tag it needs a major.
2. Implement behind the existing handler patterns in `internal/api` (codec in one
   place; validation returns 400 with an actionable message).
3. **Same PR, mandatory:**
   - `docs/openapi.yaml` — path, params, schema, examples.
   - `docs/METRICS.md` — any new/renamed metric, and whether it should alert.
   - `CHANGELOG.md` `[Unreleased]`.
   - Tests: handler tests for the shape; cursor round-trip test if the codec moved.
   - A new ScVal-bearing field: its `*Json` sibling, the OpenAPI entry
     referencing `ScValJson`, and a json-mode handler test.
4. Update the Grafana dashboard (`deploy/grafana/`) or Gatus config if a metric or
   endpoint they use changed.
5. Verify live (skill `live-smoke`) for anything that touches paging, cursors or
   coverage — the drift bugs are only visible with real data.

## Done when

OpenAPI, METRICS.md and CHANGELOG updated in the same PR; tests cover the new
shape; compat with getEvents v2 stated explicitly in the PR body.
