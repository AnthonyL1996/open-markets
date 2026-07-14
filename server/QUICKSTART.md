# Quickstart — run the backend + operator console (no game needed)

Everything here runs on the dev machine (Go is installed). You'll have a live price-index + contracts
service and a browser console to drive it in ~2 minutes — useful for exploring the online layer before the
mod is built on Windows.

## 1. Start the backend

```bash
cd server
go run ./cmd/openmarketsd
```

You'll see `listening on :8080 (version=phase-a, data=data/openmarkets.json)`. State persists to
`server/data/openmarkets.json` (delete it to start clean). Leave it running.

> Prefer a binary? `make build` → `./bin/openmarketsd`. Tests: `make test` (or `make race`).

## 2. Open the console

Browse to **http://localhost:8080/console** — the operator UI. Everything below is clickable there; no CLI
needed. (Each "city" you create stores its credentials in *this browser*, so use one tab and the **city**
dropdown to switch between them.)

## 3. Drive a demo contract (two cities, one browser)

1. **City A** — click **+ new**, name it "City A". It mints an account (you'll see it in Activity).
2. With City A active, click **Create league** (name it anything). Note the **join code** shown.
3. **City B** — click **+ new**, name it "City B". Copy City A's **account id** (shown under City / account
   — switch to City A to read it, or grab it from the Activity log).
4. Switch to **City B**, paste City A's join code into **Join code**, click **Join**.
5. Still as City B, in **Offer contract**: set *Counterparty* = City A's account id, *Side* = "I sell",
   *Commodity* = `Oil`, *Qty* = 2000, *Unit price* = 140, *Installments* = 3 → **Offer**.
6. Switch to **City A** → the **Contracts** table shows the offer as `offered` with an **accept**/**decline**
   button. Click **accept** → it goes `active`.
7. City A now shows a **pay** button each refresh; click it 3× (City B clicks **collect** on its side). The
   progress bar fills; after both sides settle all 3, the contract reads `completed`.

That's the full offer → consent → settle loop the in-game client will perform — here you're simulating both
players by hand.

## 4. Watch the shared price index (optional)

As any city in the league, use **Post report** to submit net supply (e.g. `Oil` `+10000` = net exporter).
Have both cities report the same commodity and watch **League price index** move: net supply pushes the
index **below 1.0** (cheaper), net demand **above** (dearer), clamped to 0.5–2.0. This is the per-commodity
index the in-game client books trades at (solo play uses the static base, index 1.0).

> The console also drives the rest of the league economy — two-sided **trades**, **bonds**/loans, and the
> co-op **invest**/**bailout** levers — not just contracts. See [`internal/api/console.html`](internal/api/console.html).

## 5. CLI equivalent (optional)

The same actions are scriptable with `omctl` (handy for automation/seeding):

```bash
go build -o /tmp/omctl ./cmd/omctl
/tmp/omctl -server http://localhost:8080 -profile /tmp/cityB.json account
/tmp/omctl -server http://localhost:8080 -profile /tmp/cityB.json league-join -code XXXX-XXXX
/tmp/omctl -server http://localhost:8080 -profile /tmp/cityB.json offer -to <CityA_id> \
    -side sell -commodity Oil -qty 2000 -price 140 -n 3
/tmp/omctl -server http://localhost:8080 -profile /tmp/cityB.json list
```

## 6. Pointing the game at this backend (later, on Windows)

In the mod's Options → Open Markets, set **Server base URL** to this backend (`http://<this-machine-ip>:8080`
on a LAN, or a Cloudflare Tunnel HTTPS URL for cross-internet play — see `BACKEND.md`), create/join the same
league, and the in-game city becomes one of the players. Full in-game test steps: `docs/TESTING-m4-phase-a.md`.
