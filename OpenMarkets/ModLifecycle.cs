using System;
using System.Collections.Generic;
using ICities;
using OpenMarkets.Data;
using OpenMarkets.Market;
using OpenMarkets.Net;
using OpenMarkets.Notify;
using OpenMarkets.Trade;

namespace OpenMarkets
{
    /// <summary>
    /// Game/level lifecycle hook. On a real gameplay load we bind partner cities and activate the
    /// dynamic price source; on unload we tear that state down so the next city starts clean.
    /// </summary>
    public sealed class OpenMarketsLoading : LoadingExtensionBase
    {
        private static bool _levelLoaded;

        public override void OnLevelLoaded(LoadMode mode)
        {
            base.OnLevelLoaded(mode);
            Log.Info("Level loaded (mode = " + mode + ").");

            if (mode == LoadMode.NewGame || mode == LoadMode.NewGameFromScenario || mode == LoadMode.LoadGame)
            {
                WirePriceSource();
                _levelLoaded = true;

                // CF/Unity UI must be created on the main thread. OnLevelLoaded is safe; OnAfterSimulationTick
                // below is not a place for terminal mutations.
                UI.Terminal.MarketTerminal.Create();
            }
        }

        // M9: the price source is ALWAYS the MarketFeed (server /prices when online, static base prices solo) — there
        // is no toggle and no worker thread/socket. This is the ONE place that owns the source swap + the
        // OnlineMode flag; call it (not WireOnlineServices directly) on any change to IsOnlineConfigured so
        // OnlineMode (→ import-charging + the net-volume report gate) never goes stale. Idempotent; re-fires safely
        // on recompile/level-load.
        public static void WirePriceSource()
        {
            // M9: the MarketFeed is ALWAYS the price source. Solo (no successful /prices poll) → static base prices;
            // online → the server's effective per-commodity index (driven by OnlineSync's /prices poll). The old
            // local price walk + RemotePriceSource nudge are gone; LocalPriceSim survives only for the net-volume
            // report that feeds server elasticity (DrainReportAccumulator).
            PricingService.SetSource(MarketFeed.Instance);
            // "One league city, no hijack": only the install's BOUND city acts online. A different loaded save goes
            // inert (OnlineMode off → no profile/report posts + forced-import-charging off; member polls not started)
            // so it can't overwrite the league city's server state or bleed the shared account's settlements into the
            // wrong treasury. The first configured city to load auto-binds; the player can rebind from Options.
            bool leagueCity = Settings.IsOnlineConfigured && IsThisSaveTheLeagueCity();
            OnlineMode.SetActive(leagueCity); // active = league city → daily report + city profile + forced import-charging
            Log.Info("Price source: MarketFeed (server /prices when online as the league city, static base prices otherwise).");
            // Inbox/roster + their background poll key off the same league-city gate (not the price-feed toggle), so a
            // friend's offer shows up even with the price feed off — but only for the city that actually plays in the league.
            WireOnlineServices(leagueCity);
        }

        // Start (or tear down) the shared HTTP pump and the contract poller based on whether online identity is
        // configured — independent of the price feed. The pump also marshals day-rollover report POSTs, so it
        // must exist whenever we're configured. Idempotent: EnsureRunning/Start/Stop are all safe to re-call.
        private static void WireOnlineServices(bool leagueCity)
        {
            if (leagueCity)
            {
                OmHttp.EnsureRunning();   // start the sim→main pump (reports) + frame loop (OnlineSync heartbeat)
                OnlineSync.Start();
            }
            else
            {
                OnlineSync.Stop();
                OmHttp.Stop();            // nulls the heartbeat and destroys the pump GameObject
            }
        }

        // One-time-per-loaded-save flag for the "this isn't your league city" heads-up (reset on level unload).
        private static bool _notLeagueCityNotified;

        // Decide whether the loaded save is the install's bound league city. Mints this save's per-city token if
        // needed; AUTO-BINDS the first configured city (so existing single-city players are unaffected); fires a
        // one-time notice when a DIFFERENT save loads. Main thread (lifecycle). Caller gates on IsOnlineConfigured.
        private static bool IsThisSaveTheLeagueCity()
        {
            string token = CityIdentity.EnsureToken();
            string bound = Settings.BoundCityTokenValue;
            if (string.IsNullOrEmpty(bound))
            {
                if (Settings.BoundCityToken != null) Settings.BoundCityToken.value = token; // auto-bind the first city
                Log.Info("League city: bound this city (" + OnlineSync.ShortId(token) + ").");
                return true;
            }
            if (bound == token) return true;
            NotifyNotLeagueCity();
            return false;
        }

