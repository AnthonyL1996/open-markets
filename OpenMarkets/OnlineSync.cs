using System;
using System.Collections.Generic;
using OpenMarkets.Net;
using OpenMarkets.Notify;
using OpenMarkets.Trade;
using UnityEngine;

namespace OpenMarkets
{
    /// <summary>
    /// Background online sync. Rides <see cref="OmHttp"/>'s main-thread frame loop (via
    /// <see cref="OmHttp.Heartbeat"/>) and polls the league's live feeds (roster, leagues, settlements, trade
    /// delivery, city state, prices) on fixed intervals whenever online is CONFIGURED (account + league —
    /// independent of the price-index feed toggle), nudging an open terminal to redraw so the Inbox/Members/…
    /// views stay live and firing Chirper alerts for price events / server resets.
    ///
    /// MAIN THREAD: the heartbeat, the <see cref="OmApi"/> calls, and their callbacks all run on Unity's main
    /// thread. The ONLY sim-thread hop is the Chirper post (MessageManager.QueueMessage is sim-thread work),
    /// marshalled via <c>SimulationManager.AddAction</c> — the same pattern the Inbox uses to book cash.
    /// Lifecycle-owned: <see cref="Start"/>/<see cref="Stop"/> are called from
    /// <see cref="OpenMarketsLoading"/>; when offline nothing is registered, so no network/timer exists.
    /// </summary>
    public static class OnlineSync
    {
        // Friend-group scale: an 8s cadence keeps the feeds feeling live without hammering a hobby backend.
        private const float PollIntervalSec = 8f;
        // The roster (id→display name) changes rarely, so poll it far less often than the live feeds; the first
        // poll fires immediately on going online so Chirps/Inbox show names from the start.
        private const float RosterIntervalSec = 40f;

        private static float _nextRosterAt;
        private static float _nextLeaguesAt;
        private static float _nextSettleAt;
        // Wall-clock auto-settle sweep (M9): the server owns the due-clock on REAL time, so we drive settlement off
        // this main-thread heartbeat at the server's own period instead of the in-game-day rollover (which freezes on
        // pause and races at high sim speed). The period is LEARNED from /citystate; the default is the server's own
        // 45-min default, used until the first citystate poll lands.
        private static float _nextSettleSweepAt;
        private static float _dueIntervalSec = 2700f;
        private static bool _rosterInFlight;
        private static bool _leaguesInFlight;
        private static bool _bondSettleInFlight;   // guards the day-rollover bond repay sweep
        private static bool _settlePollInFlight;  // guards the /settlements feed poll
        private static bool _deliveryInFlight;     // guards the /trades poll that drives physical delivery (M6)
        private static float _nextDeliveryAt;
        private static bool _cityStateInFlight;    // guards the /citystate poll that drives the austerity tax-lock (M7)
        private static float _nextCityStateAt;
        private static bool _pricesInFlight;        // guards the /prices poll that drives the MarketFeed price source (M9)
        private static float _nextPricesAt;
        private static bool _standingsInFlight;     // guards the /leaderboards poll that drives titles + the digest
        private static float _nextStandingsAt;
        private static bool _crisesInFlight;         // guards the /crises poll that drives the shared-league-crisis banner (slice 3)
        private static float _nextCrisesAt;

        /// <summary>The most recent /leaderboards response (or null until the first poll lands). Read by the weekly
        /// digest Chirp (ModLifecycle) — the cached DTO, never re-fetched on the sim thread. MAIN THREAD writes.</summary>
        public static LeaderboardsDto LatestLeaderboards { get; private set; }

        // Diff state for the league-wide AUSTERITY-entered Chirp: each member's austerity flag at the previous
        // roster poll. A false→true transition Chirps once. No baseline on the first poll (avoid spurious alerts).
        private static readonly Dictionary<string, bool> _prevAusterity = new Dictionary<string, bool>();
        private static bool _austerityPrimed;

        // Diff state for the personal DETHRONE Chirp: the previous rank-1 account per title-bearing board.
        private static readonly Dictionary<string, string> _prevLeader = new Dictionary<string, string>();
        private static bool _leaderPrimed;

        // Bumped on every Reset(). An in-flight GET fired before a Stop/Start (e.g. the player joins a league
        // while a poll is mid-flight) would otherwise return AFTER Reset() ran and act on stale state. Each
        // callback captures the generation it was fired under and discards itself if a Reset has happened since.
        private static int _generation;

