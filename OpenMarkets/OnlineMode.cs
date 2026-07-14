namespace OpenMarkets
{
    /// <summary>
    /// Whether the mod is currently operating online — i.e. a live remote price feed is wired. Set by
    /// <see cref="OpenMarketsLoading.WirePriceSource"/> / <see cref="OpenMarketsLoading.StopOnlineFeed"/>
    /// on the main thread; readable from anywhere. A single volatile bool: the same lock-free snapshot
    /// pattern the price/event snapshots use, so a sim-thread read can't tear.
    ///
    /// Gates online-only economic rules. The first is import charging: a shared price feed only balances
    /// if BOTH trade legs settle in cash, so online play forces imports to be charged regardless of the
    /// saved toggle — see <see cref="Settings.IsChargeImports"/>.
    /// </summary>
    public static class OnlineMode
    {
        private static volatile bool _active;

        /// <summary>True when an online price feed is wired (online prices enabled and a city is loaded).</summary>
        public static bool IsActive { get { return _active; } }

        /// <summary>Set the online-active state. Owned by the lifecycle when the price source is (un)wired.</summary>
        public static void SetActive(bool active) { _active = active; }
    }
}