        // Tell the player (once per load) that this save isn't the league city, so they know why it's offline in the
        // league and how to switch. A local Chirp (marshalled to the sim thread) + a log line. Never spams.
        private static void NotifyNotLeagueCity()
        {
            if (_notLeagueCityNotified) return;
            _notLeagueCityNotified = true;
            Log.Info("League city: this save is NOT the bound league city — staying offline for it (no profile/economy sync). " +
                     "Options → Open Markets → 'Make the loaded city my league city' to switch.");
            SimulationManager sm = SimulationManager.instance;
            if (sm != null)
                sm.AddAction(delegate
                {
                    Notify.MarketChirper.Post("This save isn't your league city, so it stays offline in the league. " +
                        "Open Options → Open Markets to make it your league city.");
                });
        }

        // Player chose (in Options) to make the currently-loaded city the league city. Rebind to THIS save's token
        // and re-wire so it starts acting online; the previously-bound save goes inert next time it loads. Switching
        // deliberately REPLACES the league city's server-side profile with this one — exactly the intent here.
        public static void BindLoadedCityToLeague()
        {
            if (!_levelLoaded) return;
            string token = CityIdentity.EnsureToken();
            if (Settings.BoundCityToken != null) Settings.BoundCityToken.value = token;
            _notLeagueCityNotified = false;
            Log.Info("League city: rebound to the loaded city (" + OnlineSync.ShortId(token) + ").");
            WirePriceSource();   // re-evaluate identity → now bound → starts services
            if (Settings.IsOnlineConfigured)
            {
                UI.Terminal.MarketTerminal.Destroy();
                UI.Terminal.MarketTerminal.Create();
            }
        }

        // Go offline: stop the pollers and revert to static base prices. Called from OnLevelUnloading AND from
        // IUserMod.OnDisabled (mod disable / recompile) so nothing can outlive the mod being switched off.
        public static void StopOnlineFeed()
        {
            OnlineMode.SetActive(false);
            OnlineSync.Stop();   // stop the pollers (clears its seen-set + heartbeat; MarketFeed.Clear → static base)
            OmHttp.Stop();       // tear down the sim→main pump so nothing lingers when offline
        }

        // The player created an account / created or joined a league from the Options UI. That can flip
        // IsOnlineConfigured true, which (a) the online services key off and (b) gates the online terminal
        // tabs. Re-wire the services and rebuild the terminal so the Contracts/Inbox/Members tabs appear
        // immediately, without needing the player to toggle the price feed or reload the city. MAIN THREAD
        // (settings-UI callback). No-op before a city is loaded — the next OnLevelLoaded handles it.
        public static void OnIdentityChanged()
        {
            if (!_levelLoaded) return;
            // Go through WirePriceSource (not WireOnlineServices directly) so OnlineMode is re-evaluated against the
            // new IsOnlineConfigured — otherwise import-charging + the net-volume report gate stay stale until reload.
            WirePriceSource();
            // The online tabs exist only once fully configured (account + league). Rebuild only then, so an
            // account-created-but-no-league-yet click doesn't needlessly tear down and recreate the terminal.
            if (Settings.IsOnlineConfigured)
            {
                UI.Terminal.MarketTerminal.Destroy();
                UI.Terminal.MarketTerminal.Create();
            }
        }

        // An online setting (the server endpoint) changed: apply it live if a city is loaded; otherwise the
        // next OnLevelLoaded picks it up. (Without this, editing mid-session did nothing / left a worker running.)
        public static void OnOnlineSettingChanged()
        {
            if (!_levelLoaded) return;
            WirePriceSource();
            // The online-only Contracts/Inbox tabs are decided at terminal-build time, so rebuild the terminal
            // to add/remove them when online is toggled mid-city. Main thread (settings callback) — safe to
            // touch CF UI. Destroy→Create is the same idempotent path the level-load hook uses.
            UI.Terminal.MarketTerminal.Destroy();
            UI.Terminal.MarketTerminal.Create();
        }

        public override void OnLevelUnloading()
        {
            base.OnLevelUnloading();
            Log.Info("Level unloading.");
            _levelLoaded = false;
            StopOnlineFeed();
            UI.Terminal.MarketTerminal.Destroy();
            MarketFeed.Instance.Clear();
            LocalPriceSim.Instance.Clear();
            SettlementLedger.Clear();
            PricingService.ResetDailyTotals();
            PricingService.ResetLifetime();
            InventoryService.Clear();
            Market.DeliveryLedger.Clear();
            Market.PriceAlerts.Clear();
            UI.Terminal.TradeTab.Forget();   // M10: drop the remembered "repeat last" basket
            TradeDelivery.Reset();
            TaxLock.Clear();
            DemandLever.Clear();
            BudgetLock.Clear();
            DeliveryStimulus.Clear();
            CoopBuff.Clear();
            CityIdentity.Clear();          // drop the in-memory city token (persisted copy stays in the save)
            _notLeagueCityNotified = false; // a different save loaded next gets its own one-time notice
            OpenMarketsThreading.ResetDayTracker();
        }
    }

