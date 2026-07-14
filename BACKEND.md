# Open Markets — Backend Architecture (self-hosted)

> Status: **reference implementation built** — see [`/server`](server/) (`openmarketsd`).
> This describes the server side of the optional online layer (see `README.md` for the feature overview).
> The design targets a small, self-hosted deployment (a home server, a VPS, or any small always-on box).
> **Language: Go** (stdlib-heavy). The Phase A service (identity, friend-group leagues, `/report`, `/index`,
> `/prices`) is implemented with an in-memory + JSON `store.Store` whose interface is the Postgres swap-in
> seam described below. See `/server/README.md` and `docs/RUNNING-THE-SERVER.md` for running it yourself.

## 0. Why a central server (recap)
A small central server is required — P2P (no CS1/Steam networking to mods, async clients), Steam Cloud
(per-user blobs, no aggregation), and a static blob (no writes/matching) all fail. The server is a **referee +
ledger + bulletin board**, never a simulator: clients run the full game locally and poll over HTTPS.

## 1. Decision: self-hosted
- **Substrate:** any small always-on host works — a container (LXC/Docker) or small VM on a home server, a
  cheap VPS, or a small cloud instance — running the stack via **Docker Compose** (reproducible, easy to
  rebuild/migrate).
- **Cost:** minimal — a small compute footprint plus a domain (~$10/yr). No mandatory cloud bill.
- **Trade-off (accepted):** a self-hosted server's uptime is bounded by its host's power/network/reboots.
  **This is OK** because the whole online layer is best-effort with graceful local fallback (§4 below) — a
  down server just means clients run single-player until it's back. Fine for a friends-scale deployment;
  the compose stack moves unchanged if you outgrow whatever host you started on.

## 2. Topology — ingress via Cloudflare Tunnel (recommended)
The hard part of self-hosting on a residential or otherwise NAT'd network is exposing an internet-facing
HTTPS endpoint (dynamic IP, NAT, open-port security risk). **Cloudflare Tunnel (`cloudflared`) solves all of
it** and is the recommended ingress.

```
 CS1 client (mod)                 Cloudflare edge              Self-hosted server
 ┌────────────────┐   HTTPS      ┌──────────────┐  tunnel   ┌─────────────────────────────┐
 │ UnityWebRequest│ ──────────►  │ TLS termination,│ ◄═════► │ container/VM: docker-compose│
 │ (main thread)  │  api.your-    │ WAF, DDoS,     │ (outbound│  ├ cloudflared (tunnel)     │
 │ poll/report    │  domain       │ caching        │  only)  │  ├ API service (stateless)  │
 └────────────────┘ ◄──────────  └──────────────┘           │  └ Postgres (internal only) │
   degrades to local                                          └─────────────────────────────┘
```

**Why Cloudflare Tunnel:**
- **No port-forwarding, no exposed IP** — the tunnel is an *outbound* connection from your host to
  Cloudflare; nothing inbound on the router. Removes the biggest self-hosting security risk.
- **Free, valid TLS at the edge** — Cloudflare presents a trusted cert, so the net35/Mono client (which uses
  the OS TLS stack via `UnityWebRequest`) gets a clean **TLS 1.2+** handshake with zero cert hassle. This is
  exactly what the client `TlsSmokeTest` confirms against the real endpoint.
- **Solves dynamic residential IP** (no DDNS needed) + free WAF/DDoS + edge caching for the hot read endpoints
  (the price index / crisis are identical per league → cache them).

**Alternative (if avoiding Cloudflare):** port-forward 443 → **Caddy** reverse proxy (auto Let's Encrypt
certs) → API, plus **Dynamic DNS** (DuckDNS / Cloudflare DDNS) for the IP. More moving parts and exposes your
IP; Tunnel is cleaner.

## 3. The application stack
- **Reverse proxy / ingress:** `cloudflared` (or Caddy in the alt path). Postgres is **never** exposed publicly
  — only the API talks to it on the internal compose network.
- **API service:** one **stateless** HTTP service speaking JSON. Language is **open** (Go = single static
  binary, tiny container, great fit; Node/Python fine). Stateless so it restarts/scales trivially.
- **Database:** **Postgres** (in the compose stack, on a persistent volume). Relational matching + transactions
  for order fills. (SQLite could start it, but Postgres is the right call once the order book exists.)
- **Scheduler:** a cron/worker (sidecar or in-process job) for the scheduled tasks — crisis selection, index
  aggregation rollups, leaderboard/season windows.
