# Open Markets

A trade-economy mod for **Cities: Skylines 1** (the original, not CS2).

Vanilla treats the outside world as invisible tax: trucks and trains roll in and out, and all you see is a number. Open Markets replaces that with an actual commodity market. Every import and export earns real per-commodity money. Play alone and prices sit at stable base values. Play with friends and you share one live market, where dumping exports pushes a price down for everyone and the occasional global shock sends it swinging.

The idea is borrowed from *SimCity* (2013) and its online multiplayer regions — the thing that made that game special was that your city was never a private island. You and your friends built neighbouring cities that specialised, traded resources back and forth, and pooled effort into shared Great Works, and it genuinely stung when those servers were retired. Open Markets brings that spirit to Cities: Skylines 1, which otherwise only ever plays solo on your own map. You join a **league** of friends who share one economy and cooperate on projects, while seeing how you stack up against everyone else on a global board. The point isn't to build alone — it's to build *together* with your friends and *against* other leagues, and to have your neighbours' decisions actually reach your city.

![Open Markets](PreviewImage.png)

> **Quickstart:** install it (subscribe on the Workshop, or drop the two DLLs in your mods folder) and enable it in Content Manager → Mods. Open the Markets board and you're trading. For the living market, go to Options → Open Markets, create an account, and share a league code with friends — the public server is already the default.

## What it does

Solo, it's quiet. Your outside-connection trade earns real per-commodity income at fixed prices, and a Markets board shows what each commodity is worth right now. Nothing to place, nothing to unlock.

The mod really comes alive online, where a **league** of friends shares one economy: the same market, the same prices, the same events. What your leaguemates do reaches your city.

## Features

**The market**
- Every import/export (truck, train, ship, or plane) books real per-commodity income instead of faceless tax.
- In a league, each commodity has one **shared price index** that your trade moves: net exports push it down for everyone, hoarding lifts it. It's **server-owned and clamped** (about 0.5×–2× base), so it tracks real supply and nobody can spike it at will.
- The server periodically fires **league-wide price events** (spikes and slumps over several in-game days), so there are good and bad windows to sell.

**Leagues & co-op**
- **Trade deals** — one-off or recurring **contracts** and multi-item **baskets** at frozen, index-priced terms; every offer shows its total value before you agree.
- **Influence RCI demand** — the **invest** lever spends § to give a leaguemate a targeted boost to a demand channel you choose (Residential, Commercial, or Industry & Office, plus attractiveness). The § transfers to them, so it's a real decision, not free money.
- **Great Works** — leagues cooperate on shared megaprojects; finishing one grants lasting bonuses, like an export price edge.
- **Shared city metrics** — the members panel shows each leaguemate's population, popularity, industry mix, finances, and online / last-seen status, so you can see who's booming and who to trade with (or prop up).
- **Bonds, loans & bailouts** — a missed installment becomes a **bond** instead of just failing; negotiate peer loans, or pay down a friend's debt to rescue them.
- **Austerity** — a city that defaults gets garnished and locked down (tax, budget, and demand) on the server's **real-time clock**, so quitting or reloading won't dodge it.

**Fair play**
- The **server is the referee** — pricing, settlement, and every timer live server-side, so a modified client can't fake prices or conjure income.
- **Money is conserved and audited** — each league's balances must net to zero, so no exploit quietly mints §.
- **Rate limits** on every endpoint plus a stricter cap on account creation; token auth over HTTPS.
- **Failure is only reputational** — an unsettled or defaulted deal never blocks a save from loading. Worst case is austerity and a dented standing, never a broken city.

**Also**
- **Real delivery (Industries DLC)** — trades that move physical goods actually ship into `[trade]`-tagged warehouses, not just settle cash.
- **Safe to try** — save- and removal-safe, additive Harmony patch, coexists with TM:PE and MoreEffectiveTransfer. Offline there's zero network activity.

## How online play works

Everyone runs their own game locally. The server is a shared referee and ledger, never a simulator, and solo play skips it entirely.