        /// <summary>Begin polling. MAIN THREAD. Idempotent: re-registering the heartbeat is harmless, and we
        /// reset state so a fresh city / re-login doesn't inherit stale timers.</summary>
        public static void Start()
        {
            Reset();
            OmHttp.Heartbeat = Pump;   // OmHttp.Update invokes this each frame on the main thread
            if (Settings.IsDebugLogging) Log.Info("OnlineSync: started (poll every " + PollIntervalSec + "s).");
        }

        /// <summary>Stop polling and clear state. MAIN THREAD. Safe to call when not running. Note
        /// <see cref="OmHttp.Stop"/> also nulls the heartbeat, so a pump teardown can't leave us firing.</summary>
        public static void Stop()
        {
            OmHttp.Heartbeat = null;   // OnlineSync is the sole heartbeat user — clear unconditionally
            Reset();
            LeagueRoster.Clear();      // going offline / unloading — drop cached names (Start re-fetches them)
            MyLeagues.Clear();
            Market.MarketFeed.Instance.Clear(); // M9: going offline → back to static base prices
            Market.PriceAlerts.ResetRuntime();  // drop the crossing baseline so re-going-online re-seeds it
        }

        private static void Reset()
        {
            _nextRosterAt = 0f;
            _nextLeaguesAt = 0f;
            _nextSettleAt = 0f;
            _nextSettleSweepAt = 0f;   // sweep once promptly on going online (catch anything already due), then per period
            _rosterInFlight = false;
            _leaguesInFlight = false;
            _bondSettleInFlight = false;
            _settlePollInFlight = false;
            _deliveryInFlight = false;
            _nextDeliveryAt = 0f;
            _cityStateInFlight = false;
            _nextCityStateAt = 0f;
            _pricesInFlight = false;
            _nextPricesAt = 0f;
            _standingsInFlight = false;
            _nextStandingsAt = 0f;
            _crisesInFlight = false;
            _nextCrisesAt = 0f;
            LatestLeaderboards = null;
            // Drop the Chirp diff baselines so a city switch / re-login can't fire stale austerity/dethrone alerts.
            _prevAusterity.Clear();
            _austerityPrimed = false;
            _prevLeader.Clear();
            _leaderPrimed = false;
            _generation++;   // invalidate any in-flight callback fired under the previous generation
        }

        // Per-frame heartbeat (main thread). Cheap until an interval elapses: time compares + flag reads.
        // The feeds poll on independent timers — all need only identity + league, NOT the price-index feed.
        // If the player cleared their account/league mid-session, none fires.
        private static void Pump(float now)
        {
            if (!Settings.IsOnlineConfigured) return;
            MaybePollRoster(now);
            MaybePollLeagues(now);
            MaybePollSettlements(now);
            MaybePollDelivery(now);
            MaybePollCityState(now);
            MaybePollPrices(now);
            MaybePollStandings(now);
            MaybePollCrises(now);
            MaybeSettleSweep(now);
        }

        // Title-bearing boards (board id → title name) for the personal DETHRONE Chirp. The deadbeat board is a
        // shame board (no positive title to lose), so it's deliberately absent.
        private static readonly KeyValuePair<string, string>[] TitleBoards =
        {
            new KeyValuePair<string, string>("netWorth", "Market Baron"),
            new KeyValuePair<string, string>("marketMover", "Market Mover"),
            new KeyValuePair<string, string>("tradeVolume", "Top Trader"),
            new KeyValuePair<string, string>("patron", "Patron"),
            new KeyValuePair<string, string>("reliability", "Good Credit"),
            new KeyValuePair<string, string>("phoenix", "Phoenix"),
            new KeyValuePair<string, string>("population", "Metropolis"),
        };

        // Poll /leaderboards on the roster cadence: drives the traveling titles (LeagueRoster.SetTitles), caches the
        // DTO for the weekly digest, and fires the personal DETHRONE Chirp when MY rank-1 on a title board is taken.
        // Gated like the other polls (online + league city); generation-guarded + in-flight bool. MAIN THREAD.
        private static void MaybePollStandings(float now)
        {
            if (_standingsInFlight || now < _nextStandingsAt) return;
            _standingsInFlight = true;
            int gen = _generation;
            OmApi.GetLeaderboards(delegate (bool ok, LeaderboardsDto dto)
            {
                if (gen != _generation) return;
                _standingsInFlight = false;
                _nextStandingsAt = Time.realtimeSinceStartup + RosterIntervalSec;
                if (ok && dto != null)
                {
                    LatestLeaderboards = dto;
                    LeagueRoster.SetTitles(dto.titles);
                    ChirpDethrones(dto);
                    UI.Terminal.MarketTerminal.NotifyDataChanged(); // titles may have changed names shown elsewhere
                }
            });
        }

