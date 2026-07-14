using System;
using System.Collections.Generic;
using System.Reflection;
using HarmonyLib;
using OpenMarkets.Data;
using OpenMarkets.Market;
using OpenMarkets.Trade;

namespace OpenMarkets.Patches
{
    /// <summary>
    /// The trade-attribution + money-booking layer (M1). Every cargo delivery that touches an outside
    /// connection is attributed to a partner and, on EXPORT, booked as mod-owned trade income via
    /// <see cref="PricingService"/> (IMPORT is charged only when the toggle is on). Each delivery is also
    /// logged when verbose logging is enabled.
    ///
    /// Two shapes, both confirmed by the decompile spike:
    ///
    ///   • <see cref="CargoRoadArrivePatch"/> — pure-ROAD border trades. CargoTruckAI.ArriveAtTarget
    ///     carries one material directly: read m_transferType / m_transferSize. m_transferSize is
    ///     DECREMENTED inside the method, so we must read it in a PREFIX.
    ///
    ///   • <see cref="CargoConsistArrivePatch"/> — RAIL / SEA / AIR. The parent consist vehicle
    ///     (CargoTrain/Ship/PlaneAI) is the one whose source/target is the outside connection, but it
    ///     carries no material itself. The real goods live on its child cargo vehicles, walked via
    ///     m_firstCargo → m_nextCargo and summed PER COMMODITY (a consist can be mixed). The body
    ///     DETACHES those children (sets m_firstCargo = 0), so this too must be a PREFIX.
    ///
    /// Child cargo trucks of a consist run station→in-city (neither end is an outside connection), so
    /// <see cref="TradeAttribution.IsOutsideConnection"/> naturally excludes them — no double counting.
    /// </summary>
    internal static class TradeAttribution
    {
        public static bool IsOutsideConnection(ushort buildingId)
        {
            if (buildingId == 0) return false;
            BuildingInfo info = BuildingManager.instance.m_buildings.m_buffer[buildingId].Info;
            return info != null && info.m_buildingAI is OutsideConnectionAI;
        }

        /// <summary>Book + log a single-commodity delivery (the road case).</summary>
        public static void HandleSingle(ushort vehicleID, string mode, bool isExport,
            ushort cityBuilding, TransferManager.TransferReason commodity, int amount)
        {
            if (!Commodities.IsTradable(commodity)) return; // skip non-trade reasons (mail, etc.) — no book, no log
            int cents = Book(isExport, commodity, amount, cityBuilding);
            if (Settings.IsDebugLogging)
                LogLine(vehicleID, mode, isExport, commodity.ToString(), amount, 0, cents);
        }

        /// <summary>
        /// Book + log a consist delivery: each commodity in <paramref name="sums"/> is booked at its own
        /// price (a cargo consist is often mixed). <paramref name="sums"/> is a reused buffer (sim thread).
        /// </summary>
        public static void HandleConsist(ushort vehicleID, string mode, bool isExport,
            ushort cityBuilding, Dictionary<TransferManager.TransferReason, long> sums, int units)
        {
            long totalAmount = 0;
            long totalCents = 0;
            int reasons = 0;
            TransferManager.TransferReason last = TransferManager.TransferReason.None;

            foreach (KeyValuePair<TransferManager.TransferReason, long> kv in sums)
            {
                int amount = kv.Value > int.MaxValue ? int.MaxValue : (int)kv.Value;
                totalCents += Book(isExport, kv.Key, amount, cityBuilding);
                totalAmount += kv.Value;
                last = kv.Key;
                reasons++;
            }

            if (!Settings.IsDebugLogging) return;
            string commodity = reasons == 1 ? last.ToString() : ("mixed(" + reasons + ")");
            int amountForLog = totalAmount > int.MaxValue ? int.MaxValue : (int)totalAmount;
            LogLine(vehicleID, mode, isExport, commodity, amountForLog, units, totalCents);
        }

        private static int Book(bool isExport, TransferManager.TransferReason commodity, int amount, ushort cityBuilding)
        {
            // Only book + count REAL market commodities. Cargo AIs also haul non-trade reasons to outside connections
            // (OutgoingMail/IncomingMail, etc.); those must not be priced (they'd fall back to the default price and
            // mint phantom trade income) nor feed the elasticity index. Defensive gate — the callers also pre-filter.
            if (!Commodities.IsTradable(commodity)) return 0;
            // Elasticity: record this city's net volume for every leg (drives the daily /report → server index),
            // independent of whether money is booked. M9: one price per commodity — no partner.
            LocalPriceSim.Instance.RecordVolume(commodity, amount, isExport);
            return isExport
                ? PricingService.BookExport(commodity, amount, cityBuilding)
                : PricingService.BookImport(commodity, amount, cityBuilding);
        }

        private static void LogLine(ushort vehicleID, string mode, bool isExport,
            string commodity, int amount, int units, long moneyCents)
        {
            string money = moneyCents > 0
                ? (isExport ? " booked=§" : " charged=§") + (moneyCents / 100)
                : string.Empty;

            Log.Info(string.Format(
                "Cargo arrive v{0} [{1}]: {2} {3} amount={4}{5}{6}",
                vehicleID,
                mode,
                isExport ? "EXPORT" : "IMPORT",
                commodity,
                amount,
                units > 0 ? " (" + units + " units)" : string.Empty,
                money));
        }
    }

