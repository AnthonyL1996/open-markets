using System.Collections.Generic;

namespace OpenMarkets.Market
{
    /// <summary>
    /// Per-save cursor of the highest trade installment THIS city has already physically PROCESSED (give-goods
    /// removed from depots + receive-goods added) per trade. The double-delivery guard for M6 give/receive
    /// physical delivery: the server advances <c>settled</c> (cash), and the client drains/fills its own depots
    /// once per newly-settled installment — so we must remember how far we've delivered across save/reload and
    /// overlapping polls, exactly like <see cref="SettlementLedger"/> does for cash. LOCK-GUARDED (the poll reads
    /// on the MAIN thread; <see cref="Advance"/> runs on the SIM thread alongside the warehouse mutation).
    /// Persisted as SaveData Section 8 (v5). Keyed by trade id.
    /// </summary>
    public static class DeliveryLedger
    {
        private static readonly object _gate = new object();
        private static readonly Dictionary<string, int> _processed = new Dictionary<string, int>();

        /// <summary>Installments already delivered for <paramref name="tradeId"/> (0 if none). Any-thread safe.</summary>
        public static int Processed(string tradeId)
        {
            if (string.IsNullOrEmpty(tradeId)) return 0;
            lock (_gate)
            {
                int n;
                return _processed.TryGetValue(tradeId, out n) ? n : 0;
            }
        }

        /// <summary>Record that installments up to <paramref name="installment"/> are delivered. Monotonic —
        /// ignores a lower value so an overlapping poll can't rewind. Returns true if this advanced it.</summary>
        public static bool Advance(string tradeId, int installment)
        {
            if (string.IsNullOrEmpty(tradeId)) return false;
            lock (_gate)
            {
                int cur;
                _processed.TryGetValue(tradeId, out cur);
                if (installment <= cur) return false;
                _processed[tradeId] = installment;
                return true;
            }
        }

        /// <summary>Restore a persisted cursor (SaveData load).</summary>
        public static void Restore(string tradeId, int installment)
        {
            if (string.IsNullOrEmpty(tradeId) || installment <= 0) return;
            lock (_gate) { _processed[tradeId] = installment; }
        }

        /// <summary>Snapshot for serialization (SaveData save).</summary>
        public static List<KeyValuePair<string, int>> Entries()
        {
            lock (_gate) { return new List<KeyValuePair<string, int>>(_processed); }
        }

        /// <summary>Drop all delivery cursors — a server data wipe (epoch changed), mirroring
        /// <see cref="SettlementLedger.ResetAll"/>; the fresh economy re-delivers from 0.</summary>
        public static void ResetAll() { lock (_gate) { _processed.Clear(); } }

        public static int Count { get { lock (_gate) { return _processed.Count; } } }

        /// <summary>Clear all cursors (level unload). SaveData restores per-save.</summary>
        public static void Clear() { lock (_gate) { _processed.Clear(); } }
    }
}
