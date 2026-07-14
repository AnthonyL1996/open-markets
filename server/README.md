# Open Markets — league economy backend (`openmarketsd`)

The online backend for the [Open Markets](../README.md) Cities: Skylines mod. Friends in a **league**
(friend-group scope) share a server-owned economy: a per-commodity **price index** (members report net
supply/demand; the server aggregates + clamps it and layers global price events), plus peer **trade
contracts**, two-sided **trade baskets**, **bonds**/loans, **austerity** with a wall-clock due-clock,
**co-op city levers** (invest/bailout), and leaguemate **city profiles**.

- **Language:** Go, **standard library only** (no external modules → builds and tests offline).
- **Storage:** in-memory with atomic JSON persistence (the `store.Store` interface lets a Postgres
  implementation drop in for production — see `../BACKEND.md`).
- **Deploy target:** self-hosted (home server, VPS, or small cloud instance) behind Cloudflare Tunnel for
  TLS, per `../BACKEND.md`.

## Quick start

```bash
make run                       # listens on :8080, persists to ./data/openmarkets.json
# or
go run ./cmd/openmarketsd
```

```bash
make test     # unit + integration tests
make race     # tests under the race detector
make cover    # coverage
make docker   # build the tiny distroless image
docker compose up --build      # run containerised with a persistent volume
```

Configuration is via `OM_*` environment variables — see [.env.example](.env.example). The aggregation
scale and clamp bounds (`OM_VOLUME_REF`, `OM_INDEX_MIN`/`MAX`) **must match the C# client** (20000 /
0.5 / 2.0) or the two disagree on how far reports move prices.

## API

| Method | Path             | Auth        | Purpose |
|--------|------------------|-------------|---------|
| GET    | `/health`        | none        | liveness + version |
| GET    | `/eol`           | none        | end-of-life signal (`{eol,message}`) for the dead-server posture |
| POST   | `/accounts`      | none        | mint an identity → `{accountId, secret}` (secret shown **once**) |
| POST   | `/leagues`       | account     | create a friend group → `{leagueId, joinCode}` (creator auto-joins) |
| POST   | `/leagues/join`  | account     | `{joinCode}` → join a friend group |
| POST   | `/report`        | member      | `{leagueId, commodity, netSupply}` → upsert your latest standing |
| POST   | `/report/batch`  | member      | `{leagueId, reports:[{commodity, netSupply}]}` → the client's daily batch report |
| GET    | `/index?league=` | member      | **legacy single-float feed** `{version, index, ts}` (superseded by `/prices`) |
| GET    | `/prices?league=`| member      | **the current client feed** — per-commodity `{version, ts, commodities:[{commodity, index, eventPct, history}]}` |
| GET    | `/leagues/members?league=` | member | league roster + each member's standing, online/last-seen, and city profile |
| POST   | `/cityprofile`   | account     | report your own city snapshot (population/happiness/industry/finances) for leaguemates |
| POST   | `/contracts`     | member      | offer a bilateral contract — `{leagueId, type, commodity, qty, unitPrice, installments, counterparty, side}` |
| GET    | `/contracts?league=` | member  | the caller's contracts in a league (newest first) |
| POST   | `/contracts/{id}/accept` `/decline` `/cancel` | party | consent transitions (accept/decline by the counterparty; cancel by the offerer) |
| POST   | `/contracts/{id}/settle` | party | book the caller's next installment |
| POST   | `/trades`        | member      | offer a two-sided **basket** trade — `{leagueId, counterparty, defaultRateBps, installments, items:[…]}` |
| GET    | `/trades?league=`| member      | the caller's trades in a league |
| POST   | `/trades/{id}/accept` `/decline` `/cancel` `/settle` `/shortfall` | party | trade transitions / settle a leg / report an undelivered give |
| GET    | `/bonds?league=` | member      | the caller's bonds (debts from defaulted installments + negotiated loans) |
| POST   | `/bonds/{id}/settle` | party   | repay a bond installment |
| POST   | `/loans` · `/loans/{id}/counter` `/accept` `/decline` `/cancel` | member/party | negotiate a peer loan |
| GET    | `/settlements?league=&since=` | member | the league's server-authored settlement-event feed (cash booking) |
| GET    | `/citystate?league=` | member  | the caller's austerity status, active co-op effects, and the wall-clock due interval |
| POST   | `/investment-office?league=` | member | co-op lever: grant a leaguemate a §-scaled, targeted demand + attractiveness buff (§ transfers) |
| POST   | `/bailout?league=` | member   | co-op lever: pay down a leaguemate's defaulted bonds (§ transfers to the creditor) |
| GET    | `/audit?league=` | member      | per-account net cash + the conservation total (must be 0 — a live invariant check) |

