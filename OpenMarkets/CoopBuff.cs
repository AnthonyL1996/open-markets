using System.Collections.Generic;
using ColossalFramework;
using OpenMarkets.Data;
using OpenMarkets.Net;
using OpenMarkets.Notify;
using OpenMarkets.Trade;
using UnityEngine;

namespace OpenMarkets
{
    /// <summary>
    /// M8 City Lever #4 — CO-OP BUFFS (investment office). A leaguemate pays a symmetric § cost (transferred to
    /// this city — a real investment) to grant it a temporary demand + attractiveness lift. The server is
    /// authoritative for who/what/duration and ships the active buffs in the <c>/citystate</c> effects payload (the
    /// same poll that drives the austerity levers); this holder applies them locally. Two effects:
    ///   • DEMAND — <see cref="DemandBoost"/> is added to RCI by <c>DemandSlumpExtension</c> (alongside the slump +
    ///     delivery lift), transient (return-value only);
    ///   • ATTRACTIVENESS — <see cref="ApplyAttractiveness"/> re-adds the rate to the global attractiveness temp
    ///     buffer each sim tick (the buffer is consumed + zeroed per cycle, so it must be re-applied continuously).
    /// Both are fully transient — nothing is baked into the game save; stop applying → vanilla reverts within a
    /// cycle. So this needs no SaveData and no guardrail stash: a BENEFIT, time-boxed + cost-symmetric server-side.
    ///
    /// Threading: <see cref="Sync"/> runs on the MAIN thread (the /citystate poll) and only writes the volatile
    /// magnitude fields + posts the receive Chirp (marshalled to sim). <see cref="ApplyAttractiveness"/> and the
    /// <see cref="DemandBoost"/> read run on the SIM thread. No manager mutation happens off the sim thread.
    /// </summary>
    public static class CoopBuff
    {
        /// <summary>Cap on the SUMMED demand lift across all active investments (bounds multi-friend stacking on top
        /// of the server's per-grant cap). Tunable.</summary>
        public const int DemandSumCap = 30;

        /// <summary>Cap on the summed attractiveness rate across all active investments. Tunable.</summary>
        public const int AttractSumCap = 1000;

        // Demand lift is now TARGETED: each investment names a demand channel (res | com | work), so the lift is
        // bucketed per channel rather than added to all three. CS1's demand API has only these three channels
        // (industrial + office share "work"). Each is summed across active grants and capped independently.
        private static volatile int _demandRes;
        private static volatile int _demandCom;
        private static volatile int _demandWork;
        private static volatile int _attractRate;

        // Effect ids we've already announced, so the receive Chirp fires once per investment (ids are unique and
        // never reused). Reset on Clear (level unload / going offline). MAIN THREAD only.
        private static readonly HashSet<string> _seen = new HashSet<string>();

        // The active investments on this city (issuer / §amount / kind / days), retained so the Members tab can LIST
        // them — the summed magnitudes above aren't enough to show "who gave how much". Volatile reference swap;
        // read on the MAIN thread (UI) only, same as Sync writes. Empty array when none.
        private static volatile CityEffectDto[] _activeEffects = new CityEffectDto[0];
        private static volatile CityEffectDto[] _investmentsMade = new CityEffectDto[0];
        private static volatile CityEffectDto[] _tradeRewards = new CityEffectDto[0];

        /// <summary>The active investment buffs RECEIVED by this city (issuer, §cost, demand kind, days left). MAIN
        /// THREAD read.</summary>
        public static CityEffectDto[] ActiveEffects { get { return _activeEffects; } }

        /// <summary>The active investments this city has MADE in others (grantee, §cost, kind, days left). MAIN THREAD.</summary>
        public static CityEffectDto[] InvestmentsMade { get { return _investmentsMade; } }

        /// <summary>Active project trade rewards received by this city. MAIN THREAD read.</summary>
        public static CityEffectDto[] ActiveTradeRewards { get { return _tradeRewards; } }

        /// <summary>
        /// The active <c>priceEdge</c> bonus (basis points) for one commodity wire-key, 0 if none — applied by
        /// <see cref="OpenMarkets.Market.PricingService.BookExport"/> to book a better §/truck on themed exports vs.
        /// the outside world. SIM-THREAD safe: reads the <c>volatile</c> <see cref="_tradeRewards"/> reference, and
        /// <see cref="Sync"/> only ever SWAPS IN a fresh immutable array (never mutates one in place), so iterating
        /// this snapshot can't tear. If several edges target the same commodity the strongest wins. (priceEdge is
        /// export-only by design — there is deliberately no import/settlement equivalent.)
        /// </summary>
        public static int EdgeBipsFor(TransferManager.TransferReason commodity)
        {
            // Hot path (per-export booking): avoid the enum.ToString() alloc in Commodities.Key when there are no
            // active trade rewards at all — the overwhelmingly common case (solo + most online play).
            if (_tradeRewards.Length == 0) return 0;
            return EdgeBipsFor(Commodities.Key(commodity));
        }