    /// <summary>
    /// Simulation-thread ticker. Detects each new in-game day (via <c>m_currentGameTime.Date</c>) and
    /// advances the price walk, then logs a once-per-day summary of price drift + money booked. The
    /// per-tick check is allocation-free; the actual work runs only on a day rollover.
    /// </summary>
    public sealed class OpenMarketsThreading : ThreadingExtensionBase
    {
        // Day tracker is STATIC so multiple extension instances (e.g. a recompile re-creates one) can't
        // double-advance prices — only the first to see a new day fires. Reset on level unload.
        private static bool _primed;
        private static DateTime _lastDay;
        // Weekly-digest counter: in-game days elapsed since the last digest Chirp. Fires every 7th day (online only).
        private static int _daysSinceDigest;

        public static void ResetDayTracker()
        {
            _primed = false;
            _lastDay = default(DateTime);
            _daysSinceDigest = 0;
        }

        public override void OnAfterSimulationTick()
        {
            try
            {
                // M8 lever #4: re-apply any active co-op attractiveness buff EVERY tick — the global attractiveness
                // temp buffer is consumed + zeroed each cycle, so a steady bonus needs continuous re-application
                // (like a vanilla attractiveness building). Cheap + no-op when no buff is active. Runs before the
                // day-gate so it's applied on every tick, not just on a day rollover.
                CoopBuff.ApplyAttractiveness();

                DateTime today = SimulationManager.instance.m_currentGameTime.Date;
                // First tick after load: prime the day tracker and warm the inventory snapshot once (so the
                // Inventory tab / trade composer have depot stock immediately, not only after the first rollover).
                if (!_primed) { _primed = true; _lastDay = today; InventoryService.Scan(); MaybePostCityProfile(); TaxLock.EnsureReleasedIfOffline(); DemandLever.EnsureReleasedIfOffline(); BudgetLock.EnsureReleasedIfOffline(); CoopBuff.EnsureReleasedIfOffline(); return; }
                if (today == _lastDay) return;
                _lastDay = today;
                OnNewDay(today);
            }
            catch (Exception e)
            {
                Log.Error("OpenMarketsThreading tick failed: " + e);
            }
        }

        private static void OnNewDay(DateTime today)
        {
            // M9: the price model is server-owned now — no local price walk. LocalPriceSim survives only to
            // accumulate this city's net trade volume for the daily report (DrainReportAccumulator below), which
            // drives the server's elasticity. Prices/events/sparklines come from the MarketFeed (the /prices poll);
            // the price-event Chirps fire from there (OnlineSync), not from a local event generator.

            // M4: upload this city's per-commodity net supply/demand to the league feed (online only). This is what
            // moves the shared server index — and it IS genuinely per-day (a day's accumulated trade volume), so it
            // stays on the in-game-day rollover.
            MaybePostDailyReport();

            // Report this city's profile (population/happiness/industry/treasury) so leaguemates can see it. Gathered
            // HERE on the sim thread (cheap field reads); the HTTP post is marshalled to the main thread.
            MaybePostCityProfile();

            // NOTE: auto-settlement of contract/trade/bond installments used to run here on the day rollover, but the
            // server owns the due-clock on WALL-CLOCK time; settling on in-game days drifted out of step (it freezes on
            // pause, races at high sim speed). It now runs in OnlineSync.MaybeSettleSweep at the server's own period.

            MaybePostWeeklyDigest();  // every 7th in-game day: one league-standings summary Chirp (online only)

            InventoryService.Scan();  // refresh the depot snapshot for the inventory views / trade composer
            TaxLock.EnsureReleasedIfOffline();   // safety: never leave an offline city frozen at the austerity rate
            DemandLever.EnsureReleasedIfOffline();   // ditto: never leave an offline city stuck in the demand slump
            BudgetLock.EnsureReleasedIfOffline();    // ditto: never leave an offline city's budgets stranded at the cap
            CoopBuff.EnsureReleasedIfOffline();      // ditto: drop co-op buffs once the league is cleared (no more polls)
            DeliveryStimulus.Decay();   // M8 lever #3: fade the delivery→demand lift one day's worth (online or not)

            long exportCents, importCents;
            int exportCount, importCount;
            PricingService.DrainDailyTotals(out exportCents, out exportCount, out importCents, out importCount);

            string imports = importCount > 0
                ? string.Format(" | imports {0}× -§{1}", importCount, importCents / 100)
                : string.Empty;

            Log.Info(string.Format(
                "New day {0:yyyy-MM-dd}: exports {1}× +§{2}{3}",
                today, exportCount, exportCents / 100, imports));
        }

