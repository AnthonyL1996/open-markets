namespace OpenMarkets.Market
{
    /// <summary>
    /// The seam (PLAN §2) between "what does a commodity sell for right now" and the rest of the mod.
    /// <see cref="MarketFeed"/> is the M9 implementation (server index when online, static base prices solo),
    /// installed via <see cref="PricingService.SetSource"/> — all without touching the patches or UI.
    /// </summary>
    public interface IPriceSource
    {
        /// <summary>
        /// Per-unit price in the SAME scaled unit as vanilla <c>IndustryBuildingAI.GetResourcePrice</c>
        /// (e.g. Ore = 300). <see cref="PricingService"/> turns it into treasury money with
        /// <c>(units * price + 50) / 100</c> cents, so price 300 ≈ §0.03/unit. M9: ONE price per commodity
        /// (the league market index) — no per-partner pricing.
        /// </summary>
        int GetPrice(TransferManager.TransferReason commodity);
    }
}
