using OpenMarkets.Notify;

namespace OpenMarkets
{
    /// <summary>
    /// M8 City Lever #3 — DELIVERY → DEMAND (the M6 payoff, co-op flavor, NO debuff). When one of my trades
    /// physically delivers receive-goods into my [trade] depots (<see cref="OpenMarkets.Trade.TradeDelivery"/>), the
    /// imported inputs stimulate the local economy: commercial + industrial/office demand gets a bounded, decaying
    /// lift. Keeping imports flowing keeps the lift topped up; a lull lets it decay. This is the elegant synthesis
    /// the lever design calls for — a fulfilled trade *rewards* the receiver through the market, not a raw buff applied
    /// from outside.
    ///
    /// Unlike the austerity levers this is EVENT-driven (a delivery), not state-driven (austerity on/off), and it is a
    /// BENEFIT — so it needs none of the inflicted-effect guardrails. Like the demand slump it is **fully transient**:
    /// the boost is added to the value returned from the demand overrides and decays to zero, so nothing is persisted
    /// (no SaveData, no version bump) and a save/reload simply re-accumulates from new deliveries.
    ///
    /// Threading: ALL sim-thread. <see cref="OnDelivery"/> is called from <c>TradeDelivery.ApplyOnSimCore</c> (sim),
    /// <see cref="Decay"/> from the day tick (sim), and <see cref="Boost"/> is read by <c>DemandSlumpExtension</c>'s
    /// overrides (sim). No marshalling needed; the field is <c>volatile</c> only as belt-and-braces.
    /// </summary>
    public static class DeliveryStimulus
    {
        /// <summary>Demand points added per delivery event (one or more receive-installments landing in a single
        /// poll). Flat, not size-scaled, so the lift rewards SUSTAINED / multiple trade relationships (repeated
        /// deliveries stack toward the cap) rather than one giant shipment. Tunable.</summary>
        public const int BoostPerDelivery = 10;

        /// <summary>The maximum lift (demand points). Bounds any catch-up burst; the boost is added to absolute
        /// 0–100 demand and clamped, so it can never overpower the game's own signal. Tunable.</summary>
        public const int BoostCap = 25;

        /// <summary>How much the lift decays each in-game day — so a fully-topped lift (≈BoostCap) fades over
        /// ~BoostCap/DecayPerDay days of no fresh imports. Tunable.</summary>
        public const int DecayPerDay = 5;

        private static volatile int _boost;

        /// <summary>The current demand lift in points (0..BoostCap). Read by the demand overrides on the sim thread.</summary>
        public static int Boost { get { return _boost; } }

        /// <summary>A trade delivered receive-goods into the city. SIM THREAD (from TradeDelivery). Bumps the lift by
        /// one flat increment (capped) and, on the 0→positive transition, posts a one-shot attributed Chirp so the
        /// player understands why demand rose (MarketChirper.Post is sim-thread work — call it directly here).</summary>
        public static void OnDelivery()
        {
            bool wasZero = _boost == 0;
            int next = _boost + BoostPerDelivery;
            _boost = next > BoostCap ? BoostCap : next;
            if (wasZero && Settings.IsChirperAlerts)
                MarketChirper.Post("Imported goods are stimulating your local economy — commercial and industrial demand are up.");
        }

        /// <summary>Decay the lift one day's worth. SIM THREAD (from the day tick). Runs regardless of online state so
        /// a lingering lift always fades after deliveries stop (incl. going offline).</summary>
        public static void Decay()
        {
            if (_boost <= 0) return;
            int next = _boost - DecayPerDay;
            _boost = next < 0 ? 0 : next;
        }

        /// <summary>Drop the lift on level unload. The lever is transient so there's nothing else to undo.</summary>
        public static void Clear() { _boost = 0; }
    }
}
