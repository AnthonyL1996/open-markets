using System.Collections.Generic;
using OpenMarkets.Data;
using OpenMarkets.Market;
using OpenMarkets.Net;
using OpenMarkets.Notify;
using UnityEngine;

namespace OpenMarkets.Trade
{
    /// <summary>
    /// M6 physical delivery: when one of my active trades advances an installment (server <c>settled</c> &gt; my
    /// local <see cref="DeliveryLedger"/> cursor), REMOVE my give-goods from my [trade] depots and ADD my
    /// receive-goods to them, then advance the cursor (idempotent across reload/overlapping polls). A give
    /// shortfall (depot ran dry) is reported to the server, which mints a cash-debt bond; receive overflow (no
    /// room / no matching depot) is lost with a Chirper warning. Driven by OnlineSync's delivery poll (MAIN
    /// THREAD); the warehouse mutation + cursor advance run on the SIM THREAD (marshalled).
    /// </summary>
    public static class TradeDelivery
    {
        // One commodity line of a trade from MY perspective: the FULL transfer-unit amount across the whole trade,
        // whether I give (remove) or receive (add) it, and the give's frozen value (for the shortfall report).
        // Per-installment deltas are derived in ApplyOnSimCore so a shortfall is attributed to the exact installment.
        private sealed class Line
        {
            public TransferManager.TransferReason Reason;
            public bool Give;
            public long TotalTU;
            public long ValueCents;     // give only — frozen value of the whole give line
        }

        // Installment we've already QUEUED a sim-thread delivery for, per trade (MAIN THREAD only). Guards against
        // a paused game: the poll keeps firing while the queued sim action can't run (so the DeliveryLedger cursor
        // hasn't advanced yet) — without this we'd queue the same installment twice and double-move goods.
        private static readonly Dictionary<string, int> _queued = new Dictionary<string, int>();

        /// <summary>Drop queued markers (level unload / server-epoch wipe). MAIN THREAD.</summary>
        public static void Reset() { _queued.Clear(); }

        /// <summary>MAIN THREAD. Deliver any installments that advanced since our cursor, for every trade I'm in.</summary>
        public static void ProcessDue(TradeDto[] trades)
        {
            if (trades == null) return;
            string me = Settings.AccountIdValue;
            if (string.IsNullOrEmpty(me)) return;

            for (int i = 0; i < trades.Length; i++)
            {
                TradeDto t = trades[i];
                if (t == null || t.items == null || t.installments <= 0) continue;
                if (t.status != "active" && t.status != "completed") continue; // completed: still deliver its final installment
                if (t.offeredBy != me && t.counterparty != me) continue;

                // Barrier = furthest installment already delivered OR queued for delivery, so a re-poll before the
                // queued sim action runs (paused game) can't re-deliver it. Once the action has run (the persisted
                // cursor caught up to the queued value) the transient marker is dropped so it can't leak.
                int barrier = DeliveryLedger.Processed(t.id);
                int q;
                if (_queued.TryGetValue(t.id, out q))
                {
                    if (barrier >= q) _queued.Remove(t.id);   // sim action ran — marker no longer needed
                    else barrier = q;                          // still pending — hold the barrier here
                }
                if (t.settled <= barrier) continue;   // nothing new to deliver
                ProcessTrade(t, me, barrier);
            }
        }

