# Running the Server

The Open Markets backend (`openmarketsd`) is a small Go HTTP service that acts as the
**referee + ledger + bulletin board** for the online layer of the Cities: Skylines mod: it
aggregates friend-group net supply/demand into a shared, clamped price index, and brokers
trades, bonds, loans, and co-op levers between players. Clients run the full game locally and
poll it over HTTP(S); the server never simulates a city.

Everything below runs with **no external dependencies** — the service is a single static Go
binary with an embedded operator console and a JSON-file store. No database is required for
local/hobby use.

> **Related docs:** [`server/QUICKSTART.md`](../server/QUICKSTART.md) (2-minute walkthrough +
> operator-console contract demo), [`server/README.md`](../server/README.md) (API surface),
> and [`BACKEND.md`](../BACKEND.md) (production hosting design). This guide is the operational
> reference; the quickstart is the guided tour.

---

## 1. Prerequisites

- **Go 1.22+** — the module (`server/go.mod`) declares `go 1.22`. The code uses the
  method-and-path `net/http.ServeMux` routing (`GET /health`, `POST /contracts/{id}/accept`)
  introduced in Go 1.22, so an older toolchain will not build.
- **A free TCP port** — defaults to **`:8080`** (override with `OM_ADDR`). The operator
  console, health check, and all client traffic share this one port.
- **`make`** (optional) — convenience targets only. On Windows where `make` is unavailable, use
  the raw `go run` commands or `run-local.ps1` (see below).
- **No game DLLs are needed** to build or test the server — it is pure Go.

---

## 2. Quick start (local dev)

From the `server/` directory:

```bash
# simplest: run with all defaults (listens on :8080, persists to data/openmarkets.json)
go run ./cmd/openmarketsd
#   …or, equivalently:
make run
```

On startup it logs, for example:

```
listening on :8080 (version=phase-a, data=data/openmarkets.json)
```

