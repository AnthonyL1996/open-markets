using System;
using System.Reflection;
using HarmonyLib;

namespace OpenMarkets.Patches
{
    /// <summary>
    /// Industries-DLC export-income neutralizer — SCOPED to the export leg only.
    ///
    /// The Industries chain pays a producer per unit of goods it ships, via
    /// <c>IndustryBuildingAI.GetResourcePrice</c> — used in <c>IndustryBuildingAI.ExchangeResource</c> on EVERY cargo
    /// delivery (to local commercial, in-city industry, AND outside connections), and in
    /// <c>ProcessingFacilityAI.ProduceGoods</c> for no-output-vehicle facilities (an internal "virtual sale"). The
    /// mod re-books trade income ONLY for outside-connection import/export (<see cref="OpenMarkets.Market.PricingService"/>,
    /// via the cargo-arrival patches). So the vanilla price must be zeroed ONLY on the export leg the mod re-books —
    /// NEVER on local sales, which the mod does not replace.
    ///
    /// The earlier version zeroed <c>GetResourcePrice</c> UNCONDITIONALLY, which killed ALL local Industries revenue
    /// (every Industries building showed 0 income from produced goods — confirmed in-game + by decompile; see project
    /// memory <c>industry-resourceprice-overreach</c>). This scopes it: a prefix on <c>ExchangeResource</c> flags when
    /// the TARGET is an outside connection (an export); the <c>GetResourcePrice</c> postfix returns 0 only while that
    /// flag is set; a finalizer always clears it. Local deliveries and the no-vehicle facility sale (which never goes
    /// through <c>ExchangeResource</c>) keep their vanilla income.
    ///
    /// Threading: <c>ExchangeResource</c> → <c>GetResourcePrice</c> run synchronously on the SIM thread; the flag is
    /// <c>[ThreadStatic]</c> so a concurrent <c>GetResourcePrice</c> on another thread is unaffected. Safe without the
    /// DLC: the types live in Assembly-CSharp.dll regardless and the methods are never called when Industries content
    /// is inactive, so both patches are inert.
    /// </summary>
    public static class IndustryExportPrice
    {
        /// <summary>Set by the <c>ExchangeResource</c> prefix while the current exchange targets an outside connection
        /// (an export the mod re-books); honoured by the <c>GetResourcePrice</c> postfix. ThreadStatic — the
        /// exchange→price call chain is synchronous on one thread.</summary>
        [ThreadStatic] internal static bool SuppressExportPrice;
    }

    /// <summary>Zero the Industries per-unit price ONLY while an export-bound <c>ExchangeResource</c> is in flight, so
    /// the mod's market booking isn't double-counted on the export. Local sales keep their vanilla price.</summary>
    [HarmonyPatch]
    public static class IndustryResourcePricePatch
    {
        public static MethodBase TargetMethod()
        {
            MethodBase method = AccessTools.Method(
                typeof(IndustryBuildingAI), "GetResourcePrice",
                new[] { typeof(TransferManager.TransferReason), typeof(ItemClass.Service) });

            if (method == null)
                Log.Warn("Could not resolve IndustryBuildingAI.GetResourcePrice — Industries export neutralizer disabled.");

            return method;
        }

        public static void Postfix(ref int __result)
        {
            if (IndustryExportPrice.SuppressExportPrice) __result = 0;
        }
    }

    /// <summary>Flag the window where an Industries building ships goods to an OUTSIDE CONNECTION (the export leg the
    /// mod re-books). The prefix sets the flag from the target building; a finalizer always clears it — even if the
    /// original throws — so a subsequent LOCAL delivery can never inherit a stale "suppress" flag and lose income.</summary>
    [HarmonyPatch]
    public static class IndustryExchangeResourcePatch
    {
        public static MethodBase TargetMethod()
        {
            MethodBase method = AccessTools.Method(
                typeof(IndustryBuildingAI), "ExchangeResource",
                new[] { typeof(TransferManager.TransferReason), typeof(int), typeof(ushort), typeof(ushort) });

            if (method == null)
                Log.Warn("Could not resolve IndustryBuildingAI.ExchangeResource — Industries export neutralizer disabled.");

            return method;
        }

        // `targetBuilding` matches the original parameter name. Suppress the vanilla price only when the goods are
        // bound for an outside connection (an export). On any error default to NOT suppressing — keep the producer's
        // local income (safe-fail: never silently zero revenue).
        // INVARIANT: this predicate MUST stay identical to CargoArrivePatches' export-booking condition
        // (target IsOutsideConnection) — we zero vanilla income here ONLY because the mod re-books that same leg
        // there. If the cargo patch ever stops booking some outside-connection export, stop suppressing it here too,
        // or that export's income is silently lost.
        public static void Prefix(ushort targetBuilding)
        {
            try { IndustryExportPrice.SuppressExportPrice = TradeAttribution.IsOutsideConnection(targetBuilding); }
            catch { IndustryExportPrice.SuppressExportPrice = false; }
        }

        public static void Finalizer()
        {
            IndustryExportPrice.SuppressExportPrice = false;
        }
    }
}
