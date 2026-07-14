using System;
using OpenMarkets.Net;

namespace OpenMarkets.Market
{
    /// <summary>
    /// Books the server-authored settlement feed (<c>GET /settlements</c>) into in-game cash. The server emits
    /// one immutable, monotonically-sequenced <see cref="SettlementEventDto"/> per booked trade/bond
    /// installment; this catches the local treasury up to events we haven't booked yet — crediting when I'm the
    /// receiver, debiting when I'm the payer (an event between two OTHER members just advances our cursor). This
    /// is the conservation-safe settlement model (Codex #3): both parties book from the SAME ordered event log.
    ///
    /// THREADING: the poll + this entry run on the MAIN thread, the
    /// cash booking is marshalled to the SIM thread, and idempotency is enforced there via the persisted
    /// <see cref="SettlementLedger"/> cursor — so overlapping polls or a save/reload book each event exactly once.
    /// </summary>
    public static class SettlementReconciler
    {
        /// <summary>Book a batch of events for a league. MAIN THREAD entry; marshals the cash to the sim thread.
        /// No-op for empty input. The events are processed in ascending seq so the monotonic cursor advances
        /// correctly even if the server returned them unordered.</summary>
        public static void Book(string leagueId, SettlementEventDto[] events)
        {
            if (string.IsNullOrEmpty(leagueId) || events == null || events.Length == 0) return;

            Array.Sort(events, CompareSeq); // defensive: the server already sorts ascending

            string me = Settings.AccountIdValue;
            string epoch = SettlementLedger.ServerEpoch; // capture now; a wipe between queue and run invalidates this batch
            SimulationManager sm = SimulationManager.instance;
            if (sm == null) return;
            sm.AddAction(delegate
            {
                // A server wipe (epoch change) happened after this batch was queued → these are stale old-epoch
                // events; dropping them avoids booking old cash + advancing the cursor past fresh post-wipe seqs.
                if (SettlementLedger.ServerEpoch != epoch) return;
                for (int i = 0; i < events.Length; i++)
                {
                    SettlementEventDto e = events[i];
                    if (e == null || e.seq <= SettlementLedger.LastSeq(leagueId)) continue; // already booked
                    int cents = ClampCents(e.cents);
                    if (cents > 0)
                    {
                        if (e.receiverId == me) PricingService.BookContractSettlement(cents, true);
                        else if (e.payerId == me) PricingService.BookContractSettlement(cents, false);
                        // else: a settlement between two other members — not our cash; only the cursor advances.
                    }
                    SettlementLedger.Advance(leagueId, e.seq);
                }
            });
        }

        private static int CompareSeq(SettlementEventDto x, SettlementEventDto y)
        {
            long xs = x != null ? x.seq : 0;
            long ys = y != null ? y.seq : 0;
            return xs.CompareTo(ys);
        }

        private static int ClampCents(long cents)
        {
            if (cents <= 0) return 0;
            return cents > int.MaxValue ? int.MaxValue : (int)cents;
        }
    }
}