![How online play works: each player's city reports trade and deals to the shared OpenMarkets server over HTTPS, and the server returns shared prices, contracts, and standings from its ledger.](docs/screenshots/how-online-works.png)

### How prices move

Your trade is what moves the market. Each member reports their net supply; the server aggregates it per commodity, clamps how far it can swing, layers on any active global event, and feeds the same price back to everyone.

![How prices move: members report net supply, the server aggregates per commodity, clamps elasticity to 0.5x-2x base, applies any global event, and feeds one shared price index to everyone's board and deal pricing.](docs/screenshots/how-prices-move.png)

## Solo vs online

| | Solo | In a league |
|---|---|---|
| Per-commodity trade income | Yes | Yes |
| Markets board | Yes — stable base prices | Yes — live, moving prices |
| Prices move with your trade | — | Yes |
| Global price events | — | Yes |
| Contracts & baskets | — | Yes |
| Bonds, loans & austerity | — | Yes |
| Invest / bailout / Great Works | — | Yes |
| Needs a server | No | Yes — public by default, or self-host |
| Network activity | None | HTTPS to your league server |

## Install

**Steam Workshop:** subscribe and it pulls in Harmony for you. *(Link goes here once it's published.)*

**Manual:** download the latest [release](../../releases), drop `OpenMarkets.dll` and `CitiesHarmony.API.dll` into your mods folder, then enable it in Content Manager → Mods.

- Windows: `%LOCALAPPDATA%\Colossal Order\Cities_Skylines\Addons\Mods\OpenMarkets\`
- macOS: `~/Library/Application Support/Colossal Order/Cities_Skylines/Addons/Mods/OpenMarkets/`
- Linux: `~/.local/share/Colossal Order/Cities_Skylines/Addons/Mods/OpenMarkets/`

You'll need the [Harmony](https://steamcommunity.com/sharedfiles/filedetails/?id=2040656402) mod (the game offers to grab it automatically). The Industries DLC is optional.

## Playing with friends

The online side talks to a small server, and you have two options.

**Use the public one.** The mod already points at a free community server (`cstrading.udonitus.com`), so there's nothing to configure. Open Options → Open Markets, create an account, and create or join a league with a friend's code. It's best-effort, with no uptime promises.

**Or run your own.** The backend is a small Go service with no database to set up, so it's one Docker command:

```bash
cd server
docker compose up --build      # serves on :8080
```

Then point everyone's **Options → Open Markets → Server base URL** at your host. For anything internet-facing, put it behind a reverse proxy or a Cloudflare Tunnel so it gets TLS. The full walkthrough is in [`docs/RUNNING-THE-SERVER.md`](docs/RUNNING-THE-SERVER.md).

## Building it

You need a modern .NET SDK. No Windows or Mono install required; the net35 target is handled through NuGet.

```bash
dotnet build OpenMarkets.sln -c Debug
```

The one thing the build can't provide is four copyrighted game DLLs (`ICities`, `ColossalManaged`, `Assembly-CSharp`, `UnityEngine`). If Cities: Skylines is installed, `Directory.Build.props` finds them automatically; otherwise copy them somewhere and set `ManagedDir` (see `Directory.Build.props.user.example`). It builds on Windows, macOS, and Linux. More detail in [`CONTRIBUTING.md`](./CONTRIBUTING.md).

## Compatibility

The money hook is an additive Harmony patch and goods delivery uses public game APIs, so it's built to coexist with transfer and cargo mods like TM:PE and MoreEffectiveTransfer. If you hit a conflict, please [open an issue](../../issues).

## FAQ

**Do I need a server?** Not for solo play. For a league you do, but the free public server is the default, or you can self-host in one Docker command.

**Is my save safe?** Yes. Everything is stored in the mod's own save section, and removing the mod leaves your city loading normally.

**Does it work with other mods?** It's an additive Harmony patch and uses public game APIs, so it's built to coexist with mods like TM:PE and MoreEffectiveTransfer. If something conflicts, open an issue.

**Can someone cheat the market?** There's no easy way. Pricing and settlement happen server-side and are clamped, and every league's money is conserved and audited, so a modified client can't fake prices or mint §.

**What if the server goes down?** Everything falls back to solo (stable prices) and your saves keep loading. The online layer is best-effort and never required.

**Does it work with Cities: Skylines II?** No, this is CS1 only.

**Do I need any DLC?** No. The Industries DLC only adds physical-goods delivery into `[trade]` warehouses; everything else works without it.

## How this was built

Full disclosure: Open Markets was built with heavy use of AI coding tools — mainly [Claude Code](https://www.anthropic.com/claude-code) and Codex. They generated and reviewed a large share of the mod code, the Go backend, the deployment setup, and the documentation (including this README), under human direction, testing, and design decisions throughout. Noting it here for transparency.

## Credits and license

Built on the work of the CS1 modding community: [CitiesHarmony](https://github.com/boformer/CitiesHarmony), [MoreEffectiveTransfer](https://github.com/pcfantasy/MoreEffectiveTransfer), [EnhancedOutsideConnectionsView](https://github.com/rcav8tr/CS1Mod-EnhancedOutsideConnectionsView), and [AdvancedOutsideConnection](https://github.com/DNKpp/CitiesSkylines_AdvancedOutsideConnection).

Released under the [MIT](./LICENSE) license.
