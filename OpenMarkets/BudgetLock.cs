using System;
using System.Reflection;
using ColossalFramework;
using UnityEngine;

namespace OpenMarkets
{
    /// <summary>
    /// M8 City Lever #2 — the austerity BUDGET CAP (self-consequence). While the city is in austerity (the same
    /// state that drives <see cref="TaxLock"/> + <see cref="DemandLever"/>) no service may be funded above
    /// <see cref="BudgetCeilingPct"/>: every service-budget slider is capped, so a defaulted city's services degrade
    /// (slower response, thinner coverage) on top of the forced taxes and demand slump. The cap is enforced by a
    /// Harmony prefix on <c>EconomyManager.SetBudget</c> (<see cref="OpenMarkets.Patches.BudgetRatePatch"/>) — the
    /// player can cut a budget LOWER during austerity but cannot raise one above the cap until they escape.
    ///
    /// This is the FULL tax-lock pattern (read→stash→force→re-assert→restore), unlike the transient demand lever:
    /// <c>m_serviceBudgetDay/Night</c> are written into the VANILLA save, so a forced cap would persist and removing
    /// the mod could strand the city's services low — the stash/restore (SaveData §10) is the NFR-3 safeguard.
    ///
    /// Threading: budget is <see cref="EconomyManager"/> (simulation) state, so enter/exit run on the SIM thread
    /// (marshalled from the main-thread /citystate poll), exactly like <see cref="TaxLock"/>. The cap flag is read
    /// by the prefix on any thread.
    /// </summary>
    public static class BudgetLock
    {
        /// <summary>The maximum service-budget % allowed while in austerity. 75 = a 25% cut for fully-funded (100%)
        /// services; services already at or below it are untouched (the cap only ever LOWERS, never raises spending
        /// during a cash crisis). Well above the vanilla slider minimum (50%), so services degrade but never brick —
        /// guardrails #2 (recovery floor / never brick) + #3 (magnitude cap). Tunable.</summary>
        public const int BudgetCeilingPct = 75;

        private static volatile bool _locked;
        private static volatile bool _enterQueued;   // an Enter is queued on the sim thread but hasn't run yet
        private static int[] _savedDay;    // the player's m_serviceBudgetDay snapshot, taken on entering austerity
        private static int[] _savedNight;  // ditto for m_serviceBudgetNight (separate day/night budget arrays)
        private static FieldInfo _dayField;
        private static FieldInfo _nightField;

        public static bool IsLocked { get { return _locked; } }

        /// <summary>React to the city's austerity status (from /citystate). MAIN THREAD — marshals the actual budget
        /// mutation to the simulation thread. No-op when already in the right state. Mirrors <see cref="TaxLock.Sync"/>.</summary>
        public static void Sync(bool austerity)
        {
            if (austerity == _locked) return;
            SimulationManager sm = SimulationManager.instance;
            if (sm == null) return;
            if (austerity)
            {
                if (_enterQueued) return;        // already queued — never double-snapshot (would capture capped budgets)
                _enterQueued = true;
                sm.AddAction(delegate { _enterQueued = false; Enter(); });
            }
            else sm.AddAction(delegate { Exit(); });
        }

        /// <summary>Safety release for an OFFLINE city: if a cap is held but online is no longer configured, austerity
        /// can never be observed to clear via /citystate — don't let the cap outlive our ability to release it.
        /// SIM THREAD (day tick + first post-load tick). Mirrors <see cref="TaxLock.EnsureReleasedIfOffline"/>.</summary>
        public static void EnsureReleasedIfOffline()
        {
            if (_locked && !Settings.IsOnlineConfigured) Exit();
        }

        // SIM THREAD. Snapshot the player's budgets (once), then cap every slot to the austerity ceiling.
        private static void Enter()
        {
            if (_locked) return;            // already locked (e.g. restored from save) — keep the original stash
            int[] day = LiveDay();
            int[] night = LiveNight();
            if (day == null || night == null) return;
            _savedDay = (int[])day.Clone();
            _savedNight = (int[])night.Clone();
            _locked = true;
            ApplyCeiling();
            Log.Info("Austerity budget cap ON — service budgets capped at " + BudgetCeilingPct + "%.");
        }

