using OpenMarkets.Net;

namespace OpenMarkets.Market
{
    /// <summary>
    /// Client-side helpers over a <see cref="TradeDto"/>'s frozen line values — who owes whom, and the net cash.
    /// Mirrors the server (store.Trade.OffererNetCents / NetPayerReceiver) so the Inbox/auto-settle only fire a
    /// /settle when THIS city is the net payer (the server rejects anyone else), avoiding wasted 409s. Pure
    /// (DTOs + <see cref="BasketValuation"/> only, no Unity), so it is unit-testable off the game.
    /// </summary>
    public static class TradeMath
    {
        /// <summary>Signed cents to the offerer across all (frozen) lines (+ = offerer nets positive).</summary>
        public static long OffererNetCents(LineItemDto[] items)
        {
            if (items == null) return 0;
            long net = 0;
            for (int i = 0; i < items.Length; i++)
            {
                LineItemDto li = items[i];
                if (li == null) continue;
                net += BasketValuation.FlowToOfferer(li.kind == "gold", li.dir == "give", li.valueCentsAtAccept);
            }
            return net;
        }

        /// <summary>Value (cents) of ONE line: the server's frozen accept value if set (accepted trade), else an
        /// INDICATIVE value from <paramref name="priceOf"/> — needed because an un-accepted OFFER has
        /// valueCentsAtAccept = 0, so the frozen path would value the whole offer at §0.</summary>
        public static long LineValueCents(LineItemDto li, System.Func<string, long> priceOf)
        {
            if (li == null) return 0;
            if (li.valueCentsAtAccept > 0) return li.valueCentsAtAccept; // frozen at accept — authoritative
            if (li.kind == "gold") return li.goldCents;
            long price = priceOf != null ? priceOf(li.commodity) : 0;
            return BasketValuation.IndicativeCommodityCents(li.qtyFixed, price);
        }

        /// <summary>Gross deal value (cents): the sum of every line's value (frozen or indicative) — the "size" of the
        /// deal on the table, regardless of who nets ahead.</summary>
        public static long GrossCents(LineItemDto[] items, System.Func<string, long> priceOf)
        {
            if (items == null) return 0;
            long gross = 0;
            for (int i = 0; i < items.Length; i++) gross += LineValueCents(items[i], priceOf);
            return gross;
        }

        /// <summary>Signed cents to the offerer, valuing each line via <paramref name="priceOf"/> (frozen when set,
        /// else indicative). Use this for OFFERED trades, where the frozen <see cref="OffererNetCents(LineItemDto[])"/>
        /// would read 0.</summary>
        public static long OffererNetCents(LineItemDto[] items, System.Func<string, long> priceOf)
        {
            if (items == null) return 0;
            long net = 0;
            for (int i = 0; i < items.Length; i++)
            {
                LineItemDto li = items[i];
                if (li == null) continue;
                net += BasketValuation.FlowToOfferer(li.kind == "gold", li.dir == "give", LineValueCents(li, priceOf));
            }
            return net;
        }

        /// <summary>The party who pays the net cash each installment: if the offerer nets positive the
        /// counterparty pays, else the offerer pays. (For a perfectly balanced trade the counterparty is the
        /// nominal payer of a zero amount.)</summary>
        public static string NetPayer(TradeDto t)
        {
            if (t == null) return string.Empty;
            return OffererNetCents(t.items) >= 0 ? t.counterparty : t.offeredBy;
        }

        /// <summary>True if accountID is the net payer of this trade (the only party the server lets settle).</summary>
        public static bool IsNetPayer(TradeDto t, string accountID)
        {
            return !string.IsNullOrEmpty(accountID) && NetPayer(t) == accountID;
        }
    }
}
