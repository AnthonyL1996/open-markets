# Open Markets — Cities: Skylines 1 Trading Mod

Working title: **Open Markets** (alts: Cargo Exchange, Free Trade)

A code mod (C#/Harmony) that turns CS1's anonymous outside-world import/export into a
living commodity market with named partner cities and dynamic prices.

---

## Target platform constraints (fixed)

- **Engine:** Unity 5.6.6f2 (permanently frozen by Colossal Order — stable ABI).
- **Runtime:** Mono / **.NET Framework 3.5**. Compile target = `net35`.
- **Language:** C# 3.0 + partial 4.0. No `async/await`, no tuples, no string interpolation.
- **Patching:** Harmony 2.x **only**, via the `CitiesHarmony.API` NuGet package + the
  `HarmonyHelper.DoOnHarmonyReady(...)` bootstrapper. Never detour. Never ship `CitiesHarmony.Harmony.dll`.
- **Mod entry:** `IUserMod` + `LoadingExtensionBase` (activate on city load) +
  `ThreadingExtensionBase` (tick the price sim) + `ISerializableDataExtension` (persist into save).
- **UI:** `ColossalFramework.UI` (not Unity uGUI).

---

## Locked design decisions

| Axis | Decision |
|------|----------|
| Trade partners | **Fictional AI partner cities** |
| Partner ↔ city | **Bound to outside connections** — each highway/rail/harbor/airport node IS a partner. Geography = strategy; trade attribution falls out of which connection the cargo used. Player can rename. |
| Goods integration | **Reuse the flow; mod owns the money.** Vanilla keeps physically moving goods through connections. ⚠️ *Revised after the decompile spike:* base-game CS1 books **no** per-trade income (only building taxation; only the Industries DLC chain has a per-unit price), so there's no "flat income" to reprice — the mod **books the trade money itself** at delivery and neutralizes the Industries `GetResourcePrice` to avoid double-counting. |
| Trade traffic | **Market drives real cargo.** Not a passive money overlay: high price → warehouses export (public `SetEmptying()`), low price → import (`SetFilling()`), spawning real vehicles via vanilla's offer matcher. Base-game fallback: inject `TransferManager` offers on the connection building. |
| Market | **Open market, dynamic prices.** |
| Price drivers | Exogenous (partner profiles + bounded random walk + events) **+ elasticity** (your own volume moves the price). |
| Player agency | **Trade controls** (per-resource/per-partner embargo / throttle / prioritize) **+ stockpile & speculate** (buy low → warehouse → auto-sell high). |
| Resources | **Real CS1 `TransferManager.TransferReason` types** (oil, ore, coal, petrol, goods, food, lumber, logs, grain; + luxury products w/ Industries). |
| DLC | **Detect & support base + Industries gracefully.** Warehouse stockpiling only when Industries present. |
| Online | **Local-first.** Fully simulated offline market is the foundation. Online is an optional later layer that only *nudges* prices. Must degrade gracefully when offline/unsubscribed. |
| Online model | **Both, phased:** (1) crowd-sourced global price index from real players' aggregated supply/demand; (2) later, a money-only speculation bid/ask book settling in in-game cash. |

---

## Core mechanic — the trade loop

1. Market state (per resource × partner) makes a commodity attractive to export or import right now.
2. The mod nudges **real cargo** to move: it flips Industries warehouses to Empty (export) / Fill (import)
   via the public `SetFilling`/`SetEmptying` API, and vanilla's `TransferManager` matcher spawns the actual
   trucks/trains/ships to/from the connection (no patch). Base-game fallback: inject offers on the connection.
3. When a cargo delivery touches an outside connection, the mod attributes
   `(resource, amount, direction, connection→partner)` at `CargoTruckAI.ArriveAtTarget` and **books the money
   itself** — `EconomyManager.AddResource(ResourcePrice, amount × marketPrice)` (export) / `FetchResource` (import).
   Import charging is **on by default** (trade is two-sided; net = exports − imports) and is **mandatory under
   online play**. Embargoed routes book §0.
4. Prices drift over time (partner profiles + random walk + events + elasticity), so timing & routing matter.

### ⚠️ De-risk spike — DONE (2026-06), and it changed the design
The ILSpy spike (`ilspycmd` on `Assembly-CSharp.dll`) found that **vanilla does *not* book per-trade
outside-connection income** in base game — `CargoTruckAI.ArriveAtTarget` / `OutsideConnectionAI` /
`WarehouseAI` move goods and stats but no money; city income from trade is really building **taxation**
(`EconomyManager.Resource.PrivateIncome`). The *only* per-unit trade income is the **Industries DLC**
chain (`ProcessingFacilityAI` → `ResourcePrice` via `IndustryBuildingAI.GetResourcePrice`).

So there's no single chokepoint to *reprice* — the mod **owns** the money (books it at the `ArriveAtTarget`
attribution point) and neutralizes the Industries `GetResourcePrice` to avoid a double-count. Traffic is
steered through `TransferManager` offers (public warehouse API; offer injection as fallback). See the
`OpenMarkets/Patches/` and `OpenMarkets/Trade/` source for the confirmed API usage.

