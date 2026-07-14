using System.Collections.Generic;
using OpenMarkets.Net;

namespace OpenMarkets
{
    /// <summary>
    /// Client-side cache of the current league's roster: account id → display name, plus the league's own
    /// name. Populated whenever a <c>/leagues/members</c> response arrives (the Members tab's refresh and
    /// <see cref="OnlineSync"/>'s periodic roster poll). Read by the Inbox / Contracts / terminal / Chirper so
    /// they can show a friendly name instead of a raw account id. The server is the source of truth; this is a
    /// read-through display cache. MAIN THREAD only (written and read from main-thread UI/HTTP callbacks).
    /// </summary>
    public static class LeagueRoster
    {
        private static readonly Dictionary<string, string> _names = new Dictionary<string, string>();
        // accountId → PRIMARY title (one per account, picked by priority). Populated from the /leaderboards poll.
        // A title TRAVELS into every UI that resolves a name through Display() — inbox, trade composer, members,
        // standings — so a Market Baron reads as "Townsville  [Market Baron]" everywhere.
        private static readonly Dictionary<string, string> _titles = new Dictionary<string, string>();
        private static readonly List<string> _memberIds = new List<string>(); // roster order (owner first)
        private static string _leagueId = string.Empty;
        private static string _leagueName = string.Empty;

        // Title priority (first match wins): a city that holds several titles shows only its most prestigious/
        // notable one. Mirrors the server's award set; "Bankrupt"/"Deadbeat" lead so shame outranks glory.
        private static readonly string[] TitlePriority =
        {
            "Bankrupt", "Deadbeat", "Market Baron", "Phoenix", "Patron",
            "Master Builder", "Top Trader", "Market Mover", "Good Credit", "Metropolis"
        };

        /// <summary>The current league's display name, or empty if not yet fetched.</summary>
        public static string LeagueName { get { return _leagueName; } }

        /// <summary>Snapshot of the current league's member account ids (roster order). Copy, so callers can't
        /// mutate the cache. Used by the offer form to pick a counterparty.</summary>
        public static List<string> MemberIds() { return new List<string>(_memberIds); }

        /// <summary>Merge a fresh roster into the cache. A roster for a DIFFERENT league than the one cached
        /// (the player switched leagues) resets the id→name map first, so stale names can't leak across.</summary>
        public static void Update(MembersDto dto)
        {
            if (dto == null) return;
            if (dto.leagueId != _leagueId) { _names.Clear(); _leagueId = dto.leagueId ?? string.Empty; }
            if (!string.IsNullOrEmpty(dto.name)) _leagueName = dto.name;
            if (dto.members == null) return;
            // Rebuild the member-id list from this roster (membership can change between polls).
            _memberIds.Clear();
            for (int i = 0; i < dto.members.Length; i++)
            {
                MemberDto m = dto.members[i];
                if (m == null || string.IsNullOrEmpty(m.accountId)) continue;
                _memberIds.Add(m.accountId);
                // Only OVERWRITE with a non-empty name. A member who hasn't posted a city profile yet comes back
                // with an empty displayName; we must NOT drop a name we already learned (that flickered the row
                // back to a raw id between polls). Once known, a name sticks until a newer non-empty one replaces it.
                if (!string.IsNullOrEmpty(m.displayName)) _names[m.accountId] = m.displayName;
            }
        }

        /// <summary>The friendly name for an id if we know one, else a readable placeholder (never null/empty).
        /// We deliberately DON'T surface the raw account id in the UI — a leaguemate who hasn't published a city
        /// profile yet (or whose roster hasn't been polled this session) reads as "unnamed city" rather than a
        /// hex blob. Your own name resolves within a tick of going online (the profile is posted on connect).</summary>
        public static string Display(string accountId)
        {
            string name = "unnamed city";
            if (!string.IsNullOrEmpty(accountId))
            {
                string known;
                if (_names.TryGetValue(accountId, out known) && !string.IsNullOrEmpty(known)) name = known;
            }
            string title = TitleOf(accountId);
            return string.IsNullOrEmpty(title) ? name : name + "  [" + title + "]";
        }

        /// <summary>The single primary title an account currently holds, or "" if none.</summary>
        public static string TitleOf(string accountId)
        {
            if (string.IsNullOrEmpty(accountId)) return string.Empty;
            string title;
            return _titles.TryGetValue(accountId, out title) ? title : string.Empty;
        }

        /// <summary>Replace the traveling-title map from a /leaderboards response. Each account's primary title is
        /// the first <see cref="TitlePriority"/> entry it holds (shame outranks glory). Rebuilt wholesale each
        /// poll so a lost title disappears.</summary>
        public static void SetTitles(TitleEntryDto[] entries)
        {
            _titles.Clear();
            if (entries == null) return;
            for (int i = 0; i < entries.Length; i++)
            {
                TitleEntryDto e = entries[i];
                if (e == null || string.IsNullOrEmpty(e.accountId) || e.titles == null) continue;
                string primary = PickPrimary(e.titles);
                if (!string.IsNullOrEmpty(primary)) _titles[e.accountId] = primary;
            }
        }

        // The most prestigious/notable title in `held`, by TitlePriority (first match wins). "" if none match.
        private static string PickPrimary(string[] held)
        {
            for (int p = 0; p < TitlePriority.Length; p++)
            {
                string want = TitlePriority[p];
                for (int h = 0; h < held.Length; h++)
                    if (held[h] == want) return want;
            }
            return string.Empty;
        }

        /// <summary>Drop everything (going offline / level unload).</summary>
        public static void Clear()
        {
            _names.Clear();
            _titles.Clear();
            _memberIds.Clear();
            _leagueId = string.Empty;
            _leagueName = string.Empty;
        }
    }
}
