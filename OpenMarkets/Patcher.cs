using System.Reflection;
using HarmonyLib;

namespace OpenMarkets
{
    /// <summary>
    /// Owns the Harmony lifecycle. Isolated from the IUserMod class so that a missing/late
    /// Harmony never blocks the mod from loading. PatchAll scans this assembly for [HarmonyPatch].
    /// </summary>
    public static class Patcher
    {
        // Unique per mod. Convention: "<author>.<modname>".
        public const string HarmonyId = "anthony.openmarkets";

        private static bool _patched;

        public static void PatchAll()
        {
            if (_patched) return;
            _patched = true;

            Log.Info("Applying Harmony patches...");
            new Harmony(HarmonyId).PatchAll(Assembly.GetExecutingAssembly());
            Log.Info("Harmony patches applied.");
        }

        public static void UnpatchAll()
        {
            if (!_patched) return;

            new Harmony(HarmonyId).UnpatchAll(HarmonyId);
            _patched = false;
            Log.Info("Harmony patches removed.");
        }
    }
}
