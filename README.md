# Open Markets

**A living trade economy for Cities: Skylines 1.**

Vanilla Cities: Skylines treats the outside world as a faceless void — trucks and trains haul goods
in and out, and the money is just abstract tax. **Open Markets turns that void into a real commodity
market.** Every truck and train of imports/exports books real, per-commodity trade income instead of
faceless tax. Play solo and you trade at stable base prices; connect to a friends' **league** and the
market comes alive — one shared price index where flooding it with exports drops the price, hoarding
lifts it, and global events send shocks through everyone's economy at once. Suddenly *what*, *where*,
and *when* you trade actually matters.

> ⚠️ This is a mod for **Cities: Skylines 1** (the original), **not** Cities: Skylines II.

![Open Markets — a living trade economy for Cities: Skylines 1](PreviewImage.png)

<!-- Got a great in-game shot? A Markets-dashboard screenshot/GIF in docs/screenshots/ makes an even better hero. -->

---

## Why it's fun

- **Prices actually move (online).** In a friends' league, one shared per-commodity index rises and falls
  with everyone's net trade, and random **global price-swing events** hit the whole league at once — there
  are good days to sell and good days to buy.
- **Your trade has consequences (online).** Elasticity: dump 50,000 tons of goods into the league market and
  you depress its selling price for everyone. Comparative advantage and timing become real strategy.
- **You're a trader now, not just a mayor.** Watch the price board, spot a spike, time your trades, profit.
- **It's ambient.** No building to plop, no tech to unlock, no micromanagement tax. Solo, it just makes your
  trade income real (at stable base prices); add a league and the living market switches on.