        // Diff each title board's rank-1 against the previous poll; if I WAS the leader and someone else now is,
        // Chirp that I've been dethroned. Skips the first poll (no baseline). MAIN THREAD; the Chirp marshals to sim.
        private static void ChirpDethrones(LeaderboardsDto dto)
        {
            string me = Settings.AccountIdValue;
            Dictionary<string, string> newLeaders = new Dictionary<string, string>();
            if (dto.boards != null)
            {
                for (int i = 0; i < dto.boards.Length; i++)
                {
                    BoardDto b = dto.boards[i];
                    if (b == null || string.IsNullOrEmpty(b.id) || b.rows == null) continue;
                    string top = LeaderOf(b);
                    if (top != null) newLeaders[b.id] = top;
                }
            }

            if (_leaderPrimed && Settings.IsChirperAlerts && !string.IsNullOrEmpty(me))
            {
                SimulationManager sm = SimulationManager.instance;
                for (int t = 0; t < TitleBoards.Length; t++)
                {
                    string boardId = TitleBoards[t].Key, title = TitleBoards[t].Value;
                    string prev, now2;
                    _prevLeader.TryGetValue(boardId, out prev);
                    newLeaders.TryGetValue(boardId, out now2);
                    if (prev == me && !string.IsNullOrEmpty(now2) && now2 != me && sm != null)
                    {
                        string newId = now2;   // loop-local for the closure
                        string text = "You've been dethroned as " + title + " by " + LeagueRoster.Display(newId) + "!";
                        sm.AddAction(delegate { MarketChirper.Post(text); });
                    }
                }
            }

            _prevLeader.Clear();
            foreach (KeyValuePair<string, string> kv in newLeaders) _prevLeader[kv.Key] = kv.Value;
            _leaderPrimed = true;
        }

        // The rank-1 account on a board, or null if none is explicitly ranked #1. We require an explicit rank==1
        // (not "first row") so a malformed/unranked response can't name the wrong leader and fire a false dethrone.
        private static string LeaderOf(BoardDto b)
        {
            for (int i = 0; i < b.rows.Length; i++)
                if (b.rows[i] != null && b.rows[i].rank == 1) return b.rows[i].accountId;
            return null;
        }

        // Auto-settle on the WALL-CLOCK due period (M9). Settlement used to ride the in-game-day rollover, which
        // stops while the game is paused and outruns the server at high sim speed — out of step with the server's
        // real-time due-clock (so a paused/sped city could miss or over-settle). Driving it from this main-thread
        // heartbeat at the server's OWN period (learned via /citystate) keeps the client paying one installment per
        // period regardless of pause or sim speed. The settle logic is unchanged; only its trigger clock moved.
        private static void MaybeSettleSweep(float now)
        {
            if (now < _nextSettleSweepAt) return;
            _nextSettleSweepAt = now + _dueIntervalSec;
            SettleDue();   // re-checks IsOnlineConfigured + the AutoSettle opt-in
        }

        // Poll /prices to drive the M9 MarketFeed (the price source): the league's effective per-commodity index +
        // events + sparkline history. On failure we DON'T publish, so the feed freezes at the last index until the
        // server returns (freeze-at-last); going offline clears it back to static base prices (see Stop). MAIN THREAD.
        private static void MaybePollPrices(float now)
        {
            if (_pricesInFlight || now < _nextPricesAt) return;
            _pricesInFlight = true;
            int gen = _generation;
            OmApi.GetPrices(delegate (bool ok, PricesDto dto)
            {
                if (gen != _generation) return;
                _pricesInFlight = false;
                _nextPricesAt = Time.realtimeSinceStartup + PollIntervalSec;
                if (ok && dto != null)
                {
                    Market.MarketFeed.Instance.Publish(dto);
                    ChirpPriceEvents();   // one-shot alert for any commodity whose event just started
                    Market.PriceAlerts.Check();   // one-shot alert for any commodity that crossed a player threshold
                }
            });
        }