        // SIM THREAD. Restore the stashed budgets and lift the cap.
        private static void Exit()
        {
            if (!_locked) return;
            _locked = false;
            RestoreSaved();
            Log.Info("Austerity budget cap OFF — budgets restored.");
        }

        /// <summary>Restore budgets if capped so removing the mod doesn't leave the city's services stranded low.
        /// Called from OnDisabled. Mirrors <see cref="TaxLock.RestoreOnDisable"/> (marshal when the sim is alive so
        /// the restore can't race a still-queued Enter; write directly only in genuine teardown).</summary>
        public static void RestoreOnDisable()
        {
            if (!_locked) return;
            SimulationManager sm = SimulationManager.instance;
            if (sm != null) sm.AddAction(delegate { Exit(); });
            else { _locked = false; RestoreSaved(); }
        }

        // Cap every budget slot to the ceiling (min → only ever lowers; ≤ceiling slots and unused slots untouched).
        // Covers all services AND subservices in one pass (the arrays are the full 42-slot PublicClassIndex space),
        // so no need to enumerate services or rely on SetBudget's subservice fan-out.
        private static void ApplyCeiling()
        {
            Cap(LiveDay());
            Cap(LiveNight());
        }

        private static void Cap(int[] budgets)
        {
            if (budgets == null) return;
            for (int i = 0; i < budgets.Length; i++)
                if (budgets[i] > BudgetCeilingPct) budgets[i] = BudgetCeilingPct;
        }

        private static void RestoreSaved()
        {
            WriteArray(LiveDay(), _savedDay);
            WriteArray(LiveNight(), _savedNight);
            _savedDay = null;
            _savedNight = null;
        }

        private static int[] LiveDay()
        {
            EconomyManager em = Singleton<EconomyManager>.instance;
            if (em == null) return null;
            if (_dayField == null)
                _dayField = typeof(EconomyManager).GetField("m_serviceBudgetDay", BindingFlags.NonPublic | BindingFlags.Instance);
            return _dayField != null ? _dayField.GetValue(em) as int[] : null;
        }

        private static int[] LiveNight()
        {
            EconomyManager em = Singleton<EconomyManager>.instance;
            if (em == null) return null;
            if (_nightField == null)
                _nightField = typeof(EconomyManager).GetField("m_serviceBudgetNight", BindingFlags.NonPublic | BindingFlags.Instance);
            return _nightField != null ? _nightField.GetValue(em) as int[] : null;
        }

        // Copy `saved` back into the live array in place (so the manager keeps using the same array). Defensive
        // length handling mirrors TaxLock.WriteRates in case the budget-array size ever differs across versions.
        private static void WriteArray(int[] live, int[] saved)
        {
            if (live == null || saved == null) return;
            int n = live.Length < saved.Length ? live.Length : saved.Length;
            if (live.Length != saved.Length)
                Log.Warn("BudgetLock: budget-array length changed (" + saved.Length + " → " + live.Length
                    + "); restoring the first " + n + ".");
            Array.Copy(saved, live, n);
        }

        // ---- SaveData §10 (v7): persist the cap state + the stashed pre-austerity budgets so a save made mid-
        // austerity restores the player's REAL budgets on reload (the capped budgets are baked into the vanilla save). ----
        public static bool PersistLocked { get { return _locked; } }
        public static int[] PersistSavedDay() { return _savedDay; }
        public static int[] PersistSavedNight() { return _savedNight; }
        public static void Restore(bool locked, int[] savedDay, int[] savedNight)
        {
            // Defensive (NFR-3): never hold a cap we can't lift. A malformed/partial blob could carry locked=true with
            // a null stash; without it RestoreSaved is a no-op and the city would be stranded at the cap with no
            // recovery path. Drop the lock instead — the prefix stops enforcing and the player can re-raise budgets;
            // worse than a clean restore, but never stranded. (Normal saves always carry both stashes — Enter only
            // locks after cloning them — so this only guards a corrupt blob.)
            if (locked && (savedDay == null || savedNight == null))
            {
                _locked = false; _savedDay = null; _savedNight = null; return;
            }
            _locked = locked; _savedDay = savedDay; _savedNight = savedNight;
        }

        /// <summary>Drop in-memory state on level unload. Does NOT touch live budgets (the city is unloading; the
        /// save captured both the capped budgets and our stash, which restore together on reload).</summary>
        public static void Clear() { _locked = false; _savedDay = null; _savedNight = null; }
    }
}
