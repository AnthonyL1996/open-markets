using System.Collections.Generic;
using OpenMarkets.Data;
using OpenMarkets.Notify;

namespace OpenMarkets.Market
{
    /// <summary>
    /// Per-commodity price-alert thresholds (a target §/truck the player wants to be told about). When the live
    /// <see cref="MarketFeed"/> truck price CROSSES a threshold (from one side to the other between two polls), a
    /// ONE-SHOT Chirp fires; it re-arms only once the price moves back to the original side, so a price hovering at
    /// the line can't re-spam. Thresholds persist in the save (SaveData §12) so they survive reloads; the last-seen
    /// price (for crossing detection) is in-memory only and re-seeded on the first poll after a load.
    ///
    /// MAIN THREAD: thresholds are set/cleared from the Market tab UI and checked from the /prices poll callback,
    /// both on the main thread. The Chirp itself is marshalled to the SIM thread by the checker (MarketChirper.Post
    /// is sim-thread work).
    /// </summary>
    public static class PriceAlerts
    {
        // commodity → threshold in §/truck (whole §, the unit shown in the Market board). Absent = no alert.
        private static readonly Dictionary<TransferManager.TransferReason, long> _thresholds =
            new Dictionary<TransferManager.TransferReason, long>();
        // commodity → last truck price (§) observed by Check, for edge-detecting a crossing. Absent = no baseline yet.
        private static readonly Dictionary<TransferManager.TransferReason, long> _lastPrice =
            new Dictionary<TransferManager.TransferReason, long>();
        // commodity → which side of the threshold we last fired on (true = at/above). Absent = armed (not fired since
        // the last re-arm), so the next crossing in either direction can fire.
        private static readonly Dictionary<TransferManager.TransferReason, bool> _firedSide =
            new Dictionary<TransferManager.TransferReason, bool>();

        /// <summary>The threshold (§/truck) set for a commodity, or 0 if none.</summary>
        public static long ThresholdOf(TransferManager.TransferReason commodity)
        {
            long v;
            return _thresholds.TryGetValue(commodity, out v) ? v : 0L;
        }

        /// <summary>MAIN THREAD. Set (or replace) a commodity's alert threshold in §/truck. A non-positive value
        /// clears it. Setting a threshold re-arms the one-shot (so it can fire on the next crossing).</summary>
        public static void Set(TransferManager.TransferReason commodity, long truckCents)
        {
            if (truckCents <= 0) { Clear(commodity); return; }
            _thresholds[commodity] = truckCents;
            _firedSide.Remove(commodity);   // re-arm: a freshly set threshold should alert on the next crossing
        }

        /// <summary>MAIN THREAD. Remove a commodity's alert threshold.</summary>
        public static void Clear(TransferManager.TransferReason commodity)
        {
            _thresholds.Remove(commodity);
            _firedSide.Remove(commodity);
        }

        /// <summary>MAIN THREAD. Compare every armed threshold against the live feed and Chirp once for each that
        /// the truck price has crossed since the previous Check. Gated on the Chirper opt-in. The Chirp is marshalled
        /// to the SIM thread. Re-arms a commodity when the price returns to the side it fired on.</summary>
        public static void Check()
        {
            if (_thresholds.Count == 0) return;

            // Snapshot keys first — we may mutate _lastPrice as we go.
            List<TransferManager.TransferReason> keys = new List<TransferManager.TransferReason>(_thresholds.Keys);
            SimulationManager sm = SimulationManager.instance;
            for (int i = 0; i < keys.Count; i++)
            {
                TransferManager.TransferReason c = keys[i];
                long threshold = _thresholds[c];
                long price = MarketFeed.Instance.PricePerTruckCents(c) / 100; // §/truck (the unit the threshold is in)

                long prev;
                bool hadPrev = _lastPrice.TryGetValue(c, out prev);
                _lastPrice[c] = price;
                if (!hadPrev) continue; // first observation seeds the baseline; never alert without a prior price

                bool nowAbove = price >= threshold;
                bool wasAbove = prev >= threshold;
                if (nowAbove == wasAbove) continue; // no crossing this tick

                // Re-arm tracking: only fire if we haven't already fired on THIS side since the last re-arm.
                bool fired;
                if (_firedSide.TryGetValue(c, out fired) && fired == nowAbove) continue;
                _firedSide[c] = nowAbove;

                if (!Settings.IsChirperAlerts || sm == null) continue;
                string name = Commodities.DisplayName(c);
                string text = nowAbove
                    ? name + " hit your §" + threshold.ToString("N0") + "/truck alert (now §" + price.ToString("N0") + ")."
                    : name + " fell below your §" + threshold.ToString("N0") + "/truck alert (now §" + price.ToString("N0") + ").";
                sm.AddAction(delegate { MarketChirper.Post(text); });
            }
        }

        // ---- persistence (SaveData §12) ----

        /// <summary>The thresholds as (commodity key, §/truck) pairs for the save blob. MAIN/SIM — read-only snapshot.</summary>
        public static List<KeyValuePair<string, long>> Entries()
        {
            List<KeyValuePair<string, long>> list = new List<KeyValuePair<string, long>>(_thresholds.Count);
            foreach (KeyValuePair<TransferManager.TransferReason, long> kv in _thresholds)
                list.Add(new KeyValuePair<string, long>(Commodities.Key(kv.Key), kv.Value));
            return list;
        }

        /// <summary>Restore one persisted threshold on load (unknown keys are skipped). Clean default = no alert.</summary>
        public static void Restore(string commodityKey, long truckCents)
        {
            TransferManager.TransferReason reason;
            if (truckCents > 0 && Commodities.TryFromKey(commodityKey, out reason))
                _thresholds[reason] = truckCents;
        }

        /// <summary>Drop the crossing baseline + fired flags (going offline). Thresholds are kept — they re-arm and
        /// re-seed their baseline on the next poll.</summary>
        public static void ResetRuntime()
        {
            _lastPrice.Clear();
            _firedSide.Clear();
        }

        /// <summary>Drop EVERYTHING — thresholds and runtime state (level unload). The persisted copy stays in the
        /// save, so the next load re-populates the thresholds for that city only.</summary>
        public static void Clear()
        {
            _thresholds.Clear();
            ResetRuntime();
        }
    }
}