### Contracts (cash-settled bilateral agreements)

A contract is a fixed deal between two league members — a seller (delivers, gets paid) and a buyer (pays)
— over **N installments** (1 = a one-shot future; N>1 = a recurring supply deal). **CS1 cannot force
physical cargo**, so a contract is an *accounting overlay*: each party settles **its own leg in in-game
cash** when ready (the game client on a day rollover; the operator CLI on command). The server is the
ledger + consent flow only — it never moves money, and an unsettled/defaulted contract must never block a
save load (risk is reputational, by design). Lifecycle: `offered → active → completed`
(or `declined`/`cancelled`).

**Auth:** `Authorization: Bearer <accountId>.<secret>`, or `?account=&secret=` query params on GET
(so a bare `UnityWebRequest.Get` can authenticate without custom headers).

**Index model:** the **effective** index per commodity = `clamp(1 - sum(netSupply)/VolumeRef, Min, Max)`
(the per-league elasticity from members' reports) **× a global price-event multiplier** (a server-side
random spike/slump shared across all leagues), re-clamped to `[Min, Max]`. Net export (positive supply)
lowers the price; net import (negative) raises it. `/prices` returns the per-commodity effective index
(+ the active event % and a rolling history for the sparkline); `/index` returns the mean across commodities
(legacy single-float feed). The in-game client consumes `/prices`.

## Operator console (web UI)

A self-contained operator page is served at **`/console`** (embedded in the binary). Open
`http://localhost:8080/console` in a browser to simulate one or more counterparty cities **visually** —
the graphical twin of `omctl`:

- **Create city** (mints an account; credentials stored per-city in the browser), switch the active city
  from the dropdown to act as several players.
- Create / join a **league** (shows the join code to share with the in-game player).
- **Offer / accept / decline / cancel / settle** contracts **and two-sided trades**; **bonds**/loans; the
  co-op **invest**/**bailout** levers; post **reports**; and watch the **league price index**, the
  **members** roster (with each city's profile + online status), and your **city state** — all auto-refresh
  every few seconds.

Disable in production with `OM_CONSOLE=0` (the page is a thin client — every action still authenticates per
city — but you may not want the UI exposed). It needs no auth itself; the API calls it makes carry the
stored per-city bearer token.

## `omctl` — operator CLI (simulate a city by hand)

Instead of AI bots, you can role-play a counterparty manually. `omctl` is a minimal CLI that stores
credentials in a profile JSON, so you can stand up a "city" and seed a league/contract from the terminal:

```bash
go build -o /tmp/omctl ./cmd/omctl
S=http://localhost:8080
/tmp/omctl -server $S -profile cityB.json account                 # mint an identity
/tmp/omctl -server $S -profile cityB.json league-join -code K7Q2-9F3M
/tmp/omctl -server $S -profile cityB.json offer -to <playerAccountId> \
    -side sell -commodity Oil -qty 2000 -price 140 -n 4 -type supply
/tmp/omctl -server $S -profile cityB.json list
```

Commands: `account`, `league-create`, `league-join`, `report`, `offer`, `list`. For the full operator
surface (accept/settle, trades, bonds/loans, and the co-op levers) use the **web console** (above) — it's
the richer twin of this CLI.

## Layout

```
cmd/openmarketsd      entrypoint (config, graceful shutdown, periodic flush, the wall-clock due-clock)
cmd/omctl             minimal operator CLI (seed a city / league / contract by hand)
cmd/omsim             economy simulation harness (conservation / due-clock sanity runs)
internal/config       OM_* env loading
internal/market       index aggregation + global price events (pure, well-covered)
internal/pricing      base-price table + accept-time valuation (mirrors the C# client)
internal/money        cents/qty scaling + amortization + byte-exact value goldens
internal/duecycle     the scheduled wall-clock due-clock (misses, garnishment, event/sparkline stepping)
internal/sim          in-process economy simulation used by omsim/tests
internal/id           identity/secret/code generation + hashing
internal/store        Store interface + in-memory/JSON impl (accounts, leagues, reports, contracts, trades,
                      bonds/loans, settlement events, co-op effects, city profiles)
internal/api          routing, middleware (recover/rate-limit/log), auth, handlers, embedded /console
```
