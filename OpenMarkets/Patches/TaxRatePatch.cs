using System.Reflection;
using HarmonyLib;

namespace OpenMarkets.Patches
{
    /// <summary>
    /// Austerity tax-lock enforcement (GATE-B). Prefixes the public
    /// <c>EconomyManager.SetTaxRate(ItemClass.Service, ItemClass.SubService, ItemClass.Level, int)</c> — the one
    /// setter the budget slider (<c>EconomyPanel.SetTaxRate</c>) and every other caller route through — and, while
    /// <see cref="OpenMarkets.TaxLock.IsLocked"/>, overrides the incoming rate to the austerity rate. So a player
    /// in austerity cannot lower their taxes (the slider snaps to the forced value) until they escape. Additive
    /// prefix: when not locked it's a no-op, so it never disturbs normal play and is conflict-tolerant.
    /// </summary>
    [HarmonyPatch]
    public static class TaxRatePatch
    {
        public static MethodBase TargetMethod()
        {
            MethodBase method = AccessTools.Method(
                typeof(EconomyManager), "SetTaxRate",
                new[] { typeof(ItemClass.Service), typeof(ItemClass.SubService), typeof(ItemClass.Level), typeof(int) });

            if (method == null)
                Log.Warn("Could not resolve EconomyManager.SetTaxRate — austerity tax-lock disabled.");

            return method;
        }

        // Force the rate to the austerity value while locked; otherwise leave the caller's rate untouched.
        public static void Prefix(ref int rate)
        {
            if (TaxLock.IsLocked) rate = TaxLock.AusterityRate;
        }
    }
}