        public static int EdgeBipsFor(string commodityKey)
        {
            if (string.IsNullOrEmpty(commodityKey)) return 0;
            CityEffectDto[] rewards = _tradeRewards; // volatile snapshot
            int bips = 0;
            for (int i = 0; i < rewards.Length; i++)
            {
                CityEffectDto e = rewards[i];
                if (e == null || e.kind != "priceEdge" || e.commodity != commodityKey) continue;
                if (e.tradePctBips > bips) bips = e.tradePctBips;
            }
            return bips;
        }

        // Map a demand-kind wire key to a label (mirrors MembersTab's DemandLabels).
        public static string KindLabel(string demandKind)
        {
            if (demandKind == "com") return "Commercial";
            if (demandKind == "work") return "Industry & Office";
            return "Residential";
        }

        public static string TradeRewardText(CityEffectDto e)
        {
            if (e == null) return "Trade reward";
            string c = CommodityLabel(e.commodity);
            if (e.kind == "marketShield")
                return c + " market impact reduced by " + Pct(e.tradePctBips);
            if (e.kind == "priceEdge")
                return "+" + Pct(e.tradePctBips) + " " + c + " export price";
            return "Trade reward";
        }

        /// <summary>Demand points to add to each channel from active co-op investments. Read by the demand extension
        /// (sim thread): Residential / Commercial / Workplace (industrial+office).</summary>
        public static int DemandBoostRes { get { return _demandRes; } }
        public static int DemandBoostCom { get { return _demandCom; } }
        public static int DemandBoostWork { get { return _demandWork; } }

        /// <summary>React to the /citystate effects payload. MAIN THREAD. <paramref name="effects"/> are the buffs
        /// RECEIVED (they drive demand/attractiveness); <paramref name="made"/> are active investments this city granted
        /// (display only). Sums the received buffs (clamped), Chirps newly-seen grants, retains both lists for the UI.</summary>
        public static void Sync(CityEffectDto[] effects, CityEffectDto[] made)
        {
            int prevMade = _investmentsMade.Length;
            int prevTrade = _tradeRewards.Length;
            _investmentsMade = made ?? new CityEffectDto[0];
            int res = 0, com = 0, work = 0, attract = 0;
            List<CityEffectDto> fresh = null;
            List<CityEffectDto> active = null;
            List<CityEffectDto> tradeRewards = null;

            if (effects != null)
            {
                for (int i = 0; i < effects.Length; i++)
                {
                    CityEffectDto e = effects[i];
                    if (e == null) continue;
                    // Apply BOTH co-op investment buffs and completed-megaproject reward buffs ("projectBuff") — both
                    // carry a demandBoost/attractRate/demandKind. They share the same summed caps below (so the total
                    // city lift is bounded regardless of source). Anything else is ignored.
                    bool isInvest = e.kind == "investmentOffice";
                    bool isProject = e.kind == "projectBuff";
                    bool isMarketShield = e.kind == "marketShield";
                    bool isPriceEdge = e.kind == "priceEdge";
                    if (isInvest || isProject)
                    {
                        // Bucket the demand lift by its targeted channel. "com"/"work" route there; anything else
                        // (incl. "res" and an empty kind from an older grant) defaults to residential.
                        if (e.demandKind == "com") com += e.demandBoost;
                        else if (e.demandKind == "work") work += e.demandBoost;
                        else res += e.demandBoost;
                        attract += e.attractRate;
                        // The Members-tab "Investments in your city" list shows INVESTMENTS only — a project reward
                        // isn't an investment from a specific leaguemate, so keep it out of that list.
                        if (isInvest)
                        {
                            if (active == null) active = new List<CityEffectDto>();
                            active.Add(e);
                        }
                    }
                    else if (isMarketShield || isPriceEdge)
                    {
                        if (tradeRewards == null) tradeRewards = new List<CityEffectDto>();
                        tradeRewards.Add(e);
                    }
                    else continue;
                    if (!string.IsNullOrEmpty(e.id) && _seen.Add(e.id))
                    {
                        if (fresh == null) fresh = new List<CityEffectDto>();
                        fresh.Add(e);
                    }
                }
            }

            int prevCount = _activeEffects.Length;
            _activeEffects = active != null ? active.ToArray() : new CityEffectDto[0];
            _tradeRewards = tradeRewards != null ? tradeRewards.ToArray() : new CityEffectDto[0];

            // Clamp each channel to the sum cap; floor at 0 defensively — a buff lever must never SUBTRACT demand /
            // attractiveness even if the server somehow sent a negative magnitude (these are benefits only).
            _demandRes = ClampDemand(res);
            _demandCom = ClampDemand(com);
            _demandWork = ClampDemand(work);
            _attractRate = attract < 0 ? 0 : (attract > AttractSumCap ? AttractSumCap : attract);

            if (fresh != null) ChirpReceived(fresh);
            // An investment just arrived or expired → redraw an open terminal so the Members tab's "Investments in your
            // city" section appears/updates live (the /citystate poll runs whether or not that tab is showing).
            if (fresh != null || _activeEffects.Length != prevCount || _investmentsMade.Length != prevMade
                || _tradeRewards.Length != prevTrade)
                UI.Terminal.MarketTerminal.NotifyDataChanged();
        }