- **It's safe to try.** Fully save-compatible and removal-safe — uninstalling leaves your city perfectly
  loadable (see [Safety](#safety--compatibility)). Online is opt-in; offline there's zero network activity.

---

## Features

**📈 A real commodity market**
- Every import/export books **mod-owned, per-commodity trade income** (no more faceless tax). Solo, prices
  are stable base values; online, each commodity has a **live league price index**.
- Online, the index **drifts with supply** and occasionally gets hit by a **global price-swing event** (a
  ±25–50% shock lasting several in-game days) shared across the whole league.

**📊 The Markets board**
- A per-commodity price board: live index, green/red movement vs. base, a `·BUY`/`·SELL` hint when a
  commodity is cheap/dear, and a **server-served price-history sparkline**. One price per commodity — no
  partner columns, no embargo (the M9 single-index model).

**💰 Balance of trade**
- Lifetime **exported / imported / net §** from outside-connection trade, at a glance.

**🤝 Play with friends — a shared league economy** *(online, opt-in)*
- **One living market:** your league shares a single per-commodity index, so a neighbor dumping oil moves
  *your* price. Global price events hit everyone at once.
- **Peer trade contracts & two-sided baskets** with frozen, index-priced terms; every offer shows its
  **total deal value**.
- **Bonds & credit:** a missed installment auto-converts to a bond; negotiate peer loans.
- **Austerity:** a city that defaults gets garnished and locked down (taxes/budgets/demand) until it
  recovers — on a server **wall-clock** clock, so you can't dodge it by quitting.
- **Co-op city levers:** **invest** in a friend (a §-scaled boost to a demand channel you pick — Residential,
  Commercial, or Industry & Office — the § transfers to them) or **bail them out** of austerity.
- **Leaguemate city profiles:** see each member's online status, population, popularity, industry breakdown,
  and finances.

**🚚 Inventory-backed delivery (Industries DLC)**
- Trades that move physical goods deliver into `[trade]`-tagged warehouses (and spill safely when full) — the
  goods actually arrive, not just the cash.

**🎚️ Options**
- Charge the treasury for imports (net = exports − imports), Chirper alerts, auto-settle contract
  installments, and the online account/league setup. (Pricing is server-owned online — there are no
  client volatility/elasticity/spread knobs.)

---

## Install

### Steam Workshop (recommended)
*Coming soon — Workshop link will go here once published.* Subscribe and it auto-installs with its
dependency (Harmony).

### Manual
1. Download the latest [release](../../releases) `.zip`.
2. Extract `OpenMarkets.dll` + `CitiesHarmony.API.dll` into:
   - **Windows:** `%LOCALAPPDATA%\Colossal Order\Cities_Skylines\Addons\Mods\OpenMarkets\`
   - **macOS:** `~/Library/Application Support/Colossal Order/Cities_Skylines/Addons/Mods/OpenMarkets/`
   - **Linux:** `~/.local/share/Colossal Order/Cities_Skylines/Addons/Mods/OpenMarkets/`
3. Launch the game → **Content Manager → Mods → enable "Open Markets"**.

### Requirements
- **Cities: Skylines 1** (any recent version).
- The **[Harmony](https://steamcommunity.com/sharedfiles/filedetails/?id=2040656402)** Workshop mod
  (boformer's) — the game will prompt to auto-subscribe.
- *Industries DLC* is optional and only adds the inventory-backed delivery of physical goods on trades;
  everything else works without any DLC.

---

## How to play

1. **Enable the mod** and load any city that does some import/export (most cities do).
2. Open the **Open Markets terminal** and check the **Market** tab — solo, every commodity sits at its base
   price (index 1.00).
3. **Go online (optional):** in **Options → Open Markets**, create an account + league (or join a friend's
   code). It uses the **public community server by default** — or point it at your own (see
   [Servers](#servers--play-on-ours-or-host-your-own)).
4. Now the board comes alive: the index moves with the league's trade, `·BUY`/`·SELL` hints flag cheap/dear
   commodities, sparklines fill in, and price-event Chirps fire.
5. **Trade with friends:** offer contracts / baskets from the terminal (each shows its total value), settle
   installments, and watch the **Members** tab for everyone's city profile and standing.
6. Open **Balance** for your lifetime exported / imported / net §.

---

## Servers — play on ours, or host your own

The online league layer talks to a small server. Two easy paths:

### Join the public server (zero setup)
The mod ships pointing at the **official community server** — `https://cstrading.udonitus.com` — as its
built-in default, so there's **nothing to configure**. Enable the mod, open **Options → Open Markets**,
create an account, and create/join a league with a friend's code. It's a **free, best-effort community
instance** (no uptime guarantee). Solo play needs no server at all.

### Host your own (easy — one Docker command)
The backend (`server/`, `openmarketsd`) is a tiny **standard-library-only Go service**: a single static
binary in a distroless image, with JSON-file persistence and **no database to set up**. Ideal for a private
friend group:

```bash
cd server
docker compose up --build      # listens on :8080, state persisted to a named volume
curl localhost:8080/health     # {"status":"ok",...}
```

Then in-game, **Options → Open Markets → "Server base URL"** → point it at your host (e.g.
`http://your-host:8080`, or your own HTTPS domain). Everyone in the league sets the same URL.

For a public/remote server, front it with a reverse proxy or a **Cloudflare Tunnel** for free TLS — no
port-forwarding and no exposed IP (the client uses HTTPS by default so account tokens aren't sent in clear).
Full walkthrough — configuration, TLS, Kubernetes/k3s manifests (`deploy/k3s/`), and backups — is in
[`docs/RUNNING-THE-SERVER.md`](docs/RUNNING-THE-SERVER.md) and [`BACKEND.md`](./BACKEND.md).

---

## Roadmap

Open Markets is built in milestones — **M0–M9 are shipped** (solo economy + the full online league layer):

| | |
|---|---|
| ✅ **M1–M3** | Mod-owned per-commodity trade income, the Markets board, sparklines, balance report |
| ✅ **M4** | **Online "play with friends"** — accounts, leagues, the Go backend, a shared price feed |
| ✅ **M5–M6** | Peer trade **contracts** + two-sided **baskets**, and `[trade]`-tagged inventory delivery |
| ✅ **M7–M8** | **Bonds/credit**, **austerity** (garnishment + tax/budget/demand locks), and **co-op city levers** (invest / bailout) |
| ✅ **M9** | **Market rework** — one server-owned per-commodity index, global price events, server-served sparklines; solo = static base prices |

The online layer **has landed**: a self-hosted [Go backend](./server) runs the shared market and a web
console acts as a counterparty city. It's always optional and degrades gracefully offline. See
[`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) for how it all fits together and [`BACKEND.md`](./BACKEND.md)
for the backend design.

---

## Safety & compatibility

- **Save-safe & removal-safe.** Open Markets stores its data in its own versioned save section and never
  writes anything the base game can't read back. **Uninstall the mod and your city still loads cleanly.**
- **Plays nice.** The money hook is an additive Harmony patch; inventory delivery uses public game APIs
  (no patch). It's designed to coexist with transfer/cargo mods like TM:PE and MoreEffectiveTransfer — if you
  hit a conflict, please [open an issue](../../issues).
- **Multiplayer note:** the online league layer is **opt-in**; with no account/league configured there is
  zero network activity.

---

## Build from source

You only need a **modern .NET SDK** (e.g. `dotnet` 8) — **no Windows, no Mono, no .NET Framework install.** The
mod targets `net35`, but a NuGet reference-assembly package (`Microsoft.NETFramework.ReferenceAssemblies.net35`)
supplies the net35 BCL, and `CitiesHarmony.API` supplies Harmony — both restored automatically. The build is
already cross-platform (Windows / macOS / Linux) and the produced `OpenMarkets.dll` is platform-independent.

```bash
dotnet build OpenMarkets.sln -c Debug
```

### The only requirement: your game's 4 `Managed` DLLs

The compiler + net35 are handled by NuGet; the **single** thing the build needs from outside is four
**copyrighted game assemblies** (not redistributable, so there is no public package):
`ICities.dll`, `ColossalManaged.dll`, `Assembly-CSharp.dll`, `UnityEngine.dll`. Two ways to provide them:

- **Install Cities: Skylines 1** (it's a native macOS / Windows / Linux Steam game). `Directory.Build.props`
  has the per-OS Steam `Managed` paths baked in, so `dotnet build` then **just works with zero config**.
- **Or copy just those 4 files** from any existing install into a folder, and point the build at it: copy
  `Directory.Build.props.user.example` → `Directory.Build.props.user` (gitignored) and set
  `<ManagedDir>/path/to/Managed</ManagedDir>`, or pass `-p:ManagedDir=…` on the command line.

The build fails fast with a clear message if the path is wrong, and **auto-deploys** the result to the game's
Mods folder (`ModsDir`). What *compiles* needs only these DLLs; what still needs the **running game** is the
in-game visual/render and live-economy checks.

> **macOS note:** developing this mod on a Mac is fully supported — the perceived "Windows-only" blocker is just
> these 4 DLLs, *not* net35 or the toolchain. The Go backend (`server/`) builds and tests on macOS natively.

Deeper docs for contributors: [`DESIGN.md`](./DESIGN.md) (rationale), [`CHANGELOG.md`](./CHANGELOG.md)
(project history), and [`CONTRIBUTING.md`](./CONTRIBUTING.md) (how to build, test, and contribute).

---

## Contributing

Issues and PRs welcome — bug reports, compatibility findings, balance feedback, and crisis-bank ideas
especially. See [`CONTRIBUTING.md`](./CONTRIBUTING.md) for the platform constraints (target `net35`, no
`async`/`await`, additive Harmony patches, save-safety preserved) and how to build/test/run the server.

---

## Credits

Built on patterns from the CS1 modding community:
[CitiesHarmony](https://github.com/boformer/CitiesHarmony) (boformer),
[MoreEffectiveTransfer](https://github.com/pcfantasy/MoreEffectiveTransfer),
[EnhancedOutsideConnectionsView](https://github.com/rcav8tr/CS1Mod-EnhancedOutsideConnectionsView),
[AdvancedOutsideConnection](https://github.com/DNKpp/CitiesSkylines_AdvancedOutsideConnection).

## License

[MIT](./LICENSE) — free to use, modify, and redistribute.
