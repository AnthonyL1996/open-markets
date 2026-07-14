using System.Reflection;
using HarmonyLib;

namespace OpenMarkets.Patches
{
    /// <summary>
    /// Austerity budget-cap enforcement (M8 lever #2). Prefixes the public
    /// <c>EconomyManager.SetBudget(ItemClass.Service, ItemClass.SubService, int budget, bool night)</c> — the one
    /// setter the budget sliders (<c>EconomyPanel</c>) route through — and, while
    /// <see cref="OpenMarkets.BudgetLock.IsLocked"/>, clamps the incoming budget DOWN to the austerity ceiling. A
    /// player in austerity may cut a service budget further but cannot raise one above the cap until they escape.
    /// Additive prefix: a no-op when not locked, so it never disturbs normal play and is conflict-tolerant. SetBudget
    /// recurses to fan out across subservices; the prefix re-runs on each recursion and is idempotent (re-clamping an
    /// already-clamped value is harmless).
    /// </summary>
    [HarmonyPatch]
    public static class BudgetRatePatch
    {
        public static MethodBase TargetMethod()
        {
            MethodBase method = AccessTools.Method(
                typeof(EconomyManager), "SetBudget",
                new[] { typeof(ItemClass.Service), typeof(ItemClass.SubService), typeof(int), typeof(bool) });

            if (method == null)
                Log.Warn("Could not resolve EconomyManager.SetBudget — austerity budget cap disabled.");

            return method;
        }

        // Clamp the budget to the austerity ceiling while locked; otherwise leave the caller's value untouched.
        public static void Prefix(ref int budget)
        {
            if (BudgetLock.IsLocked && budget > BudgetLock.BudgetCeilingPct) budget = BudgetLock.BudgetCeilingPct;
        }
    }
}
