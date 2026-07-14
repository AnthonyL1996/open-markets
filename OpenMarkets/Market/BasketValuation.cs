namespace OpenMarkets.Market
{
    /// <summary>
    /// Client-side INDICATIVE valuation for the trade-basket composer preview. The server freezes the real
    /// per-line value at accept (base price × the league's live index); the client can't see the index while
    /// composing, so it previews at a neutral index of 1.0 and labels the totals "indicative". Pure integer
    /// math (only <see cref="Money"/> + primitives — no Unity), so it is unit-testable off the game.
    ///
    /// Mirrors the server's two rules: a commodity line is worth qty × base price, and each line's signed cash
    /// flow to the OFFERER follows the give/take direction (commodity and gold flow OPPOSITE ways, TRADE-SCREEN
    /// §9.4a) — kept in lockstep with store.LineItem.CashFlowToOfferer.
    /// </summary>
    public static class BasketValuation
    {
        /// <summary>Scaled-index-unit → cents divisor (matches the server: contract cents = price / 100).</summary>
        public const long PriceScale = 100L;

        /// <summary>Indicative value (cents) of a commodity line: qtyFixed × basePrice / (QtyScale × PriceScale),
        /// rounded half-up, with a neutral index. Non-positive inputs → 0.</summary>
        public static long IndicativeCommodityCents(long qtyFixed, long basePrice)
        {
            if (qtyFixed <= 0 || basePrice <= 0) return 0;
            long denom = Money.QtyScale * PriceScale;
            return (qtyFixed * basePrice + denom / 2) / denom; // round half-up
        }

        /// <summary>Signed cents this line moves toward the offerer: commodity give = +value (sells), take =
        /// −value (buys); gold give = −value (pays), take = +value (receives).</summary>
        public static long FlowToOfferer(bool isGold, bool isGive, long valueCents)
        {
            if (isGold) return isGive ? -valueCents : valueCents;
            return isGive ? valueCents : -valueCents;
        }
    }
}
