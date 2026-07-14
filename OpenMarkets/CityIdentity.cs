using System;

namespace OpenMarkets
{
    /// <summary>
    /// This save's stable <b>city token</b> — a per-CITY identity persisted inside our own <see cref="Persistence.SaveData"/>
    /// blob (NOT in the global settings file). Because the blob travels with every autosave / manual save / "Save As"
    /// of a city, all of a city's save files share one token; only a brand-new city mints a fresh one. The token lets
    /// the lifecycle decide whether the loaded save is the install's BOUND league city
    /// (<see cref="Settings.BoundCityToken"/>) so a DIFFERENT save can't hijack or overwrite the league city's
    /// server-side profile and economy ("one league city, no hijack").
    ///
    /// MAIN/SIM thread: <see cref="Restore"/> runs on load (sim thread, SaveData), <see cref="EnsureToken"/> at
    /// wire-time (main thread). A plain string is fine — it's only ever touched on those single-threaded hops, never
    /// from a hot path.
    /// </summary>
    public static class CityIdentity
    {
        private static string _token = string.Empty;

        /// <summary>This save's token, or empty if not yet generated (a solo session that never went online, or a
        /// save predating this feature until it next goes online).</summary>
        public static string Token { get { return _token; } }

        /// <summary>Restore the token read from the save blob. Empty/absent → stays empty (old save / new city);
        /// it is minted lazily the first time this city needs an online identity (<see cref="EnsureToken"/>).</summary>
        public static void Restore(string token) { _token = token ?? string.Empty; }

        /// <summary>Ensure a token exists, minting one the first time this city needs an online identity. Idempotent
        /// — once set it never changes for this save lineage (it then persists into every future autosave). Returns
        /// the token.</summary>
        public static string EnsureToken()
        {
            if (string.IsNullOrEmpty(_token)) _token = Guid.NewGuid().ToString("N");
            return _token;
        }

        /// <summary>Drop the in-memory token on level unload. The persisted copy stays in the save file, so the
        /// same city keeps its identity on reload; a different save brings its own (or none yet).</summary>
        public static void Clear() { _token = string.Empty; }
    }
}