        // Chirp once for each commodity whose price event just started (server feed). MarketChirper.Post is sim work
        // and we're on the main thread, so marshal; gated on the Chirper opt-in (Post itself doesn't gate).
        private static void ChirpPriceEvents()
        {
            // Always drain (even with alerts off) so the new-events list can't accumulate unbounded.
            List<TransferManager.TransferReason> started = Market.MarketFeed.Instance.TakeNewEvents();
            if (!Settings.IsChirperAlerts || started.Count == 0) return;
            SimulationManager sm = SimulationManager.instance;
            if (sm == null) return;
            for (int i = 0; i < started.Count; i++)
            {
                TransferManager.TransferReason c = started[i];
                int pct = Market.MarketFeed.Instance.EventPct(c);
                string text = Data.Commodities.DisplayName(c) + " prices are "
                    + (pct >= 0 ? "spiking (+" : "sliding (") + pct + "%) on the league market.";
                sm.AddAction(delegate { MarketChirper.Post(text); });
            }
        }

        // Poll /crises to drive the shared-league-crisis banner (social slice 3): the active named, narrated crises
        // (global — same for everyone). On success publish to the MarketFeed so the Market tab can render the banner,
        // and fire a one-shot Chirp for any crisis that just appeared. MAIN THREAD.
        private static void MaybePollCrises(float now)
        {
            if (_crisesInFlight || now < _nextCrisesAt) return;
            _crisesInFlight = true;
            int gen = _generation;
            OmApi.GetCrises(delegate (bool ok, CrisesDto dto)
            {
                if (gen != _generation) return;
                _crisesInFlight = false;
                _nextCrisesAt = Time.realtimeSinceStartup + PollIntervalSec;
                if (ok && dto != null)
                {
                    Market.MarketFeed.Instance.PublishCrises(dto);
                    ChirpNewCrises();
                }
            });
        }

        // Chirp once for each crisis that just appeared (server feed). MarketChirper.Post is sim work and we're on the
        // main thread, so marshal; gated on the Chirper opt-in.
        private static void ChirpNewCrises()
        {
            // Always drain (even with alerts off) so the new-crisis list can't accumulate unbounded.
            List<CrisisDto> started = Market.MarketFeed.Instance.TakeNewCrises();
            if (!Settings.IsChirperAlerts || started.Count == 0) return;
            SimulationManager sm = SimulationManager.instance;
            if (sm == null) return;
            for (int i = 0; i < started.Count; i++)
            {
                CrisisDto c = started[i];
                string text = !string.IsNullOrEmpty(c.narrative) ? c.narrative : ("Crisis: " + c.name);
                sm.AddAction(delegate { MarketChirper.Post(text); });
            }
        }

        // Poll /citystate to drive the austerity tax-lock: when the city owes a terminally-defaulted bond it enters
        // austerity and its taxes are forced + frozen; when that clears, the player's rates are restored. The tax
        // mutation is marshalled to the sim thread inside TaxLock.Sync. Same live cadence as the other feeds.
        private static void MaybePollCityState(float now)
        {
            if (_cityStateInFlight || now < _nextCityStateAt) return;
            _cityStateInFlight = true;
            int gen = _generation;
            OmApi.GetCityState(delegate (bool ok, CityStateDto dto)
            {
                if (gen != _generation) return;
                _cityStateInFlight = false;
                _nextCityStateAt = Time.realtimeSinceStartup + PollIntervalSec;
                if (ok && dto != null)
                {
                    TaxLock.Sync(dto.austerity);
                    DemandLever.Sync(dto.austerity);   // M8 lever #1: austerity demand slump rides the same state as the tax-lock
                    BudgetLock.Sync(dto.austerity);    // M8 lever #2: austerity budget cap, ditto
                    CoopBuff.Sync(dto.effects, dto.investmentsMade); // M8 lever #4: received buffs + investments made
                    if (dto.dueIntervalSec > 0) _dueIntervalSec = dto.dueIntervalSec;  // pace auto-settle to the server's period
                }
            });
        }

        // Poll /trades to (1) publish the reserved-stock snapshot for the driver clamp + inventory views and
        // (2) physically deliver any newly-settled installments (give-goods out of my depots, receive-goods in).
        // Same live cadence as settlements; the actual warehouse mutation marshals to the sim thread internally.
        private static void MaybePollDelivery(float now)
        {
            if (_deliveryInFlight || now < _nextDeliveryAt) return;
            _deliveryInFlight = true;
            int gen = _generation;
            OmApi.GetTrades(delegate (bool ok, TradeListDto list)
            {
                if (gen != _generation) return;   // a Stop/Start happened mid-flight — discard
                _deliveryInFlight = false;
                _nextDeliveryAt = Time.realtimeSinceStartup + PollIntervalSec;
                if (!ok || list == null) return;

                string me = Settings.AccountIdValue;
                Dictionary<TransferManager.TransferReason, long> resUnits =
                    InventoryReservations.ReservedByCommodity(list.trades, me);
                Dictionary<TransferManager.TransferReason, long> resTU =
                    new Dictionary<TransferManager.TransferReason, long>(resUnits.Count);
                foreach (KeyValuePair<TransferManager.TransferReason, long> kv in resUnits)
                    resTU[kv.Key] = kv.Value * InventoryService.TransferUnitsPerUnit;
                InventoryService.PublishReserved(resTU);

                TradeDelivery.ProcessDue(list.trades);
            });
        }

