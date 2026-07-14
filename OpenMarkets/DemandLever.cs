using ICities;
using OpenMarkets.Notify;
using UnityEngine;

namespace OpenMarkets
{
    /// <summary>
    /// M8 City Lever #1 — the austerity DEMAND SLUMP (self-consequence). While the city is in austerity (it owes a
    /// terminally-defaulted league bond — the same state that drives <see cref="TaxLock"/>), investor confidence
    /// collapses and residential / commercial / industrial+office demand are dampened, so a default *feels* like a
    /// crisis instead of an invisible ledger entry.
    ///
    /// Unlike the tax-lock this lever is **fully transient** — the effect IS the value returned from
    /// <see cref="DemandSlumpExtension"/>'s <c>OnCalculate*Demand</c> overrides, which the game recomputes every
    /// demand cycle. So there is NOTHING to stash or restore: no SaveData section, no version bump, no vanilla array
    /// mutated. Stop returning the cut → vanilla demand reverts within a cycle. That satisfies guardrails #2
    /// (recovery floor / never brick), #7 (reversible on mod removal / server death) for free.
    ///
    /// Threading: <see cref="Sync"/> runs on the MAIN thread (the /citystate poll); the overrides run on the SIM
    /// thread (the demand cycle). The only shared state is the <c>volatile</c> active flag — a plain cross-thread
    /// bool read/write, no marshalling needed because there is no manager to mutate (cf. TaxLock, which must hop to
    /// the sim thread to write <c>m_taxRates</c>).
    /// </summary>
    public static class DemandLever
    {
        /// <summary>How much demand is cut while in austerity, as a percentage of the game's own demand. PROPORTIONAL
        /// (not a flat subtraction) so it scales with the economy and can never push demand negative or zero out a
        /// sector that vanilla still wants to grow — guardrail #3 (magnitude cap) + #2 (never brick). 40 = a sharp
        /// but survivable slump. Tunable.</summary>
        public const int DemandCutPct = 40;

        private static volatile bool _active;

        /// <summary>True while the slump is applied. Read by <see cref="DemandSlumpExtension"/> on the sim thread and
        /// by the BondsTab banner on the main thread.</summary>
        public static bool IsActive { get { return _active; } }

        /// <summary>Apply the slump to one of the game's absolute demand values (0–100). Returns <paramref name="original"/>
        /// unchanged when inactive. SIM THREAD (called from the demand overrides).</summary>
        public static int Apply(int original)
        {
            if (!_active) return original;
            // Proportional cut, integer math (original ≤ 100 so original*DemandCutPct can't overflow). Clamp defensively.
            return Mathf.Clamp(original - original * DemandCutPct / 100, 0, 100);
        }

        /// <summary>React to the city's austerity status (from /citystate). MAIN THREAD. Idempotent — no-op when
        /// already in the right state, so the engage Chirp fires exactly once per slump (guardrail #6, telegraphed
        /// &amp; attributed). The price-event / tax-lock telegraphs use the same one-shot-on-transition shape.</summary>
        public static void Sync(bool austerity)
        {
            if (austerity == _active) return;
            if (austerity)
            {
                // Mirror TaxLock's contract: the engage Chirp must marshal to the sim thread (MessageManager is sim
                // work), so don't CONSUME the transition until the sim is available — otherwise the flag would flip
                // with the one-shot telegraph silently lost (the edge never re-fires). The slump effect itself needs
                // no sim thread, but keeping the flag + Chirp atomic preserves guardrail #6. sm is null only in brief
                // load/teardown windows; the next /citystate poll retries.
                SimulationManager sm = SimulationManager.instance;
                if (sm == null) return;
                _active = true;
                Log.Info("Austerity demand slump ON — residential/commercial/industrial demand cut " + DemandCutPct + "%.");
                if (Settings.IsChirperAlerts)
                    sm.AddAction(delegate
                    {
                        MarketChirper.Post("Your default has spooked investors — demand has slumped " + DemandCutPct
                            + "% across the board until your city escapes austerity.");
                    });
            }
            else
            {
                _active = false;
                Log.Info("Austerity demand slump OFF — demand restored.");
            }
        }

        /// <summary>Safety release for an OFFLINE city: a slump can only clear when /citystate reports austerity has
        /// ended, so if a lock is held but online is no longer configured (league cleared, or a mid-austerity save
        /// loaded offline) the slump could outlive our ability to release it. SIM THREAD (day tick + first post-load
        /// tick), mirroring <see cref="TaxLock.EnsureReleasedIfOffline"/>. A configured-but-unreachable city stays
        /// slumped (austerity is still real); only a genuinely un-configured city is freed.</summary>
        public static void EnsureReleasedIfOffline()
        {
            if (_active && !Settings.IsOnlineConfigured) _active = false;
        }

        /// <summary>Drop the flag on level unload / mod-disable. The slump is transient so there's nothing else to
        /// undo; a fresh city (or re-enable) re-engages from /citystate if still in austerity.</summary>
        public static void Clear() { _active = false; }
    }

    /// <summary>
    /// Auto-discovered <see cref="DemandExtensionBase"/> (the game's PluginManager instantiates every ICities
    /// extension in a mod assembly, exactly as it does <c>OpenMarketsLoading</c> / <c>OpenMarketsThreading</c> — no
    /// registration needed). Each override returns ABSOLUTE demand (0–100) and composes the M8 demand levers:
    ///   • lever #1 — <see cref="DemandLever.Apply"/>: the austerity slump (multiplicative cut) on ALL three sectors;
    ///   • lever #3 — <see cref="DeliveryStimulus.Boost"/>: the delivery lift (additive) on commercial + workplace
    ///     only (imported inputs feed commerce/industry, not housing);
    ///   • lever #4 — CoopBuff per-channel boost: a friend's investment lift (additive) on the ONE demand channel the
    ///     investor targeted (Residential / Commercial / Workplace).
    /// Order is cut-then-lift (cut multiplicatively, then add the boosts, then clamp once): during austerity a fresh
    /// import or a friend's investment partially offsets the slump. All are zero at rest, so this is a no-op for a
    /// solvent city with no recent imports and no active co-op buffs.
    /// </summary>
    public sealed class DemandSlumpExtension : DemandExtensionBase
    {
        private static int Clamp(int d) { return d > 100 ? 100 : (d < 0 ? 0 : d); }

        public override int OnCalculateResidentialDemand(int originalDemand)
        { return Clamp(DemandLever.Apply(originalDemand) + CoopBuff.DemandBoostRes); }

        public override int OnCalculateCommercialDemand(int originalDemand)
        { return Clamp(DemandLever.Apply(originalDemand) + DeliveryStimulus.Boost + CoopBuff.DemandBoostCom); }

        public override int OnCalculateWorkplaceDemand(int originalDemand)
        { return Clamp(DemandLever.Apply(originalDemand) + DeliveryStimulus.Boost + CoopBuff.DemandBoostWork); }
    }
}