---

## Price engine

- Per `(resource, partner)`: `basePrice`, `currentPrice`, short price history (for sparklines).
- Movement: partner supply/demand profile + bounded random walk + discrete events
  ("refinery fire in Northport → petrol demand spikes").
- **Elasticity:** large net volume from your city pushes that partner's price against you
  (dump oil → price craters). Turns "always sell everything" into a real decision.
- **Design the price source behind a `IPriceSource` seam** so a remote feed can replace/augment the
  local sim later without rearchitecting.

---

## Roadmap

**v0.1 — spike + skeleton** ✅ DONE (verified in-game)
- ILSpy spike to confirm the income chokepoint.
- Mod skeleton: `IUserMod` + CitiesHarmony bootstrapper + net35 csproj + post-build copy to
  `%LOCALAPPDATA%\Colossal Order\Cities_Skylines\Addons\Mods\OpenMarkets\`.
- Partners auto-bound to outside connections (all connections, not just 3).
- Per-resource bounded random-walk prices; mod-owned money booking (not a "repricing patch" — see spike).
- Read-only → interactive market panel (`ColossalFramework.UI`).
- Persist prices + partners via `ISerializableDataExtension`.

**v1 — the game** ✅ feature-complete in code (in-game verify of the latest batch outstanding)
- Trade controls: **embargo** done (financial + driver-coupling); throttle / prioritize TODO.
- Price-swing events. ✅
- Price-history sparklines. ✅ *(block-glyph; font support is an in-game risk)*
- Volume elasticity. ✅
- DLC detection (base vs Industries) — commodity tiers + driver gate ✅; conditional registration TODO.
- The physical Commodity Exchange building was **abandoned** (runtime prefab-clone too brittle) → market is **ambient**.

**v2 — depth** ◐ slice started
- Warehouse stockpile / speculation (Industries). ☐ next
- Chirper notifications on price-swing events. ✅ *(save-safe via `MessageSerializePatch`)*
- Per-partner balance (export/import cents, dashboard tooltips). ✅ Full balance-of-trade report TODO.

**v3 — online (optional, phased)** — *import charging becomes mandatory here (force-on at `IsChargeImports`)*
- Crowd-sourced global price index: cities report anonymized supply/demand; server aggregates;
  client polls REST and nudges local prices. Graceful offline fallback.
- (Later) money-only speculation bid/ask book settling in in-game cash, with rate-limiting/anti-abuse.

### Online technical notes (when we get there)
- REST polling only (no WebSockets on .NET 3.5 Mono). Network off the sim thread; marshal results back.
- **TLS 1.2 on Unity 5.6 Mono is a known pain** — plan the endpoint/transport around it.
- Live service = hosting cost + auth + anti-abuse + moderation. The market is **not** cheat-proof
  (single-player, fully moddable, no server-authoritative state) — aggregation/anonymization is the
  defense, not enforcement. Never hard-depend on the server.

---

## Key references
- Skeleton to clone: boformer CitiesHarmony example gist
  (https://gist.github.com/boformer/fa0ad18e843452d1e30448c4cdcf8e27)
- cslmodding.info — canonical technical reference
- skylines-modding-docs.readthedocs.io + skylines.paradoxwikis.com/Modding_API
- github.com/CitiesSkylinesMods/TMPE — best-maintained large mod to study patterns
- harmony.pardeike.net — patching docs
- Test against: SKYVE + Loading Screen Mod Revisited (what your users run)
