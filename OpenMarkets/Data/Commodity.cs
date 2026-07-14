namespace OpenMarkets.Data
{
    /// <summary>Which DLC a commodity needs to exist in a game (base game = always available).</summary>
    public enum CommodityTier
    {
        Base,
        Industries,     // Industries DLC: AnimalProducts/Flours/Paper/PlanedTimber/Petroleum/Plastics/Glass/Metals/LuxuryProducts
        SunsetHarbor,   // Sunset Harbor DLC: Fish
    }

    /// <summary>
    /// One tradable commodity: a vanilla <see cref="TransferManager.TransferReason"/> plus the display
    /// name (for the dashboard) and the anchor/base price (in the scaled unit vanilla GetResourcePrice
    /// uses — see <see cref="OpenMarkets.Market.PricingService"/> for the cents conversion). The single
    /// source of truth for "what can be traded", replacing the price table that used to live in
    /// LocalPriceSim. <see cref="Tier"/> drives DLC-conditional registration later (M2).
    /// </summary>
    public sealed class Commodity
    {
        public TransferManager.TransferReason Reason { get; private set; }
        public string DisplayName { get; private set; }
        public int BasePrice { get; private set; }
        public CommodityTier Tier { get; private set; }

        public Commodity(TransferManager.TransferReason reason, string displayName, int basePrice, CommodityTier tier)
        {
            Reason = reason;
            DisplayName = displayName;
            BasePrice = basePrice;
            Tier = tier;
        }
    }
}