- **Monitoring (optional, self-hosted):** Uptime Kuma for liveness; container logs for the rest.

## 4. Dead-server rail (must hold even though it's *your* server)
Because a self-hosted server can and will go down, this is doubly important:
- Every online call is **best-effort with local fallback**; a timeout/404/410 is **normal**, not an error
  popup. The client reverts to `LocalPriceSim` (already how `RemotePriceSource` degrades).
- Ship an **end-of-life flag** the server can publish so old clients go **local-only forever** if you retire it.
- **Mod removal / server death never blocks a save load** (NFR-3). Online state is never load-critical.

## 5. API surface (grows per phase)
All endpoints: HTTPS, JSON, auth token in header, every payload carries `{schemaVersion, modVersion}` (server
soft-degrades mismatches). Read endpoints are cache-friendly; write endpoints are rate-limited.

- **Identity (foundation):** `POST /account` (issue id + secret token), `POST /friend` (add by code),
  `GET /friends`.
- **Phase A — price index:** `POST /report` (anonymized net supply/demand), `GET /index?league=` (cached,
  clamped, outlier-trimmed), `GET /crisis` (current cached crisis from the curated bank).
- **Phase B — order book + contracts:** `POST /order`, `DELETE /order/:id`, `GET /book?commodity=`,
  `GET /fills`; `POST /contract` (loan/future/insurance/bond), `POST /contract/:id/accept|settle`,
  `GET /contracts`, `GET /credit`.
- **Phase C — competition:** `POST /score`, `GET /leaderboard?metric=&window=`, `GET /season`, `GET /h2h`.
- **Phase D — social:** `GET /profile/:friend`, `PUT /profile` (name/age/stats/tagline), `POST /pact`,
  `POST /embargo`, `GET /feed`, `POST /taunt` (canned ids only).
- **Ops:** `GET /health`, `GET /eol` (end-of-life flag), `POST /report-abuse`.

## 6. Data model (sketch)
`accounts`(id, friend_code, secret_hash, created, reputation) · `friendships`/`leagues` ·
`reports`(account, commodity, net_supply, ts) → `index_snapshots`(league, commodity, value, ts) ·
`crises`(id, region, commodity, magnitude, duration, headline, ts) ·
`orders`(id, account, commodity, side, qty, limit_price, status) · `fills`(buy_order, sell_order, qty, price, ts) ·
`contracts`(id, type, party_a, party_b, terms_json, schedule, status) ·
`scores`(account, metric, value, window) · `moderation`(reports, bans, tagline_queue).
No PII; profiles store only the chosen name + aggregate stats + a moderated tagline.

## 7. Auth & anti-abuse
- **Auth:** anonymous account → server issues a **secret token** (store hash); token in `Authorization`.
  Friend **codes** are shareable public ids. No email/password required (keeps PII out, moderation cheap).
- **Anti-abuse:** per-IP signup throttle (Cloudflare helps), per-window rate limits on every write, server-side
  **plausibility** (cash/inventory deltas vs. city size — cross-validate the City Profile stats),
  **shadow-trim** index outliers, ban list, report endpoint, tagline moderation queue. A cheater only wrecks
  their own standing.

## 8. Security posture (self-hosted specific)
- **No inbound ports** (Cloudflare Tunnel) → your IP hidden, no router exposure.
- **Network-segment the container/VM** (dedicated bridge / VLAN if your hypervisor supports it) so a
  compromise can't pivot into the rest of your network.
- **Postgres internal-only**, strong creds, never published. API runs **non-root**. Keep images patched;
  automatic or manual updates. Secrets via env/compose `.env` (gitignored), never committed.
- Treat it as a real public service: rate limits + WAF + minimal attack surface.

## 9. Backups & recovery
- **Postgres:** nightly `pg_dump` → off-box (a NAS, second disk, or object storage); test a restore.
- **Host snapshots:** if your hypervisor supports it, scheduled container/VM snapshots. The whole stack is a
  compose file + a DB dump → rebuildable on any host (this is also the migration path to a different host).

## 10. Open decisions
- **Ingress:** Cloudflare Tunnel (recommended) vs. port-forward + Caddy + DDNS.
- **Domain/subdomain** for the API (e.g. `api.openmarkets.<domain>`).
- **League scope of the index:** currently **friend-group only** — `?league=` is keyed to the friend-code
  group; global/hybrid scopes could layer on later.