        private static void ProcessTrade(TradeDto t, string me, int processed)
        {
            bool flip = t.counterparty == me;           // item.dir is relative to offeredBy
            int inst = t.installments;
            int fromInst = processed, toInst = t.settled; // deliver installments [processed, settled)

            List<Line> lines = new List<Line>();
            for (int k = 0; k < t.items.Length; k++)
            {
                LineItemDto it = t.items[k];
                if (it == null || it.kind != "commodity") continue;
                TransferManager.TransferReason reason;
                if (!Commodities.TryFromKey(it.commodity, out reason)) continue;

                string dir = it.dir;
                if (flip) dir = dir == "give" ? "take" : "give";

                long totalTU = (it.qtyFixed / Money.QtyScale) * InventoryService.TransferUnitsPerUnit;
                if (totalTU <= 0L) continue;

                Line ln = new Line();
                ln.Reason = reason;
                ln.Give = dir == "give";
                ln.TotalTU = totalTU;
                ln.ValueCents = ln.Give ? it.valueCentsAtAccept : 0L;
                lines.Add(ln);
            }

            if (lines.Count == 0) { DeliveryLedger.Advance(t.id, t.settled); return; }

            string tradeId = t.id;
            SimulationManager sm = SimulationManager.instance;
            if (sm == null) return;                      // odd teardown window — retry next poll (nothing queued)
            _queued[tradeId] = toInst;                   // mark queued so a re-poll can't double-deliver
            sm.AddAction(delegate { ApplyOnSim(tradeId, fromInst, toInst, inst, lines); });
        }

        // SIM THREAD wrapper. Guarded so an unexpected throw can't break the simulation loop.
        private static void ApplyOnSim(string tradeId, int fromInst, int toInst, int inst, List<Line> lines)
        {
            try { ApplyOnSimCore(tradeId, fromInst, toInst, inst, lines); }
            catch (System.Exception e) { Log.Error("TradeDelivery.ApplyOnSim failed: " + e); }
        }

        // SIM THREAD: deliver each installment in [fromInst, toInst) SEPARATELY, so a give shortfall is reported
        // under the exact installment it occurred on (the server caps per installment). Receive goods fill the
        // [trade] depots, overflow spills to untagged warehouses of the commodity, and only the true remainder is
        // lost with a Chirp. The cursor advances atomically with the moves (a CS1 save is one sim-thread pass).
        private static void ApplyOnSimCore(string tradeId, int fromInst, int toInst, int inst, List<Line> lines)
        {
            bool receivedGoods = false;   // M8 lever #3: did any receive-goods actually land in the city this event?
            for (int n = fromInst; n < toInst; n++)
            {
                long undeliveredCents = 0L;
                for (int i = 0; i < lines.Count; i++)
                {
                    Line ln = lines[i];
                    long deltaTU = ln.TotalTU * (n + 1) / inst - ln.TotalTU * n / inst; // installment n's share
                    if (deltaTU <= 0L) continue;

                    if (ln.Give)
                    {
                        long removed = -InventoryService.MoveStock(ln.Reason, -deltaTU); // signed (negative) moved
                        long shortTU = deltaTU - removed;
                        if (shortTU > 0L) undeliveredCents += ln.ValueCents * shortTU / ln.TotalTU;
                    }
                    else
                    {
                        long added = InventoryService.MoveStock(ln.Reason, deltaTU);          // [trade] depots first
                        long landed = added;
                        long overflowTU = deltaTU - added;
                        if (overflowTU > 0L)
                        {
                            long spilled = InventoryService.SpillToUntagged(ln.Reason, overflowTU); // then other warehouses
                            landed += spilled;
                            long lostTU = overflowTU - spilled;
                            if (lostTU > 0L)
                                MarketChirper.Post("No warehouse room — " + (lostTU / InventoryService.TransferUnitsPerUnit)
                                    + " " + Commodities.DisplayName(ln.Reason) + " from a trade was lost.");
                        }
                        if (landed > 0L) receivedGoods = true;
                    }
                }

                if (undeliveredCents > 0L)
                {
                    long cents = undeliveredCents;       // capture per-installment locals for the marshalled call
                    int installment = n;
                    OmHttp.OnMainThread(delegate { OmApi.ReportShortfall(tradeId, installment, cents, delegate (bool ok) { }); });
                }
            }

            // M8 lever #3 (delivery→demand): imported inputs landing in the city lift local commercial/industrial
            // demand. One flat increment per delivery event (capped + decaying in DeliveryStimulus); sim thread.
            if (receivedGoods) DeliveryStimulus.OnDelivery();

            InventoryService.Scan();                 // refresh the inventory snapshot to reflect the moved stock
            DeliveryLedger.Advance(tradeId, toInst);
        }
    }
}
