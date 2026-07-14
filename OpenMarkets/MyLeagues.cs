using OpenMarkets.Net;

namespace OpenMarkets
{
    /// <summary>
    /// Client cache of the leagues this account belongs to, backing the in-game league switcher (gap 4.7).
    /// Populated by <see cref="OnlineSync"/>'s periodic poll of <c>GET /leagues</c>. The active league is
    /// still <see cref="Settings.LeagueId"/>; this only enumerates the choices. MAIN THREAD only.
    /// </summary>
    public static class MyLeagues
    {
        private static LeagueSummaryDto[] _leagues = new LeagueSummaryDto[0];

        /// <summary>How many leagues the account is in (0 until first fetched).</summary>
        public static int Count { get { return _leagues.Length; } }

        public static void Update(MyLeaguesDto dto)
        {
            _leagues = (dto != null && dto.leagues != null) ? dto.leagues : new LeagueSummaryDto[0];
        }

        /// <summary>The league id AFTER the current active one (wrapping), or empty when there's nothing to
        /// switch to (fewer than two leagues). If the active league isn't in the list yet, returns the first.</summary>
        public static string NextLeagueId()
        {
            if (_leagues.Length < 2) return string.Empty;
            string current = Settings.LeagueIdValue;
            int idx = -1;
            for (int i = 0; i < _leagues.Length; i++)
                if (_leagues[i] != null && _leagues[i].leagueId == current) { idx = i; break; }
            LeagueSummaryDto next = _leagues[(idx + 1) % _leagues.Length]; // idx == -1 → first
            return next != null ? next.leagueId : string.Empty;
        }

        public static void Clear() { _leagues = new LeagueSummaryDto[0]; }
    }
}
