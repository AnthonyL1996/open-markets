# Changelog

## [Unreleased] — Great Works Rewards V2 (2026-06-28)
_Merged to `main` (`d9f737d`). Go gate green (build/vet/test) + Postgres conformance passing against embedded
PG; conservation (`AuditLeague==0`) and C#↔Go DTO parity verified by review. The new reward UI still owes an
in-game pass (the net35 client can't compile on the Mac)._

- **Phase 2 — contribution-proportional project buff.** Each builder's buff magnitude scales by their share of
  total contribution (`scale = FLOOR + (1-FLOOR)·mine/maxBy`, FLOOR=1/2, integer math), applied before
  `InvestBuffMagnitude` so demand + attractiveness both scale and still cap. Top contributor gets the full
  advertised buff; duration is unchanged. Identical math in the in-memory and Postgres stores via shared
  exported helpers; client `ProjectsTab` shows a "projected reward" readout.
- **Phase 3 — prestige / legacy.** Richer completion chronicle naming builder count + top builder;
  `CompletedProjectCountsByBuilder` (both stores + conformance) feeding a new **"Master Builder"** leaderboard
  board.
- **Phase 4 — trade-economy rewards.** `marketShield` (conservation-neutral): a shielded builder's net-supply
  moves the shared league price index less for the project's themed commodities (`CommodityIndicesWithShields`).
  Additive `Effect` fields `Commodity`/`TradePctBips` + kinds `marketShield`/`priceEdge`, per-template
  `TradeReward`, migration `0004_trade_rewards.sql`. **`priceEdge` is now implemented** (commit `6a4e31c`): the
  two commercial Works grant it (Exchange +8% / Grand Refinery +10%) and the client books a higher §/truck on
  themed EXPORTS via a conservation-safe `BookExport` multiplier (void income only; import + peer settlement
  untouched). Go-verified + Codex-reviewed; the C# booking still owes the in-game pass.
- **Repo housekeeping.** Deleted six stale local branches (all already merged to `main`); `main` is the only
  branch. Added contributor agent guidance and gitignored machine-local tool config.

## [Unreleased] — M6–M9: inventory, city levers, market rework + online polish (2026-06-19)
_All merged to `main` (no feature branch). Client builds `0/0`; the Go suite (incl. the conservation/due-clock
sim harness) is green. The whole M5→M9 client money/price path is code-, agent-, and test-verified but still
owes one in-game pass (`docs/IN-GAME-TEST-CHECKLIST.md`)._

### M9 — market rework (single server-owned index)
- **One per-commodity league price index**, server-owned: `clamp(1 - sumNetSupply/VolumeRef, 0.5, 2.0)` (the
  per-league elasticity from members' daily net-supply reports) **× a GLOBAL price-event multiplier** (a
  server-side random ±25–50% spike/slump shared across all leagues), re-clamped. **Solo play = static base
  prices** (`MarketFeed`, index 1.0). Sparklines are **server-served**; prices **freeze at last** when the
  server is unreachable and snap back to base when offline.
- **Removed:** partner cities, embargo, bid/ask spread, auto-trade, the warehouse traffic-driver, and the
  client-side price model. `RemotePriceSource`/`SpeculationWatch` deleted; `LocalPriceSim` reduced to the
  net-supply report accumulator. Options lost the online-price toggle + volatility/elasticity sliders.
- **Wall-clock settlement:** bonds/contracts/trades settle on the server's due-clock (`OM_DUE_INTERVAL_SEC`,
  default **2700s** ≈ one in-game day; `run-local.ps1` sets 120 for testing). The client learns the period via
  `/citystate` and auto-settles off its real-time poll loop instead of the in-game-day rollover, so pause/sim-speed
  no longer drift it; misses + garnishment stay server-driven (anti-dodge).

### Online polish
- **Leaguemate city profiles** — `/cityprofile` reports population, popularity (happiness), attractiveness,
  treasury + weekly income/expenses, and the industry/zone breakdown (gathered from the game's `District(0)`
  aggregate); `/leagues/members` flattens it in alongside each member's **online status + last-seen**. Shown in
  the in-game **Members** tab and the operator console.
- **Targeted, §-scaled co-op investment** — the investor picks which demand the boost targets (**Residential /
  Commercial / Industry & Office** — CS1 combines industrial+office) and the magnitude scales with the § invested
  (cap +20 at §50k); the § still transfers to the grantee (conserved). The **recipient sees it**: a Members-tab
  "Investments in your city" list (who/how much/which demand/days left) and a Chirp naming the §.
- **Total deal value on every trade offer** — the basket composer shows "Deal value §X · net to you ±§Y", and the
  Inbox now shows each incoming offer's gross + net (valued indicatively off the live index, since an unaccepted
  offer isn't frozen yet).

