using System.Collections.Generic;

namespace OpenMarkets.Market
{
    /// <summary>
    /// City net-trade volume reporter. (This WAS the local price simulation; M9 moved the price model entirely to
    /// the server — see <see cref="MarketFeed"/> — so all that survives is the per-commodity net-supply accumulator
    /// that feeds the daily <c>/report</c>, which in turn drives the server's elasticity. The class keeps its old
    /// name only to avoid churning its few call sites.) Sim thread only; drained on the daily report cadence.
    /// </summary>
    public sealed class LocalPriceSim
    {
        /// <summary>Shared instance (the cargo patches, the day tick, and SaveData all use this one).</summary>
        public static readonly LocalPriceSim Instance = new LocalPriceSim();
        private LocalPriceSim() { }

        // Per-COMMODITY signed net units since the last /report drain: export = +supply, import = -supply (demand),
        // matching the backend sign. Sim thread only (written from the cargo-arrival attribution, drained on the
        // day rollover). Not persisted — it's a within-session running delta.
        private readonly Dictionary<TransferManager.TransferReason, long> _reportAccum =
            new Dictionary<TransferManager.TransferReason, long>();

        /// <summary>Record one delivery's net volume (sim thread, from the cargo-arrival attribution). Accumulated
        /// ONLY while online, so a long offline session doesn't dump stale net into the first online report.</summary>
        public void RecordVolume(TransferManager.TransferReason commodity, int units, bool isExport)
        {
            if (units <= 0 || !OnlineMode.IsActive) return;
            long ra;
            _reportAccum.TryGetValue(commodity, out ra);
            _reportAccum[commodity] = ra + (isExport ? units : -units);
        }

        /// <summary>Drain + clear the accumulated per-commodity net supply for the daily /report.</summary>
        public List<KeyValuePair<TransferManager.TransferReason, long>> DrainReportAccumulator()
        {
            List<KeyValuePair<TransferManager.TransferReason, long>> rows =
                new List<KeyValuePair<TransferManager.TransferReason, long>>(_reportAccum.Count);
            foreach (KeyValuePair<TransferManager.TransferReason, long> kv in _reportAccum) rows.Add(kv);
            _reportAccum.Clear();
            return rows;
        }

        /// <summary>Drop state on level unload.</summary>
        public void Clear() { _reportAccum.Clear(); }
    }
}