    /// <summary>Pure-road border trades: CargoTruckAI.ArriveAtTarget (prefix).</summary>
    [HarmonyPatch]
    public static class CargoRoadArrivePatch
    {
        public static MethodBase TargetMethod()
        {
            MethodBase method = AccessTools.Method(
                typeof(CargoTruckAI), "ArriveAtTarget",
                new[] { typeof(ushort), typeof(Vehicle).MakeByRefType() });

            if (method == null)
                Log.Warn("Could not resolve CargoTruckAI.ArriveAtTarget — signature may differ on this game version.");

            return method;
        }

        // PREFIX: ArriveAtTarget decrements data.m_transferSize, so read it before the body runs.
        public static void Prefix(ushort vehicleID, ref Vehicle data)
        {
            try
            {
                ushort source = data.m_sourceBuilding;
                ushort target = data.m_targetBuilding;

                bool targetIsOutside = TradeAttribution.IsOutsideConnection(target);
                bool sourceIsOutside = TradeAttribution.IsOutsideConnection(source);

                // Not a partner trade (and excludes consist child trucks, which run station↔in-city).
                if (!targetIsOutside && !sourceIsOutside) return;

                // Outside connection is the TARGET => goods leaving the city => EXPORT; else IMPORT.
                bool isExport = targetIsOutside;
                ushort cityBuilding = isExport ? source : target;

                TradeAttribution.HandleSingle(
                    vehicleID, "road", isExport, cityBuilding,
                    (TransferManager.TransferReason)data.m_transferType, data.m_transferSize);
            }
            catch (Exception e)
            {
                Log.Error("CargoRoadArrivePatch.Prefix failed: " + e);
            }
        }
    }

    /// <summary>
    /// Rail / sea / air trades: the parent consist's ArriveAtTarget (prefix). Volume + commodities come
    /// from walking the child cargo chain (summed per commodity), since the parent carries no material.
    /// </summary>
    [HarmonyPatch]
    public static class CargoConsistArrivePatch
    {
        // Reused per-call to sum child volume per commodity without allocating (sim thread only).
        private static readonly Dictionary<TransferManager.TransferReason, long> _sums =
            new Dictionary<TransferManager.TransferReason, long>();

        public static IEnumerable<MethodBase> TargetMethods()
        {
            foreach (Type ai in new[] { typeof(CargoTrainAI), typeof(CargoShipAI), typeof(CargoPlaneAI) })
            {
                MethodBase method = AccessTools.Method(
                    ai, "ArriveAtTarget", new[] { typeof(ushort), typeof(Vehicle).MakeByRefType() });

                if (method == null)
                    Log.Warn("Could not resolve " + ai.Name + ".ArriveAtTarget — signature may differ on this game version.");
                else
                    yield return method;
            }
        }

        // PREFIX: the body detaches the child cargo chain (sets m_firstCargo = 0), so walk it first.
        public static void Prefix(ushort vehicleID, ref Vehicle data)
        {
            try
            {
                ushort source = data.m_sourceBuilding;
                ushort target = data.m_targetBuilding;

                bool targetIsOutside = TradeAttribution.IsOutsideConnection(target);
                bool sourceIsOutside = TradeAttribution.IsOutsideConnection(source);
                if (!targetIsOutside && !sourceIsOutside) return;

                bool isExport = targetIsOutside;
                ushort cityBuilding = isExport ? source : target;

                // Sum child cargo volume per commodity (a consist can carry mixed goods).
                Vehicle[] vehicles = VehicleManager.instance.m_vehicles.m_buffer;
                _sums.Clear();
                int units = 0;
                ushort cargoId = data.m_firstCargo;
                int guard = 0;
                while (cargoId != 0 && guard++ < 16384)
                {
                    int size = vehicles[cargoId].m_transferSize;
                    if (size > 0)
                    {
                        var reason = (TransferManager.TransferReason)vehicles[cargoId].m_transferType;
                        if (Commodities.IsTradable(reason)) // exclude non-trade cargo (mail, etc.) from the basket
                        {
                            long existing;
                            _sums[reason] = _sums.TryGetValue(reason, out existing) ? existing + size : size;
                        }
                    }
                    units++;
                    cargoId = vehicles[cargoId].m_nextCargo;
                }

                if (_sums.Count > 0)
                    TradeAttribution.HandleConsist(vehicleID, ModeOf(data), isExport, cityBuilding, _sums, units);
            }
            catch (Exception e)
            {
                Log.Error("CargoConsistArrivePatch.Prefix failed: " + e);
            }
        }

        private static string ModeOf(Vehicle data)
        {
            VehicleAI ai = data.Info != null ? data.Info.m_vehicleAI : null;
            if (ai is CargoTrainAI) return "rail";
            if (ai is CargoShipAI) return "sea";
            if (ai is CargoPlaneAI) return "air";
            return "consist";
        }
    }
}