        // Drain this city's per-commodity net trade (SIM thread) and POST it to the league feed. The drain +
        // payload build happen here on the sim thread; the HTTP itself is marshaled to the main thread via
        // OmHttp.OnMainThread (UnityWebRequest is main-thread only). Online + configured only; a no-trade day
        // posts nothing. A failed post just drops that day's delta (next day reports fresh) — acceptable for a
        // price-index nudge.
        private static void MaybePostDailyReport()
        {
            if (!OnlineMode.IsActive || !Settings.IsOnlineConfigured) return;

            List<KeyValuePair<TransferManager.TransferReason, long>> drained =
                LocalPriceSim.Instance.DrainReportAccumulator();
            if (drained.Count == 0) return;

            List<ReportRowDto> rows = new List<ReportRowDto>(drained.Count);
            for (int i = 0; i < drained.Count; i++)
                rows.Add(new ReportRowDto { commodity = Commodities.Key(drained[i].Key), netSupply = drained[i].Value });

            ReportBatchDto batch = new ReportBatchDto { leagueId = Settings.LeagueIdValue, reports = rows.ToArray() };
            OmHttp.OnMainThread(delegate
            {
                OmApi.PostReports(batch, delegate(bool ok)
                {
                    if (!ok && Settings.IsDebugLogging) Log.Info("report: daily post failed (retries next day).");
                });
            });
        }

        // Every 7th in-game day, post ONE league-standings digest Chirp naming the current title-holders. Reads the
        // cached /leaderboards DTO (OnlineSync.LatestLeaderboards) — never re-fetches. Online (league city) only; a
        // null cache (no poll has landed yet) simply skips this week. SIM thread, and MarketChirper.Post is sim-safe,
        // so we post directly (no marshal). Gated on the Chirper opt-in like the other alerts.
        private static void MaybePostWeeklyDigest()
        {
            if (!OnlineMode.IsActive || !Settings.IsOnlineConfigured) return;
            _daysSinceDigest++;
            if (_daysSinceDigest < 7) return;
            _daysSinceDigest = 0;
            if (!Settings.IsChirperAlerts) return;

            Net.LeaderboardsDto dto = OnlineSync.LatestLeaderboards;
            if (dto == null || dto.boards == null) return;

            // Read the ACTUAL awarded title holders (the server already applied the "meaningful value" guards — e.g.
            // Deadbeat only when MissedCount>0), so the digest never crowns an innocent zero-miss / no-trade player.
            string baron = TitleHolder(dto, "Market Baron");
            string trader = TitleHolder(dto, "Top Trader");
            string deadbeat = TitleHolder(dto, "Deadbeat");
            if (baron == null && trader == null && deadbeat == null) return;

            System.Text.StringBuilder sb = new System.Text.StringBuilder("League weekly:");
            bool any = false;
            if (baron != null) { sb.Append(" Market Baron ").Append(baron); any = true; }
            if (trader != null) { sb.Append(any ? ", Top Trader " : " Top Trader ").Append(trader); any = true; }
            if (deadbeat != null) { sb.Append(", and ").Append(deadbeat).Append(" is the league Deadbeat"); }
            sb.Append('.');
            Notify.MarketChirper.Post(sb.ToString());
        }

        // The friendly name (via LeagueRoster.Display) of whoever currently HOLDS a given title, or null if nobody
        // does. Titles are awarded by the server with the meaningful-value guards already applied, and at most one
        // account holds each, so this is an exact "who's the Market Baron?" lookup.
        private static string TitleHolder(Net.LeaderboardsDto dto, string title)
        {
            if (dto.titles == null) return null;
            for (int i = 0; i < dto.titles.Length; i++)
            {
                Net.TitleEntryDto e = dto.titles[i];
                if (e == null || string.IsNullOrEmpty(e.accountId) || e.titles == null) continue;
                for (int t = 0; t < e.titles.Length; t++)
                    if (e.titles[t] == title) return LeagueRoster.Display(e.accountId);
            }
            return null;
        }

        // Gather this city's profile snapshot (SIM thread — cheap field reads) and POST it (marshalled to the main
        // thread). Online + configured only; a gather failure simply skips this day (retries next rollover).
        private static void MaybePostCityProfile()
        {
            if (!OnlineMode.IsActive || !Settings.IsOnlineConfigured) return;
            Net.CityProfilePostDto profile = CityStats.Gather();
            if (profile == null) return;
            OmHttp.OnMainThread(delegate
            {
                OmApi.PostCityProfile(profile, delegate(bool ok)
                {
                    if (!ok && Settings.IsDebugLogging) Log.Info("cityprofile: daily post failed (retries next day).");
                });
            });
        }
    }
}
