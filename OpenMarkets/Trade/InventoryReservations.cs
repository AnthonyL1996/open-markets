using System.Collections.Generic;
using OpenMarkets.Data;
using OpenMarkets.Market;
using OpenMarkets.Net;

namespace OpenMarkets.Trade
{
    /// <summary>
    /// How much of each commodity the player has still COMMITTED to give on active trades — the reservation that
    /// reduces tradeable inventory so you can't double-promise the same stock, and that the driver protects from
    /// auto-export. Reserves the REMAINING (undelivered) give share, i.e. give × (installments − settled) /
    /// installments — Phase-2 delivery drains depots as installments settle, so only the not-yet-delivered part is
    /// still reserved. Whole commodity units.
    /// </summary>
    public static class InventoryReservations
    {
        /// <summary>Remaining undelivered give units of <paramref name="reason"/> across the player's trades.</summary>
        public static long ReservedUnits(TradeDto[] trades, string me, TransferManager.TransferReason reason)
        {
            if (trades == null || string.IsNullOrEmpty(me)) return 0L;
            string key = Commodities.Key(reason);
            long reserved = 0L;
            for (int i = 0; i < trades.Length; i++)
            {
                TradeDto t = trades[i];
                if (!Reservable(t, me, out int undelivered)) continue;
                bool flip = t.counterparty == me;   // item.dir is relative to offeredBy
                for (int k = 0; k < t.items.Length; k++)
                {
                    LineItemDto it = t.items[k];
                    if (it == null || it.kind != "commodity" || it.commodity != key) continue;
                    string dir = it.dir;
                    if (flip) dir = dir == "give" ? "take" : "give";
                    if (dir != "give") continue;     // only what I GIVE reserves my own stock
                    reserved += (it.qtyFixed / Money.QtyScale) * undelivered / t.installments;
                }
            }
            return reserved;
        }

        /// <summary>Remaining-give per commodity (whole units) across all my trades — for publishing the driver's
        /// reserved snapshot in one pass.</summary>
        public static Dictionary<TransferManager.TransferReason, long> ReservedByCommodity(TradeDto[] trades, string me)
        {
            Dictionary<TransferManager.TransferReason, long> map = new Dictionary<TransferManager.TransferReason, long>();
            if (trades == null || string.IsNullOrEmpty(me)) return map;
            for (int i = 0; i < trades.Length; i++)
            {
                TradeDto t = trades[i];
                if (!Reservable(t, me, out int undelivered)) continue;
                bool flip = t.counterparty == me;
                for (int k = 0; k < t.items.Length; k++)
                {
                    LineItemDto it = t.items[k];
                    if (it == null || it.kind != "commodity") continue;
                    string dir = it.dir;
                    if (flip) dir = dir == "give" ? "take" : "give";
                    if (dir != "give") continue;
                    TransferManager.TransferReason reason;
                    if (!Commodities.TryFromKey(it.commodity, out reason)) continue;
                    long add = (it.qtyFixed / Money.QtyScale) * undelivered / t.installments;
                    long cur; map.TryGetValue(reason, out cur); map[reason] = cur + add;
                }
            }
            return map;
        }

        // A trade reserves stock for the installments NOT YET physically delivered (DeliveryLedger.Processed), not
        // merely those not yet cash-settled — so stock stays protected until it actually leaves the depot (else the
        // driver could export between cash-settle and the slightly-later delivery). `undelivered` = installments
        // minus the local delivery cursor. Only binding trades (active or cash-completed-but-goods-pending) count.
        private static bool Reservable(TradeDto t, string me, out int undelivered)
        {
            undelivered = 0;
            if (t == null || t.items == null || t.installments <= 0) return false;
            if (t.offeredBy != me && t.counterparty != me) return false;
            if (t.status != "active" && t.status != "completed") return false; // offered/declined/cancelled → no obligation
            int processed = DeliveryLedger.Processed(t.id);
            if (processed >= t.installments) return false;                     // fully delivered already
            undelivered = t.installments - processed;
            return true;
        }
    }
}