        // Pull the league's settlement feed (trade/bond installments booked server-side) and apply any events we
        // haven't booked yet to the local treasury. Same live cadence as the other feeds; idempotent via the persisted
        // SettlementLedger cursor, so re-fetching already-booked events is harmless.
        private static void MaybePollSettlements(float now)
        {
            if (_settlePollInFlight || now < _nextSettleAt) return;
            _settlePollInFlight = true;
            int gen = _generation;
            string league = Settings.LeagueIdValue;
            OmApi.GetSettlements(Market.SettlementLedger.LastSeq(league), delegate(bool ok, SettlementListDto list)
            {
                if (gen != _generation) return;   // a Stop/Start happened mid-flight — discard
                _settlePollInFlight = false;
                _nextSettleAt = Time.realtimeSinceStartup + PollIntervalSec;
                if (!ok || list == null) return;
                // Server data-wipe detection via the EPOCH (the only safe reset signal — a bare seq comparison is
                // not, since the cursor can exceed our own latest seq). A changed epoch means the server's data
                // was wiped, so the old events are gone and replaying the fresh economy from 0 can't double-book.
                if (!string.IsNullOrEmpty(list.epoch))
                {
                    string known = Market.SettlementLedger.ServerEpoch;
                    if (string.IsNullOrEmpty(known))
                    {
                        Market.SettlementLedger.SetServerEpoch(list.epoch); // first sight — adopt, no reset
                    }
                    else if (known != list.epoch)
                    {
                        // Server was reset: drop ALL league cursors, adopt the new epoch, notify once, re-poll.
                        Market.SettlementLedger.ResetAll();
                        Market.DeliveryLedger.ResetAll();   // re-deliver the fresh economy's goods from 0
                        TradeDelivery.Reset();

                        Market.SettlementLedger.SetServerEpoch(list.epoch);
                        if (Settings.IsChirperAlerts) ChirpServerReset();
                        _nextSettleAt = 0f; // re-poll next frame to book the fresh economy from seq 0
                        return;             // this response was fetched at the old cursor — skip it
                    }
                }
                if (list.events != null && list.events.Length > 0)
                    Market.SettlementReconciler.Book(league, list.events);
            });
        }

        // Refresh the id→name cache so Chirps/Inbox/Members can show friendly names even if the player never
        // opens the Members tab. Its own in-flight flag + timer.
        private static void MaybePollRoster(float now)
        {
            if (_rosterInFlight || now < _nextRosterAt) return;
            _rosterInFlight = true;
            int gen = _generation;
            OmApi.GetMembers(delegate(bool ok, MembersDto dto)
            {
                if (gen != _generation) return;
                _rosterInFlight = false;
                _nextRosterAt = Time.realtimeSinceStartup + RosterIntervalSec;
                if (ok && dto != null)
                {
                    LeagueRoster.Update(dto);
                    ChirpAusterityEntered(dto);   // alert when any leaguemate falls into austerity
                    UI.Terminal.MarketTerminal.NotifyDataChanged(); // names may have changed
                }
            });
        }

        // Diff each member's austerity flag against the previous roster poll; Chirp once on a false→true transition.
        // Skips the first poll (no baseline). MAIN THREAD; the Chirp marshals to the sim thread.
        private static void ChirpAusterityEntered(MembersDto dto)
        {
            Dictionary<string, bool> current = new Dictionary<string, bool>();
            if (dto.members != null)
            {
                SimulationManager sm = _austerityPrimed ? SimulationManager.instance : null;
                for (int i = 0; i < dto.members.Length; i++)
                {
                    MemberDto m = dto.members[i];
                    if (m == null || string.IsNullOrEmpty(m.accountId)) continue;
                    current[m.accountId] = m.austerity;
                    if (!_austerityPrimed || !Settings.IsChirperAlerts || sm == null) continue;
                    bool was;
                    if (_prevAusterity.TryGetValue(m.accountId, out was) && !was && m.austerity)
                    {
                        string text = LeagueRoster.Display(m.accountId) + " has entered AUSTERITY!";
                        sm.AddAction(delegate { MarketChirper.Post(text); });
                    }
                }
            }
            _prevAusterity.Clear();
            foreach (KeyValuePair<string, bool> kv in current) _prevAusterity[kv.Key] = kv.Value;
            _austerityPrimed = true;
        }

