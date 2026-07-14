using System;
using System.Reflection;
using ColossalFramework;
using UnityEngine;

namespace OpenMarkets
{
    /// <summary>
    /// GATE-B austerity tax-lock: while the city is in austerity (it owes a terminally-defaulted league bond),
    /// force its tax rates to a punitive rate and FREEZE them — the Harmony prefix on
    /// <c>EconomyManager.SetTaxRate</c> (<see cref="OpenMarkets.Patches.TaxRatePatch"/>) blocks the budget slider —
    /// until austerity clears. The player's pre-austerity rates are stashed in our own SaveData (§9) and restored
    /// on exit, on mod-disable, and across reload. NFR-3: <c>m_taxRates</c> is written into the VANILLA save, so a
    /// forced rate would persist; the stash/restore is the safeguard so removing the mod can't strand the city.
    ///
    /// Tax state is <see cref="EconomyManager"/> (simulation) state, so the enter/exit run on the SIM thread
    /// (marshalled from the main-thread /citystate poll). The lock flag is read by the prefix on any thread.
    /// </summary>
    public static class TaxLock
    {
        /// <summary>The rate (%) forced on every service while in austerity. Within GetTaxRate's 0–100 read clamp;
        /// 29 is the vanilla budget-panel maximum. Tunable.</summary>
        public const int AusterityRate = 29;

        private static volatile bool _locked;
        private static volatile bool _enterQueued;   // an Enter is queued on the sim thread but hasn't run yet
        private static int[] _saved;   // the player's m_taxRates snapshot, taken on entering austerity
        private static FieldInfo _taxRatesField;

        private static readonly ItemClass.Service[] Services =
        {
            ItemClass.Service.Residential, ItemClass.Service.Commercial,
            ItemClass.Service.Industrial, ItemClass.Service.Office,
        };

        public static bool IsLocked { get { return _locked; } }

        /// <summary>React to the city's austerity status (from /citystate). MAIN THREAD — marshals the actual tax
        /// mutation to the simulation thread (EconomyManager is sim state). No-op when already in the right state.</summary>
        public static void Sync(bool austerity)
        {
            if (austerity == _locked) return;
            SimulationManager sm = SimulationManager.instance;
            if (sm == null) return;
            if (austerity)
            {
                if (_enterQueued) return;        // already queued — never double-snapshot (would capture forced rates)
                _enterQueued = true;
                sm.AddAction(delegate { _enterQueued = false; Enter(); });
            }
            else sm.AddAction(delegate { Exit(); });
        }

        /// <summary>Safety release for an OFFLINE city: if a lock is held but online is no longer configured (the
        /// player cleared their league, or loaded a mid-austerity save offline), austerity can never be observed
        /// to clear via /citystate — so don't let the freeze outlive our ability to release it. SIM THREAD (called
        /// from the day tick + the first post-load tick). A configured-but-server-unreachable city stays locked
        /// (austerity is still real); only a genuinely un-configured city is freed here.</summary>
        public static void EnsureReleasedIfOffline()
        {
            if (_locked && !Settings.IsOnlineConfigured) Exit();
        }

        // SIM THREAD. Snapshot the player's rates (once), then force every service to the austerity rate.
        private static void Enter()
        {
            if (_locked) return;            // already locked (e.g. restored from save) — keep the original stash
            int[] live = LiveRates();
            if (live == null) return;
            _saved = (int[])live.Clone();
            _locked = true;
            ForceAusterityRates();
            Log.Info("Austerity tax-lock ON — taxes forced to " + AusterityRate + "%.");
        }

        // SIM THREAD. Restore the stashed rates and unfreeze.
        private static void Exit()
        {
            if (!_locked) return;
            _locked = false;
            RestoreSaved();
            Log.Info("Austerity tax-lock OFF — taxes restored.");
        }

        /// <summary>Restore rates if locked so removing the mod doesn't leave the city stranded at the austerity
        /// rate. Called from OnDisabled (the sim may be tearing down, so this writes directly, not via AddAction).</summary>
        public static void RestoreOnDisable()
        {
            if (!_locked) return;
            // Marshal to the sim thread when it's alive so the restore can't race a concurrent sim read or a
            // still-queued Enter (AddAction is FIFO, so Exit runs AFTER any pending Enter → final state restored).
            // Only write directly when the simulation is gone (genuine teardown, no race to lose).
            SimulationManager sm = SimulationManager.instance;
            if (sm != null) sm.AddAction(delegate { Exit(); });
            else { _locked = false; RestoreSaved(); }
        }

        // Re-assert the forced rate (also called by the prefix is unnecessary — the prefix overrides at the source;
        // this is the initial push on Enter). SetTaxRate(None,None) fans out to every level/sub-service.
        private static void ForceAusterityRates()
        {
            EconomyManager em = Singleton<EconomyManager>.instance;
            if (em == null) return;
            for (int i = 0; i < Services.Length; i++)
                em.SetTaxRate(Services[i], ItemClass.SubService.None, ItemClass.Level.None, AusterityRate);
        }

        private static void RestoreSaved()
        {
            if (_saved != null) WriteRates(_saved);
            _saved = null;
        }

        private static int[] LiveRates()
        {
            EconomyManager em = Singleton<EconomyManager>.instance;
            if (em == null) return null;
            if (_taxRatesField == null)
                _taxRatesField = typeof(EconomyManager).GetField("m_taxRates", BindingFlags.NonPublic | BindingFlags.Instance);
            return _taxRatesField != null ? _taxRatesField.GetValue(em) as int[] : null;
        }

        // Copy `rates` back into the live m_taxRates array (in place, so the manager keeps using the same array).
        private static void WriteRates(int[] rates)
        {
            int[] live = LiveRates();
            if (live == null || rates == null) return;
            int n = live.Length < rates.Length ? live.Length : rates.Length;
            if (live.Length != rates.Length)
                Log.Warn("TaxLock: tax-array length changed (" + rates.Length + " → " + live.Length
                    + "); restoring the first " + n + " rates.");
            Array.Copy(rates, live, n);
        }

        // ---- SaveData §9 (v6): persist the lock state + the stashed rates so a save mid-austerity restores the
        // player's real rates on reload (the forced rates are baked into the vanilla save). ----
        public static bool PersistLocked { get { return _locked; } }
        public static int[] PersistSaved() { return _saved; }
        // Defensive (NFR-3): never hold a lock we can't lift — a malformed blob with locked=true but a null stash
        // would strand the city at the forced rate (RestoreSaved would no-op). Drop the lock in that case. Normal
        // saves always carry the stash (Enter locks only after cloning it), so this only guards a corrupt blob.
        public static void Restore(bool locked, int[] saved)
        {
            if (locked && saved == null) { _locked = false; _saved = null; return; }
            _locked = locked; _saved = saved;
        }

        /// <summary>Drop in-memory state on level unload. Does NOT touch the live rates (the city is unloading; the
        /// save already captured both the forced rates and our stash, which restore together on reload).</summary>
        public static void Clear() { _locked = false; _saved = null; }
    }
}