### Fixes
- **League index was stuck at 1.00 / trade baskets posted empty** — Unity 5.6 `JsonUtility` drops an object-array
  field mixed with scalars **outbound** too (not just inbound). The `/report/batch` and `/trades` POST bodies are
  now hand-serialized so `reports[]` / `items[]` survive.
- **`OutgoingMail` (and other non-commodities) booked as trade income** — cargo attribution now gates on
  `Commodities.IsTradable`, so only real market commodities are priced + counted toward the index.
- **`/investment-office` + `/bailout` returned 400 "missing league"** — `authMember` reads `?league=` from the
  query, but the clients sent it only in the body. Both now append `?league=`.

### M6–M8 (shipped earlier in the arc)
- **M6** — `[trade]`-tagged inventory & physical delivery of goods on settled trades (`WarehouseAI.ModifyMaterialBuffer`,
  overflow spill), and a delivery→demand stimulus.
- **M7** — league transparency (per-member standing visible to all) and the austerity **tax-lock**.
- **M8 city levers** — austerity **demand slump** + **budget cap** (self-consequence), and the cross-player co-op
  **investment office** + **bailout** (both conserve cash; server-authored, due-clock-expired).

## [Unreleased] — M5: trade basket + bonds + austerity (2026-06-16)
_Branch `m5-trade-bonds` (off `m4-phase-a-client`). Design + server are **built and tested on the dev Mac**
(`go test -race` green across all packages); the C# changes (`Money.cs` mirror) are **not yet compiled on
Windows**. Client UI/wiring for the basket is **not started**. The CS1 tax-rate API for austerity is **unverified
(GATE-B)** — needs a Windows `/decompile`._

### Final pre-merge review (2026-06-17)
- A holistic **Codex** review of the whole branch diff (server Go, runtime-authoritative side) before merging
  to `main` found **2 HIGH money-safety bugs**, both fixed + regression-tested, then re-verified clean by Codex:
  - **Loan principal could exceed the bookable ceiling** — `ValidateBondTerms` only capped the per-installment
    slice, but `AcceptLoan` transfers the whole principal as one settlement event. Now rejects
    `principalCents > MaxBookableCents` (covers offer/counter/accept; auto-bonds unaffected).
  - **Trade net could overflow int64** — gold lines were never value-capped, so many large same-direction gold
    lines could wrap `OffererNetCents` (and the `MaxBookableCents` guard then ran on the wrapped value). Each
    line value is now capped ≤ `MaxBookableCents` (gold at offer, commodity at freeze), keeping the ≤20-line
    sum far below int64 max.

### Verification — simulation/property harness + `/audit` (2026-06-17)
- **`store.AuditLeague` + `GET /audit`** (member-only) — re-derives per-account net online cash from the
  settlement-event log plus the conservation total. A live invariant / sanity check.
- **`internal/sim`** — a seeded, deterministic random-economy harness (`Run(Params) Result`, no assertions) that
  drives the store + due-clock through random trades/loans/settles/misses/defaults/garnishment, drains to steady
  state, and reports the system invariants: cash **conservation**, austerity **escapability** (no stuck-defaulted
  bond, no stuck-austerity city after the drain), no stranded active trades, no settled-count overflow, full
  drain. Plus a **coverage guard** (peak defaulted-bonds / austerity-cities observed mid-run) so a green result
  can't be vacuous — the test asserts the default + austerity paths were actually entered.
- **`internal/sim/sim_test.go`** — property tests over many seeds + a larger run + a non-vacuity test; green
  under `-race`. **`cmd/omsim`** — runnable stress command (`-members/-rounds/-seed`, `-sweep N`), exits
  non-zero on any violation.