        /// <summary>Re-apply the active attractiveness rate to the global temp buffer. SIM THREAD, called every tick
        /// (the buffer is consumed + zeroed each cycle, so a steady bonus needs continuous re-application — the same
        /// way vanilla attractiveness buildings contribute each step). Cheap: a single guarded array add, no alloc.</summary>
        public static void ApplyAttractiveness()
        {
            int rate = _attractRate;
            if (rate <= 0) return;
            ImmaterialResourceManager irm = Singleton<ImmaterialResourceManager>.instance;
            if (irm != null) irm.AddResource(ImmaterialResourceManager.Resource.Attractiveness, rate);
        }

        /// <summary>Drop all buff state on level unload / going offline. The buffs are transient (nothing to undo);
        /// the next /citystate poll re-seeds from the server if any are still active.</summary>
        public static void Clear()
        {
            _demandRes = 0;
            _demandCom = 0;
            _demandWork = 0;
            _attractRate = 0;
            _activeEffects = new CityEffectDto[0];
            _investmentsMade = new CityEffectDto[0];
            _tradeRewards = new CityEffectDto[0];
            _seen.Clear();
        }

        // Clamp one channel's summed demand lift to [0, DemandSumCap].
        private static int ClampDemand(int v) { return v < 0 ? 0 : (v > DemandSumCap ? DemandSumCap : v); }

        /// <summary>Release the buffs when online is no longer configured (the player cleared their league): the
        /// /citystate poll stops, so Sync can never zero a stale buff. SIM THREAD (day + first post-load tick),
        /// mirroring the austerity levers' offline-release. While configured, the poll keeps the buff live.</summary>
        public static void EnsureReleasedIfOffline()
        {
            if ((_demandRes != 0 || _demandCom != 0 || _demandWork != 0 || _attractRate != 0) && !Settings.IsOnlineConfigured) Clear();
        }

        // Post one Chirp per newly-seen investment, attributed to the issuer. MarketChirper.Post is sim-thread work
        // and Sync is on the main thread, so marshal (like OnlineSync's offer chirps). Gated on the Chirper opt-in.
        private static void ChirpReceived(List<CityEffectDto> fresh)
        {
            if (!Settings.IsChirperAlerts) return;
            SimulationManager sm = SimulationManager.instance;
            if (sm == null) return;
            for (int i = 0; i < fresh.Count; i++)
            {
                CityEffectDto e = fresh[i];
                string text;
                if (e.kind == "projectBuff")
                    text = "A completed league project rewards your city — " + KindLabel(e.demandKind)
                        + " demand + attractiveness up for ~" + e.ticksRemaining + " days.";
                else if (e.kind == "marketShield" || e.kind == "priceEdge")
                    text = "A completed league project rewards your city — " + TradeRewardText(e)
                        + " for ~" + e.ticksRemaining + " days.";
                else
                    text = LeagueRoster.Display(e.issuerId) + " invested §" + (e.costCents / 100).ToString("N0")
                        + " in your city — " + KindLabel(e.demandKind) + " demand + attractiveness up for ~" + e.ticksRemaining + " days.";
                sm.AddAction(delegate { MarketChirper.Post(text); });
            }
        }

        private static string CommodityLabel(string key)
        {
            TransferManager.TransferReason reason;
            if (!string.IsNullOrEmpty(key) && Commodities.TryFromKey(key, out reason)) return Commodities.DisplayName(reason);
            return string.IsNullOrEmpty(key) ? "commodity" : key;
        }

        private static string Pct(int bips)
        {
            if (bips < 0) bips = -bips;
            int whole = bips / 100;
            int frac = bips % 100;
            if (frac == 0) return whole + "%";
            if (frac % 10 == 0) return whole + "." + (frac / 10) + "%";
            return whole + "." + (frac < 10 ? "0" : "") + frac + "%";
        }
    }
}