Leave it running. State persists to `server/data/openmarkets.json` (delete that file to start
clean — see [§8](#8-persistence--data)).

### Self-host with Docker (recommended for a real server)

The whole thing runs from one command — no Go toolchain, no database:

```bash
cd server
docker compose up --build      # builds the tiny distroless image, listens on :8080
curl localhost:8080/health     # {"status":"ok",...}
```

State is kept in the `om_data` named volume (see `server/docker-compose.yml`), so it survives
restarts. To let a friend group connect, expose it over HTTPS (see [§7](#7-production-deployment))
and have everyone set **Options → Open Markets → "Server base URL"** to your address.

### Local in-game testing: use `run-local.ps1` / `make run-local`

When you want to point the **actual game client** at the server on the same machine, do **not**
use the bare `make run`. Use the local launcher instead:

```powershell
# Windows (PowerShell), from server/
./run-local.ps1
```

```bash
# macOS/Linux
make run-local
```

Both set exactly two environment variables and then `go run ./cmd/openmarketsd`:

```
OM_CONSOLE=1        # serve the /console operator UI (also the default)
OM_RATE_PER_MIN=0   # DISABLE rate limiting
```

**Why `OM_RATE_PER_MIN=0` matters (the localhost gotcha):** the rate limiter is keyed
**per client IP**. When both the game client *and* your `/console` browser tab hit the server
from `localhost`, they share one per-IP bucket. The default of **120 requests/min** is then
split between them, so the game gets throttled with **HTTP 429** and the Members tab / league
name silently fail to load. Setting `OM_RATE_PER_MIN=0` removes the limit for local testing.
**Production keeps the 120 default.** (See project memory `om-local-test-rate-limit`.)

### Open the operator console

With the console enabled (default, or explicitly `OM_CONSOLE=1`), browse to:

```
http://localhost:8080/console
```

This is a self-contained operator UI (vanilla JS, embedded in the binary, no external assets).
See [§5](#5-operator-console).

### Health check

```bash
curl http://localhost:8080/health
# {"status":"ok","version":"phase-a","time":"2026-06-19T...Z"}
```

`GET /health` is unauthenticated and always available. There is also `GET /eol`
(end-of-life signal; currently `{"eol":false,"message":""}`) that a retired server can flip to
tell clients to go local-only forever.

### Build a binary

```bash
make build          # → bin/openmarketsd  (CGO off, trimmed, stripped)
./bin/openmarketsd
```

---

## 3. Configuration reference

All configuration is via `OM_*` **environment variables**; every one has a default, so the
service runs out of the box with no env set. Values are read once at startup
(`server/internal/config/config.go`). A copyable template lives in `server/.env.example`.

| Env var | Default | Meaning |
|---|---|---|
| `OM_ADDR` | `:8080` | HTTP listen address (host:port). |
| `OM_DATA` | `data/openmarkets.json` | Path to the JSON state snapshot (atomic temp-file + rename writes). Mount a volume here in production. |
| `OM_VERSION` | `phase-a` | Version string reported by `/health` and embedded in feed responses. |
| `OM_VOLUME_REF` | `20000` | Net-supply units that map to a full index swing. **Must match the C# client's `VolumeRef`** or server and client disagree on how far a report moves the price. |
| `OM_INDEX_MIN` | `0.5` | Hard floor for any published price index (matches the client's `MinIndex`). |
| `OM_INDEX_MAX` | `2.0` | Hard ceiling for any published price index (matches the client's `MaxIndex`). |
| `OM_READ_TIMEOUT_SEC` | `10` | Per-request HTTP read timeout, in seconds. |
| `OM_WRITE_TIMEOUT_SEC` | `10` | Per-request HTTP write timeout, in seconds. |
| `OM_RATE_PER_MIN` | `120` | Max requests **per minute per client IP** (fixed-window limiter). `0` disables rate limiting. Set to `0` for local in-game testing (see [§2](#2-quick-start-local-dev)). |
| `OM_CONSOLE` | `1` | Serve the `/console` operator UI when non-zero. Set `0` to disable it (recommended in production). |
| `OM_DUE_INTERVAL_SEC` | `2700` | Due-clock: real seconds that equal one installment period. Default is **45 min ≈ one in-game day at 1×**, so the wall-clock period matches the city's day rhythm. The client learns this value (via `/citystate`) and paces its auto-settle sweep to it. **Lower it for testing** (e.g. `120`) so installments/bonds cycle observably in one session — `run-local.ps1` sets `120`. |
| `OM_DUE_GRACE` | `1` | Extra installment intervals allowed before a due installment counts as missed. |
| `OM_DUE_MAX_MISSES_PER_TICK` | `4` | Cap on overdue installments processed per item per tick (backlog drain). |
| `OM_DUE_OFFLINE_GRACE` | `5` | Extra grace intervals for an obligor that appears offline (away players get this many extra periods before an auto-bond). `0` turns offline grace off. |
| `OM_DUE_OFFLINE_THRESHOLD_SEC` | `120` | Seconds since an account's last authenticated request after which it counts as offline (feeds `OM_DUE_OFFLINE_GRACE`). |

> **Client/server agreement:** `OM_VOLUME_REF`, `OM_INDEX_MIN`, and `OM_INDEX_MAX` mirror
> constants baked into the C# mod. Keep them in sync or the in-game price nudge will not match
> the server's published index.

---

## 4. Connecting the game client

- **Solo play needs no server.** A single-player city trades at **static base prices** (`MarketFeed`,
  index 1.0). The server is only for the **online** layer (shared price index, cross-player
  trades/bonds/loans, co-op levers). (`LocalPriceSim` is no longer the solo price source — post-M9 it's
  just the net-supply report accumulator.)
- In the mod's **Options → Open Markets**, the player sets up an **account + league** and a
  **server base URL**. Point that URL at this running server:
  - LAN: `http://<this-machine-ip>:8080`
  - Cross-internet: a Cloudflare Tunnel HTTPS URL (see [§7](#7-production-deployment)).
- The client mints an anonymous account (`POST /accounts`), then creates or joins a league;
  the in-game city then becomes one of the players reporting net supply and trading.
- **Graceful degradation:** every online call is best-effort. A timeout / 404 / 410 / EOL flag
  is normal, not an error — the client reverts to local static prices and the save stays
  loadable with or without the server.

**Auth model** (`server/internal/api/api.go`): accounts authenticate with an id + secret, sent
either as `Authorization: Bearer <id>.<secret>` **or** as `?account=&secret=` query params (the
latter lets a bare `UnityWebRequest.Get` authenticate without custom headers). The secret is
returned **once** at account creation and stored client-side; the server keeps only a salted
hash.

---

## 5. Operator console

The `/console` page (served when `OM_CONSOLE=1`, the default) is a browser-based stand-in for a
second player — it lets you act as a **counterparty for testing without running a second copy of
the game**. It is a thin client over the same public API; each "city" you create stores its
credentials in *that browser*, and you switch between cities with a dropdown.

From the console you can:

- **Drive a city** — mint accounts, set display names, create/join leagues (with join codes).
- **Trades** — offer two-sided basket trades and accept / decline / cancel / settle them,
  including per-installment settlement and shortfalls.
- **Contracts & bonds** — offer single-sided contracts, settle installments, and watch bonds.
- **Loans (co-op negotiation)** — offer → counter → accept / decline / cancel.
- **Co-op levers (M8)** — `investment-office` (grant a friend a buff) and `bailout`
  (pay down a friend's defaulted debt).
- **Price index** — post net-supply reports and watch the shared **league price index** move
  (net supply pushes it below 1.0 = cheaper, net demand above 1.0 = dearer, clamped to the
  `OM_INDEX_MIN`–`OM_INDEX_MAX` band).

A full guided demo (two cities in one browser running an offer → consent → settle loop) is in
[`server/QUICKSTART.md`](../server/QUICKSTART.md). The same actions are scriptable headless via
the `omctl` operator CLI (`cmd/omctl`); `cmd/omsim` is the randomized-economy sim harness used
by `make verify`.

---

## 6. Verifying

The full pre-merge / CI gate is:

```bash
make verify
```

which runs, in order (`server/Makefile`):

1. **`fmt-check`** — fails if any file needs `gofmt`.
2. **`vet`** — `go vet ./...`.
3. **`race`** — `go test -race ./...` (the full test suite under the race detector).
4. **`sim`** — `go run ./cmd/omsim -sweep 200`: the randomized-economy invariant harness across
   200 seeds, checking cash conservation, austerity escapability, and no stranded/overflow
   state. Fails non-zero on the first violation.
5. **`fuzz`** — a short fuzz pass over the money math (`FuzzAmortize`, `FuzzTotalDueCents`,
   `FuzzLineValueCents`, `FuzzValidateBondTerms`); bump `FUZZTIME` (default `10s`) to hunt harder.

It is pure Go — no game DLLs needed.

**Runtime cash-conservation check:** `GET /audit?league=<id>` (authenticated as a league member)
returns the per-account net cents and the league total. In a closed economy the total should
remain conserved — a non-zero drift signals a settlement/booking bug. The `omsim` sweep asserts
the same invariant offline.

Other handy targets: `make test`, `make race`, `make cover`, `make vet`, `make fmt`,
`make clean` (removes `bin/` and `data/`), `make docker` (builds the image).

---

## 7. Production deployment

Production is **self-hosted** — full design in [`BACKEND.md`](../BACKEND.md). Summary of the
intended path:

- **Substrate:** any small always-on container or VM (home server, VPS, or small cloud
  instance), running the stack via **Docker Compose** (`server/Dockerfile`,
  `server/docker-compose.yml`) for reproducible rebuild/migration.
- **Ingress:** **Cloudflare Tunnel (`cloudflared`)** — an *outbound* connection from your host to
  the Cloudflare edge, so there are **no inbound ports**, no exposed IP, no port-forwarding,
  and free valid TLS 1.2+ at the edge (which the net35/Mono client needs). It also solves
  dynamic residential IPs and adds WAF/DDoS + edge caching for hot read endpoints. (Alternative:
  port-forward 443 → Caddy + Dynamic DNS — more moving parts; the tunnel is preferred.)
- **Data:** the design swaps the JSON `store.Store` for **Postgres**, kept **internal-only** on
  the compose network and never published; only the stateless API talks to it. Postgres has a
  persistent volume with nightly `pg_dump` backups off-box.
- **Production config:** keep `OM_RATE_PER_MIN=120` (the default) and set `OM_CONSOLE=0` to hide
  the operator UI. Mount a persistent volume at `OM_DATA`.
- **Dead-server posture:** the whole online layer is best-effort. If the server is down,
  clients degrade to local static prices and saves still load; an end-of-life flag (`GET /eol`)
  can retire old clients to local-only permanently.

See [`BACKEND.md`](../BACKEND.md) for the topology diagram, data model, security segmentation,
and backup/recovery details.

---

## 8. Persistence & data

- **Where state lives:** a single JSON snapshot at the `OM_DATA` path
  (default `server/data/openmarkets.json`). Writes are **atomic** (temp file + rename). The
  server persists on each mutating write and also runs a **periodic durability flush every 30s**
  as a backstop, plus a final flush on graceful shutdown (`SIGINT`/`SIGTERM`).
- **Start clean:** stop the server and delete the snapshot file (or run `make clean`, which
  removes both `bin/` and `data/`). A fresh store mints a new **epoch**.
- **Epoch / reset behaviour:** the store carries a data-tied **epoch** id, generated once when
  the store is fresh and then persisted in the snapshot. A normal restart re-loads the same
  epoch, so it looks unchanged to clients. The epoch only changes when the data is genuinely
  **wiped** (snapshot deleted) — that is the one time it is safe for clients to replay
  settlements from zero (no old events to double-book). Clients use an epoch change to detect a
  server reset.
- **Ephemeral (not persisted) state:** the live market price-event/shock map and each account's
  `lastActive` online signal (used by the due-clock's offline grace) are runtime-only and reset
  on restart.
