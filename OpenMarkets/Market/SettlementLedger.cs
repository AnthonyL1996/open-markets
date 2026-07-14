using System.Collections.Generic;

namespace OpenMarkets.Market
{
    /// <summary>
    /// Per-save cursor of the highest settlement-event sequence THIS city has already booked in cash, per
    /// league. The double-book guard for the server-authored settlement feed (trade/bond installments): the
    /// server emits monotonic events, but the actual cash is booked locally, so we must remember how far we've
    /// booked across save/reload and overlapping polls. Is
    /// LOCK-GUARDED because reconciliation spans threads — the poll reads <see cref="LastSeq"/> on the MAIN
    /// thread to build the <c>?since=</c> request, while <see cref="Advance"/> runs on the SIM thread alongside
    /// the cash booking. Persisted as SaveData Section 7 (v3).
    /// </summary>
    public static class SettlementLedger
    {
        private static readonly object _gate = new object();
        private static readonly Dictionary<string, long> _seq = new Dictionary<string, long>();
        // The server data epoch we last synced to. When the server reports a different epoch, its data was wiped
        // (the only time replaying from 0 is safe — no old events to double-book). Persisted with the cursors.
        private static string _serverEpoch = string.Empty;

        /// <summary>Highest event seq booked for <paramref name="leagueId"/> (0 if none). Any-thread safe.</summary>
        public static long LastSeq(string leagueId)
        {
            if (string.IsNullOrEmpty(leagueId)) return 0;
            lock (_gate)
            {
                long s;
                return _seq.TryGetValue(leagueId, out s) ? s : 0;
            }
        }

        /// <summary>Record that events up to <paramref name="seq"/> are booked. Monotonic — ignores a lower
        /// value (so overlapping polls can't rewind the cursor). Returns true if this advanced it.</summary>
        public static bool Advance(string leagueId, long seq)
        {
            if (string.IsNullOrEmpty(leagueId)) return false;
            lock (_gate)
            {
                long cur;
                _seq.TryGetValue(leagueId, out cur);
                if (seq <= cur) return false;
                _seq[leagueId] = seq;
                return true;
            }
        }

        /// <summary>The server data epoch this client last synced to ("" if none yet). Any-thread safe.</summary>
        public static string ServerEpoch { get { lock (_gate) { return _serverEpoch; } } }

        /// <summary>Adopt the server's epoch (first sight, or after a detected wipe). Any-thread safe.</summary>
        public static void SetServerEpoch(string epoch) { lock (_gate) { _serverEpoch = epoch ?? string.Empty; } }

        /// <summary>Clear ALL league cursors — a genuine server data wipe (epoch changed). The next polls re-fetch
        /// from 0 and book the fresh server economy. Any-thread safe.</summary>
        public static void ResetAll() { lock (_gate) { _seq.Clear(); } }

        /// <summary>Restore a persisted cursor (SaveData load).</summary>
        public static void Restore(string leagueId, long seq)
        {
            if (string.IsNullOrEmpty(leagueId) || seq <= 0) return;
            lock (_gate) { _seq[leagueId] = seq; }
        }

        /// <summary>Snapshot for serialization (SaveData save).</summary>
        public static List<KeyValuePair<string, long>> Entries()
        {
            lock (_gate) { return new List<KeyValuePair<string, long>>(_seq); }
        }

        public static int Count { get { lock (_gate) { return _seq.Count; } } }

        /// <summary>Clear all cursors and the synced epoch (level unload). SaveData restores both per-save.</summary>
        public static void Clear() { lock (_gate) { _seq.Clear(); _serverEpoch = string.Empty; } }
    }
}