        // Refresh the list of leagues this account is in, so the terminal's league switcher knows the choices.
        // Same low cadence as the roster (it changes only when the player joins/creates a league).
        private static void MaybePollLeagues(float now)
        {
            if (_leaguesInFlight || now < _nextLeaguesAt) return;
            _leaguesInFlight = true;
            int gen = _generation;
            OmApi.GetMyLeagues(delegate(bool ok, MyLeaguesDto dto)
            {
                if (gen != _generation) return;
                _leaguesInFlight = false;
                _nextLeaguesAt = Time.realtimeSinceStartup + RosterIntervalSec;
                if (ok && dto != null)
                {
                    MyLeagues.Update(dto);
                    UI.Terminal.MarketTerminal.NotifyDataChanged(); // switcher availability may have changed
                }
            });
        }

        /// <summary>Auto-settle: advance MY due installment by one on every active bond. Called on the MAIN thread
        /// from <see cref="MaybeSettleSweep"/> once per server due-period (wall-clock, learned via /citystate).
        /// Gated on the AutoSettle opt-in. One /settle per active bond per call; booking is idempotent.</summary>
        public static void SettleDue()
        {
            if (!Settings.IsOnlineConfigured || !Settings.IsAutoSettle) return;
            // Trades are NOT settled here: once a trade is agreed the SERVER auto-settles each installment on its
            // due-clock (store.AutoSettleTradeInstallment), so payment is automatic for every city — online,
            // offline, opted-in or not. This client only reconciles the booked cash (the /settlements poll) and
            // delivers the goods (the /trades delivery poll). Bonds still settle client-side below.
            SettleDueBonds();
        }

        /// <summary>Run the /settlements poll on the next heartbeat frame (after a settle/repay) so booked cash
        /// appears promptly. Booking always goes through the single CONTIGUOUS cursor (poll from LastSeq), never
        /// an isolated out-of-order event — that would let a later event jump the cursor past earlier unbooked
        /// ones and drop them. MAIN THREAD.</summary>
        public static void RequestSettlementPoll() { _nextSettleAt = 0f; }

        // Repay one due installment on each of MY active/delinquent bonds (I'm the debtor). Booking idempotent.
        private static void SettleDueBonds()
        {
            if (_bondSettleInFlight) return;
            _bondSettleInFlight = true;
            OmApi.GetBonds(delegate(bool ok, BondListDto list)
            {
                _bondSettleInFlight = false;
                if (!ok || list == null || list.bonds == null) return;
                string me = Settings.AccountIdValue;
                for (int i = 0; i < list.bonds.Length; i++)
                {
                    BondDto b = list.bonds[i];
                    if (b == null || b.debtorId != me || b.settled >= b.installments) continue;
                    if (b.status != "active" && b.status != "delinquent") continue;
                    OmApi.SettleBond(b.id, delegate(bool ok2, BondSettleResultDto res, string error2)
                    {
                        if (ok2) RequestSettlementPoll(); // book via the contiguous /settlements poll, not this event
                    });
                }
                UI.Terminal.MarketTerminal.NotifyDataChanged();
            });
        }

        // One Chirp telling the player the online economy was reset (server data wipe). Marshalled to the sim
        // thread; the player's treasury is unchanged (old cash was real), only the feed restarts.
        private static void ChirpServerReset()
        {
            SimulationManager sm = SimulationManager.instance;
            if (sm == null) return;
            sm.AddAction(delegate
            {
                MarketChirper.Post("Online economy was reset by the server — your treasury is unchanged; trades start fresh.");
            });
        }

        /// <summary>Trim a long account id for display (the server's only identity is the raw id).</summary>
        public static string ShortId(string id)
        {
            if (string.IsNullOrEmpty(id)) return "?";
            // ASCII "…" — a modded game's NGUI font atlas may not carry U+2026.
            return id.Length <= 10 ? id : id.Substring(0, 10) + "...";
        }
    }
}