- **Codex-verified finding:** `AuditLeague`'s `total` is structurally always 0 (every event is `+cents`
  receiver / `−cents` payer), so `total == 0` is tautological. The real conservation signal is the per-account
  `net` map — the sim flags any **void (`""`) or non-member key appearing at all** (presence, not just non-zero
  net, so an event masked by netting-to-zero can't hide). Suite now 91 tests, race-clean.
- **Money-math fuzzing** — `internal/money/fuzz_test.go`: native Go fuzz targets for `Amortize`,
  `TotalDueCents`, `LineValueCents`, `ValidateBondTerms`, checked against `math/big` oracles (exact-sum split,
  no zero/negative installment, validated terms always schedulable, correct half-up rounding at any magnitude;
  conservative overflow guards tolerated but never a wrong value). ~3M execs/target, no crashers.
- **CI gate (Go side)** — `server/Makefile` `make verify` (fmt-check + vet + race suite + `omsim -sweep 200` +
  money-math fuzz) and `.gitea/workflows/ci.yml` running it on push/PR to `server/**`. The net35 C# client
  stays out of CI (needs the 4 proprietary game Managed DLLs). Closes BEYOND-M5 §1 (verification depth).

### Design & verification
- **`TRADE-SCREEN.md`** — Civ5-style two-sided cash-settled trade basket + a bond credit subsystem (lending +
  always-bond-the-shortfall backstop, negotiable interest with a league floor) + an **austerity** insolvency
  penalty (forced/frozen taxes + garnishment with a guaranteed escapability invariant). Records the owner's
  locked decisions, a full **Codex** design review with resolutions, and a canonical bond state machine.
- **`PLAN-trade-bonds.md`** — phased build plan with GATE-A (money math) / GATE-B (tax-API decompile) gates.

### GATE-A — integer-cents money math (Go + net35 C# mirror), cross-verified
- `server/internal/money` — `TotalDueCents` (flat interest in basis points), `Amortize` (exact-sum
  quotient+remainder split), `LineValueCents` (fixed-point qty), `MaxInstallments` cap. 13 tests under `-race`.
- `OpenMarkets/Market/Money.cs` — net35-safe mirror, verified **17/17** against the shared `vectors.json`.

### Phase 2a — server side COMPLETE
- **Domain model + state machine** (`store/trade.go`, `store/bond.go`) — Trade/LineItem/Bond/SettlementEvent;
  freeze-at-accept valuation; the per-line cash-flow-direction rule (commodity vs gold flow opposite ways);
  bond state machine with the frozen-at-default escapability invariant.
- **Settle loop + store wiring** (`store/tradestore.go`) — atomic accept, gross-evaluate/net-book
  `SettleTradeInstallment` (→ server-authored `SettlementEvent`), `MissTradeInstallment` (auto-bond the
  shortfall), bond settle/miss, `SettlementsSince`; harsh `EconParams`; append-only save-safe snapshot.
- **Pricer** (`internal/pricing`) — server base-price table × league index → frozen unit price (server-authoritative).
- **Due-clock ticker** (`internal/duecycle`) — scheduled sweep auto-bonds overdue installments and drives bonds
  to default past a one-interval grace window; offline cities still accrue. Configurable cadence.
- **HTTP** (`api/trades.go`) — `POST /trades`, `GET /trades`, `/trades/{id}/{accept|decline|cancel|settle}`,
  `GET /bonds`, `POST /bonds/{id}/settle`, `GET /settlements?since=`. Integration-tested end to end.

### Earlier this session (in-game UI polish, online config)
- Surface server rejection reasons in the offer/inbox tabs; offer-composer cost preview + in-flight lock.
- Default endpoint → `http://localhost:8080`; Options shows the resolved "server in effect".

### Phase 2a — client side COMPLETE
- DTOs + `OmApi` (CreateTrade/GetTrades/TradeTransition/SettleTrade/GetBonds/SettleBond/GetSettlements);
  `SettlementReconciler` + `SettlementLedger` book the `/settlements` feed to in-game cash idempotently
  (sim-thread; SaveData §7 v3, save-safe). `TradeTab` two-pane composer. Inbox gained "Incoming trades" +
  "Due trade installments"; new `BondsTab` (you-owe / owed-to-you + repay). Day-rollover auto-settle sweeps
  trades (net-payer only) and bonds. `Money`/`BasketValuation`/`TradeMath` are Mac-typechecked + unit-verified;
  cs1-modder reviewed the UI (one compile-blocker fixed).

### Phase 3 — server austerity engine (GATE-B-independent half)
- **Codex full-branch verification** fixed a HIGH settlement cursor-jump money-loss bug (isolated event booking
  could jump the per-league cursor past unbooked events; now only the contiguous `/settlements` poll books) and
  a MEDIUM int32 booking-cap mismatch (server now caps installments to `MaxBookableCents`).
- **Austerity engine**: a defaulted bond freezes its garnishable balance; the due-clock garnishes it by an
  income-independent minimum write-down each tick (→ **provably escapable**, with a timebox write-off backstop);
  `GET /citystate`; client `CityStateDto` + a BondsTab AUSTERITY banner. Go suite green (race).

### Phase 3 — manual bond lending + reputation + negotiation UI
- Negotiated peer loans (both directions, in-thread counter-offers): server `store/loan.go` + `api/loans.go`,
  client `OmApi` + the `BondsTab` negotiation UI (issue form doubling as the counter editor; accept/counter/
  decline/cancel). Reputation (on-time vs missed → reliability score) in the roster. Codex-validated (fixed a
  reputation double-count + dust-skew); cs1-modder-reviewed the UI.

### Risk audit + hardening
- Three parallel risk scans (security / Go concurrency-resource / economic-save-safety) + multiple Codex/
  cs1-modder passes. No CRITICAL in normal operation. Fixed: trade item cap (overflow + amplification),
  austerity escapability clamp, loan interest cap, `io.LimitReader` body guard, `?since=` 400, persist-error
  logging. Then owner-selected hardening: caller-scoped settlement feed (privacy), due-clock backlog cap,
  bounded offline auto-bond grace.
- **Server-reset recovery (§D) closed via a data EPOCH.** A first attempt (`latestSeq < cursor`) was
  Codex-rejected as unsafe (false-positive → double-book) and reverted. The correct fix: a server data-tied epoch
  (persisted in the snapshot, stable across restarts, new on a wipe) → the client resets cursors + replays the
  fresh economy only on a genuine wipe. Codex-verified after fixing two gaps (immediate migration persist;
  epoch-guarded sim-thread bookings). Residual: partial restore-to-older-snapshot = a degraded stall, never a
  double-book (documented). Go suite 87 green under `-race`.

### Build: macOS/Linux fully supported (no Windows needed)
- Confirmed the net35 client **compiles on macOS** with just the 4 game `Managed` DLLs (net35 + Harmony come
  from NuGet). README *Build from source* updated with the exact steps. Only in-game visual/render + live-economy
  checks still require the running game.

### Next
The **client tax-LOCK** needs **GATE-B** (`GATE-B-DECOMPILE.md`, Windows decompile of `EconomyManager`). With
the 4 game DLLs in place, the rest of the net35 client can be compiled + verified on macOS.

## [Unreleased] — M4 Phase A: backend service + client config seam + cleanups (2026-06-15)
_Branch `m4-phase-a-backend` (off `chore/price-sim-cleanup`, off `m4-online-foundation`). The Go backend is
**built, tested, and run on the dev Mac**; the C# changes are **not yet compiled** (Mac has no game DLLs) and
need the Windows build alongside the still-owed NFR-3 removal test._

### Backend service (`/server`) — NEW, fully verified on Mac
- **`openmarketsd`**: the Phase A shared price-index service. **Go, standard library only** (no external
  modules → builds/tests offline). `go test -race`: **28 tests pass**; coverage 100% market / 91% id /
  84% store / 70% api. Live smoke test passed end-to-end (account → league → report → index → 401).
- Endpoints: `/health`, `/eol`, `POST /accounts` (identity + one-time secret), `POST /leagues` +
  `/leagues/join` (friend-group leagues by share code), `POST /report` (net supply/demand), `GET /index`
  (legacy single-float `PriceFeedDto` the shipping client parses) and `GET /prices` (per-commodity).
- **Friend-group aggregation** (the decided scope): `index = clamp(1 − Σ netSupply / VolumeRef, 0.5, 2.0)`,
  matching the client's scale/clamp. Auth via `Bearer <id>.<secret>` or `?account=&secret=` query.
- `store.Store` interface (in-memory + atomic JSON now; Postgres swap later per BACKEND.md). Dockerfile
  (distroless, non-root), docker-compose, Makefile, README, `.env.example`.

### Contracts + operator CLI (NEW — bilateral contract primitive; verified on Mac)
- **Bilateral cash-settled contracts**: seller delivers/gets paid, buyer pays, over N installments
  (1 = one-shot future; N>1 = recurring supply deal). Lifecycle `offered → active → completed`
  (+ `declined`/`cancelled`). **CS1 can't force physical cargo**, so a contract is an accounting overlay:
  each party settles its own leg in in-game cash; the server is ledger + consent only (never moves money;
  an unsettled contract must never block a save load — risk is reputational). Endpoints `POST /contracts`,
  `GET /contracts`, `POST /contracts/{id}/accept|decline|cancel|settle`.
- **`omctl` operator CLI** — run a *simulated city* by hand (account → join league → offer/accept/settle).
  The manual stand-in for AI counterparties (human-only by design); also a seed/test tool.
- Verified: store + API contract tests pass; live `omctl` demo drove a recurring contract A↔B end-to-end
  (offer → accept → 3 installments each leg → completed; over-settle correctly rejected 409).

### Client (C#) — Windows-build-pending
- **Configurable feed endpoint** (`Settings.Endpoint` + `EndpointValue`, editable in Options); wired into
  `RemotePriceSource` construction. Whitespace falls back to the placeholder (Codex-caught).
- **`OnlineMode.IsActive`** flag (set by the lifecycle when the feed is wired) — implements the long-standing
  **ChargeImports force-on-when-online** TODO: imports are charged regardless of the toggle while online.
- **SpeculationWatch** now evaluates through `PricingService.ActiveSource` (nudged prices), not the raw local
  snapshot — latent bug fixed before any live feed.
- Cleanups: `LocalPriceSim.EnsureEntry` (dedupe), consolidated tuning constants. Codex-reviewed (all pass).

## [Unreleased] — M4 foundation: transport + TLS gate + UI terminal refactor (2026-06-14) — builds clean + in-game verified (save-removal pending)
_Branch `m4-online-foundation` (off `m3-spread-speculation-online`); pushed to Gitea. Reviewed by Claude + Codex
across several passes. **First Windows compile (2026-06-14) FAILED** — the UI terminal refactor used `UIView`
like a `UIComponent` (`view.components`; generic `view.AddUIComponent<T>()`), which don't exist on `UIView`.
Fixed (`GetComponentsInChildren<UIComponent>()`; `(T)view.AddUIComponent(typeof(T))`, matching the pre-refactor
idiom, confirmed against the `ColossalManaged` decompile). **Now builds clean (0/0) on Windows.**_

### In-game verification (2026-06-14, ~5 in-game months on a 13-connection city)
- **TLS smoke test: 3/3 HTTPS endpoints reachable — TLS 1.2 WORKS** (run 4×, deterministic). The M4 GO/NO-GO
  gate is **GREEN** → online transport is viable; M4 Phase A (Proxmox backend, real endpoint) is unblocked.
- **UI terminal refactor:** one corner button, tabbed Market/Balance window, tab switching, embargo cell-clicks
  (`Embargo ON/OFF`), and disable→re-enable idempotency (`Terminal destroyed`→`created`, no duplicate buttons)
  all confirmed. Assembly hot-reloads cleanly on enable/disable.
- **Bid/ask spread:** verified *quantitatively* from per-trade logs — exports book sell-side (idx×(1−hs)),
  imports buy-side (idx×(1+hs)); math exact at 8% (e.g. Goods idx 915 → §526 export / idx 861 → §537 import).
- **Speculation:** BUY *and* SELL signals fire on crossings (no daily re-spam); markers publish to the board.
- **Online groundwork:** feed starts/stops on the `OnlinePrices` toggle and on mod-disable; exponential back-off
  confirmed growing 60→120→240→**480**s toward the 900s cap against the unreachable placeholder; silent fallback.
- **Economy regression:** daily ticks, price-swing event full lifecycle (start→realize→clamp→expire→revert),
  traffic driver (1–2 warehouses/day), elasticity, multi-modal cargo (road + sea mixed consists) all clean.
- **Save round-trip (with mod):** `Saved`/`Loaded market state` round-trips across a full restart.
- **STILL PENDING:** the mod-**removal** save-safety check (save → disable mod → reload loads clean, NFR-3),
  and three eyes-only visuals (sparkline glyphs render vs boxes; `·BUY/·SELL` marker fit; Balance TOTALS == title net).

### Economy fixes from the in-game session (2026-06-14)
- **Price-swing events now realize their headline %** (`LocalPriceSim`): events pull toward their spiked/crashed
  target at `EventReversion = 0.50`/day (vs the ordinary `MeanReversion = 0.10`). Previously a −46% event only
  ever reached ~−21% within its 4–8 day window, so it never crossed the speculation BUY threshold — BUY signals
  were effectively unreachable from events. Now a swing realizes its % in ~2 days. The pull only ever closes half
  the gap to target, so it never overshoots; it composes with (does not fight) elasticity and the [0.4×,2.5×] clamp.
- **Events only fire on traded commodities** (`LocalPriceSim.MaybeStartEvent`): selection is now restricted to
  commodities with ≥1 live route in `_price`. An event on an untraded commodity moved no price (`TickDay` walks
  `_price` only) and never reached the board or speculation — a cosmetic headline affecting nothing.
- **SaveData version guard hardened** (`SaveData.OnLoadData`): now bails to clean defaults on `version > Version`
  too (not just `< 1`), so a future blob can't be mis-parsed as v1 — forward-compat for M4 save evolution.

- **Transport rewrite (`Market/RemotePriceSource.cs`):** replaced the dormant worker-thread `HttpWebRequest`
  poller (which can't negotiate TLS 1.2 on net35/Mono) with a **main-thread `UnityWebRequest` coroutine**
  (`RemoteFeedRunner : MonoBehaviour`) — the only reliable HTTPS path (OS TLS stack). Adds exponential
  back-off (60s→15min), pause-proof `realtimeSinceStartup` waits, and in-flight request tracking aborted on
  teardown. `IPriceSource` contract / ctors / `Start`/`Stop` / the volatile-float hot path unchanged →
  `ModLifecycle` untouched. **Supersedes the "M4 online groundwork" bullet below** (different transport).
- **TLS smoke test — the M4 GO/NO-GO gate (`Net/TlsSmokeTest.cs`):** a "Run TLS smoke test" Settings button
  (new "diagnostics" group) fires a main-thread `UnityWebRequest` GET at 3 HTTPS hosts (self-managed 10s
  timeout) and logs the verdict (`TLS 1.2 WORKS`/`FAILED`) under `[OpenMarkets]`. Must pass on a real build
  before any online (Phase A) code is trusted.
- **UI terminal refactor (behavior-preserving, no online dependency) — `UI/Terminal/`:** collapsed the two
  standalone panels into ONE tabbed **"Open Markets"** terminal: `ITabBody` (tab contract), `MarketTerminal`
  (shell: one corner button, draggable window, manual tab bar, drag/close/Refresh/title), `MarketTab`,
  `BalanceTab`, `UiKit` (shared palette + `Cellate`). Deleted `UI/MarketPanel.cs` + `UI/BalancePanel.cs`;
  `ModLifecycle` now creates/destroys the terminal. Behavior identical to the old panels. Spec:
  `docs/REFACTOR-terminal-shell.md`. Teaching comments added in-code (main-thread-only, idempotency-not-by-name,
  snapshot-before-`RemoveUIComponent`, loop-local closure capture).
- **Decisions captured (design docs, no code):** backend = **self-hosted on Proxmox** (LXC + Docker Compose,
  Cloudflare Tunnel ingress, internal-only Postgres — `BACKEND.md`); **City Profile** (name/age/stats + moderated
  pinned tagline, cosmetic v1 — the online layer design); **UI/UX** hybrid tabbed-terminal IA, CS1-native, async via
  return-digest + actionable-inbox (`UI-UX.md`); **Crisis engine** (fictional world, curated bank first, LLM
  later — Appendix B); **Candidate mechanics** (loans/futures/insurance/bonds via one "agreement" primitive,
  cartels, tariffs, leaderboards — Appendix A); **City Levers beyond price** (RCI demand/taxes/budget/efficiency/
  happiness/tourists; all four roles; hard handicaps + 7 guardrails — Appendix C).
- **Public release prep:** `README.md` rewritten player-first; `docs/WORKSHOP.md` (Steam description, online
  features under "Coming soon"); `LICENSE` (MIT).
- **Reviews:** Claude self-review + multiple Codex passes. Codex caught + fixed: a build-breaking
  `yield`-in-`catch` in `TlsSmokeTest`; a `UnityWebRequest` leak on feed teardown; and a UI idempotency bug
  (idempotency keyed on `FindUIComponent`-by-name could leave the terminal un-built after a recompile — now
  guards on own built-state + sweeps stale components). All Codex-verified.

## [Unreleased] — Spread + speculation + balance + M4 groundwork (2026-06) — code-complete, in-game verify pending
- **Market bid/ask spread (economy):** `PricingService` now applies a half-spread at the booking sites — the
  city sells exports below the index and buys imports above it (partner takes a market-maker margin both
  ways). Tunable `SpreadPct` slider (default 8%, range 0–30; 0% = prior behavior exactly). Profit now comes
  from value-add chains + timing. Addresses the net-importer-bleed observation from the 2026-06-10 playtest.
- **Stockpile speculation — manual + alerts (`Market/SpeculationWatch.cs`):** the daily tick flags commodities
  that are a good BUY (avg price ≤ base×0.75) or SELL (≥ base×1.25); posts a one-shot Chirper alert on each
  *crossing* (anti-spam) and marks the commodity on the dashboard (`·BUY`/`·SELL`). Opt-in `SpeculationAlerts`
  (default on). Player flips warehouses by hand — no automation, no new persistence.
- **Balance-of-trade report (`UI/BalancePanel.cs`):** a standalone "Balance" panel listing each partner's
  lifetime exported/imported/net § (sorted best-net-first) + a TOTALS row matching lifetime net. Reads the
  existing per-partner §5 ledger; no save change.
- **M4 online groundwork (`Market/RemotePriceSource.cs`):** a DORMANT decorator behind the `IPriceSource`
  seam — when `OnlinePrices` is enabled (default **off**) a background thread polls a REST endpoint for a
  global price-index nudge (HttpWebRequest, TLS 1.2 via `(SecurityProtocolType)3072`, `JsonUtility` DTO,
  `volatile` publish), clamped 0.5×–2.0×, with silent offline fallback. No live server (placeholder
  `.invalid` endpoint). Off by default = no thread, no socket, no network. Stop-before-restart on lifecycle
  re-fire so no worker leaks.
- **Reviews:** 1 decompile/feasibility analysis (net35 HTTP/TLS/JSON/threading) + 4 sequential build agents
  (contract-isolated) + 1 independent code review. Review fixed: stop the prior `RemotePriceSource` before
  re-wiring on `OnLevelLoaded`. Build clean (0/0) throughout. Branch `m3-spread-speculation-online`.
- **Risk audit (4 parallel agents: concurrency/lifecycle, save-safety, economy/math, perf/compat) + hardening.**
  Save-safety confirmed fully intact. Fixed: `PricingService._source` → `volatile` (money-path publish fence);
  online worker now stopped on `OnDisabled`/recompile (`StopOnlineFeed`) and the `OnlinePrices` toggle applies
  live (`WirePriceSource`/`OnOnlineSettingChanged`); speculation BUY/SELL signal now classifies on the
  spread-adjusted price (no longer overstates the edge); BalancePanel adds an "Unattributed" row so TOTALS ties
  out to lifetime net. Independent re-review: 4/4 PASS. Residual (flagged, not fixed): online poll has no
  back-off; speculation reads local (pre-nudge) price; online-forces-ChargeImports is still doc-only; default
  8% spread + import-charge can bleed pure-import cities (left for in-game tuning).

## [Unreleased] — M2 UX fixes + M3 slice (2026-06) — ✅ verified in-game 2026-06-10
- **Embargo↔traffic-driver convergence (HIGH/UX fix):** the driver now skips a commodity embargoed on *every*
  partner and releases any warehouse it had forced back to Balanced, so an "embargoed" good stops being
  driven (no more §0 trucks rolling). Partial embargoes stay driven by design (driver is per-commodity).
  `TradeControls.IsCommodityEmbargoedForAll`, `TrafficDriver`.
- **Embargoed route can't be stranded off the board (HIGH/UX fix):** embargoed partners are now always shown
  as columns (prioritized within the 8-col cap), so a low-activity embargoed route is always reachable to
  un-embargo. `TradeControls.EmbargoedPartnerIds`, `MarketPanel.ActivePartners`.
- **Chirper alerts (M3):** `Notify/MarketChirper.cs` posts a chirp when a price-swing event starts (opt-in
  `ChirperAlerts`, default on). `LocalPriceSim.NewEventsThisTick` feeds the daily tick.
- **Save-safety patch (NFR-3):** `Patches/MessageSerializePatch.cs` prefixes the nested
  `MessageManager.Data.Serialize` to strip our custom `MessageBase` from the queue + recent-ring before the
  **vanilla** save is written — otherwise the type fails to deserialize once the mod is removed. *(Caught by
  the review agent; first fix targeted the wrong method — a second verifier found the serializer lives on the
  nested `Data` type.)*
- **Per-partner stats (M3):** `PricingService` accumulates lifetime export/import cents per partner;
  persisted as **SaveData §5** (append-only, guarded — older saves load clean); shown as tooltips on each
  partner column header.
- **Dashboard sparklines (M3):** ephemeral 12-day price-history ring + `HistorySnapshot` in `LocalPriceSim`;
  rendered as a compact block-glyph trend inline + full sparkline in the cell tooltip (not persisted; warms
  up after load). *In-game risk: CS1's UI font may lack the block glyphs `▁▂▃▄▅▆▇█` — one swappable constant.*
- **Import charging now ON by default** (was off/"vanilla free imports") — trade is two-sided, dashboard
  "net" = exports − imports. **Mandatory under online play (M4):** TODO marker at `Settings.IsChargeImports`
  to force-on while online is active. (Existing saves keep their stored value; only new setups get the new default.)
- **Reviews:** 3-agent feasibility/feature build (sequential, contract-isolated) + independent code review
  (thread-safety / save-safety / net35 / hot-path) + a focused verifier on the save-strip fix. Minor:
  `_newEvents` now cleared in `LocalPriceSim.Clear()`. Build clean (0/0) throughout.

## [Unreleased] — M1 money core (Phase A) — verified in-game, M1 CLOSED
- **In-game verification (2026-06):** mod-owned trade income books on export (daily `+§…` summaries), prices drift via `LocalPriceSim`, and a full game-restart save/reload restored 19 price entries with partners rebinding by building id — 0 errors, no conflict with the installed Transfer Manager CE.
- `Market/IPriceSource.cs` + `Market/LocalPriceSim.cs`: price seam + bounded mean-reverting random walk per (commodity, partner), lazy-seeded at per-commodity base prices; daily `ThreadingExtension` tick advances prices and logs an always-on summary.
- `Market/PricingService.cs`: books mod-owned trade income at the cargo-arrival point — `AddResource(ResourcePrice, …)` on export, `FetchResource(…)` on import (behind a toggle). Long-safe money math.
- `Patches/IndustryPricePatch.cs`: postfixes `IndustryBuildingAI.GetResourcePrice → 0` so the Industries chain stops double-booking (covers both `IndustryBuildingAI` + `ProcessingFacilityAI` callers; inert without the DLC).
- `Persistence/SaveData.cs`: `ISerializableDataExtension` — versioned, guarded, append-only blob (price matrix + partner name overrides); clean-default + mod-removal safe (NFR-3).
- `Settings.cs` + options UI: persisted `ChargeImports` (default off) and `DebugLogging` (default off; per-trade log gated, daily summary always on).
- `CargoArrivePatches.cs`: export branch books income; consist path sums volume **per commodity** and books each at its own price.
- **3-agent review** (correctness / platform+API / tech-debt): fixed an int32 overflow in the money math (now `long`-safe) and a mixed-commodity consist mispricing; hardened the daily tick against double-fire; removed dead `FixedPriceSource`. Verified neutralizer completeness, save-safety, and low conflict risk with TM:PE/MoreEffectiveTransfer. Remaining debt (D1–D5) tracked internally.

## [Unreleased] — M1 attribution layer (M0 closed)
- `Data/Partner.cs` + `Trade/PartnerRegistry.cs`: discover outside-connection buildings on level load, bind each to an auto-named fictional partner city, read-only lookup thereafter.
- `Patches/CargoArrivePatches.cs`: replaces the M0 single-target postfix probe with **prefix** patches across all 4 cargo AIs. ROAD (`CargoTruckAI`) reads `m_transferType`/`m_transferSize` directly; RAIL/SEA/AIR (`CargoTrain/Ship/PlaneAI`) walk the child cargo chain (`m_firstCargo`→`m_nextCargo`) since the parent consist carries no material. Direction from outside-connection-on-target=export / source=import; consist child trucks excluded by the same guard (no double-count).
- **Decompile correction:** only `CargoTruckAI` mutates `m_transferSize`; the consists carry a child *count*, not volume — truck-only patching would have silently dropped all rail/sea/air trades. Verified via two `ilspycmd` agent passes before coding.
- **M0 CLOSED + attribution verified in-game (2026-06):** `Harmony patches applied`, `PartnerRegistry: bound 13 outside connection(s)`, ~237 `Cargo arrive` lines (road both directions + sea consist with correct aggregated amounts), 0 errors, all 4 targets resolved.

## [Unreleased] — M0 scaffold
- Initial SDK-style `net35` project targeting Cities: Skylines 1.
- CitiesHarmony bootstrapper (`Mod` + `Patcher`) following boformer's pattern.
- Cross-platform `Directory.Build.props` (macOS / Windows / Linux) to locate game DLLs and the Mods folder, with auto-deploy after build.
- `OpenMarketsLoading` level-lifecycle hook (logging only for now).
- `CargoTruckArrivePatch`: M0 probe that logs outside-connection cargo deliveries (partner / direction / commodity / amount) to validate the repricing attribution chain.

### Decompile spike (2026-06) — reframed the core mechanic
- **Finding:** base-game CS1 books **no** per-trade outside-connection income (city trade money is building **taxation**); the only per-unit trade income is the **Industries DLC** chain (`ProcessingFacilityAI` → `EconomyManager.Resource.ResourcePrice` via `IndustryBuildingAI.GetResourcePrice`). `CargoTruckAI.ArriveAtTarget`, `OutsideConnectionAI`, and `WarehouseAI` move goods/stats but **no money**.
- **Decision:** the mod now **owns** the trade money (books `ResourcePrice` at the `ArriveAtTarget` attribution point; neutralizes `GetResourcePrice` to avoid double-count) and **drives real cargo traffic** — high price → warehouse `SetEmptying()` (export), low → `SetFilling()` (import), via vanilla's offer matcher (no patch); base-game fallback injects `TransferManager` offers on the connection.
- Confirmed APIs: `EconomyManager.Resource` enum (no `Reason`), `AddResource/FetchResource` signatures, `TransferManager.AddOutgoing/IncomingOffer` + transient-offer/sim-thread constraints, inverted `Building.Flags.IncomingOutgoing`. Docs (PLAN/DESIGN/REQUIREMENTS) updated accordingly.
- Build env: user-local .NET 8 SDK + `ilspycmd`; M0 builds clean (0/0) and auto-deploys on Windows. In-game M0 verification still pending.

See [DESIGN.md](./DESIGN.md) for design rationale, and this changelog for the milestone roadmap (M0–M9).
