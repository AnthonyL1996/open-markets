using System.Collections.Generic;
using ColossalFramework;

namespace OpenMarkets.Trade
{
    /// <summary>
    /// Reads tradeable inventory from the player's <c>[trade]</c>-tagged Industries warehouses (the same depots
    /// the trade depots hold). Scans on the SIMULATION thread (the daily tick + once on load) and
    /// publishes a per-commodity snapshot of stored stock that the UI reads on the main thread.
    /// M6 Phase 1: READ-ONLY — no stock is moved yet (give-side delivery is Phase 2).
    ///
    /// Units: a warehouse's <c>Building.m_customBuffer1</c> (ushort) counts HUNDREDS of transfer units, so stored
    /// transfer units = <c>m_customBuffer1 * 100</c>. We expose whole "commodity units" via
    /// <see cref="TransferUnitsPerUnit"/>. See INVENTORY-TRADE.md §3–§4.
    /// </summary>
    public static class InventoryService
    {
        /// <summary>Transfer units per 1 whole commodity/basket unit. 8000 = one truckload (WarehouseAI's
        /// GetMaxLoadSize), so a default 1,000,000-capacity warehouse holds ~125 units — legible, physically
        /// intuitive, and gives offer-validation real teeth. Must be a multiple of 100 (the delivery
        /// <c>ModifyMaterialBuffer</c> path divides deltas by 100). Tunable calibration — see INVENTORY-TRADE.md §4.</summary>
        public const int TransferUnitsPerUnit = 8000;

        // Per-commodity stored TRANSFER UNITS in tagged depots. Volatile reference swap: the sim thread builds a
        // fresh dictionary and publishes it; the main thread reads the current reference (no shared mutation).
        private static volatile Dictionary<TransferManager.TransferReason, long> _stored =
            new Dictionary<TransferManager.TransferReason, long>();

        // Per-commodity RESERVED transfer units (remaining undelivered give on active trades), published by the
        // main-thread delivery poll and read by the sim-thread driver clamp. Volatile reference swap.
        private static volatile Dictionary<TransferManager.TransferReason, long> _reserved =
            new Dictionary<TransferManager.TransferReason, long>();

        public static void Clear()
        {
            _stored = new Dictionary<TransferManager.TransferReason, long>();
            _reserved = new Dictionary<TransferManager.TransferReason, long>();
        }

        /// <summary>Publish the per-commodity reserved transfer units (main thread, from the trades poll).</summary>
        public static void PublishReserved(Dictionary<TransferManager.TransferReason, long> reservedTransferUnits)
        {
            _reserved = reservedTransferUnits ?? new Dictionary<TransferManager.TransferReason, long>();
        }

        /// <summary>Raw stored transfer units for a commodity (0 if none). Any-thread (volatile read).</summary>
        public static long StoredTransferUnits(TransferManager.TransferReason reason)
        {
            long t;
            return _stored.TryGetValue(reason, out t) ? t : 0L;
        }

        /// <summary>Reserved transfer units (remaining undelivered give) for a commodity. Any-thread.</summary>
        public static long ReservedTransferUnits(TransferManager.TransferReason reason)
        {
            long t;
            return _reserved.TryGetValue(reason, out t) ? t : 0L;
        }

        /// <summary>SIM THREAD. Rescan <c>[trade]</c>-tagged warehouses and publish the per-commodity stored snapshot.
        /// Independent of the driver toggle — inventory is just read (you can see depot stock without auto-driving).</summary>
        public static void Scan()
        {
            if (!SteamHelper.IsDLCOwned(SteamHelper.DLC.IndustryDLC)) { Clear(); return; }

            Dictionary<TransferManager.TransferReason, long> sum = new Dictionary<TransferManager.TransferReason, long>();
            Building[] buffer = BuildingManager.instance.m_buildings.m_buffer;
            InstanceManager instances = Singleton<InstanceManager>.instance;

            for (int id = 1; id < buffer.Length; id++)
            {
                Building.Flags flags = buffer[id].m_flags;
                if ((flags & Building.Flags.Created) == 0) continue;
                if ((flags & Building.Flags.Completed) == 0) continue;
                if ((flags & Building.Flags.Deleted) != 0) continue;
                if ((flags & Building.Flags.CustomName) == 0) continue;   // only custom-named can carry the tag

                BuildingInfo info = buffer[id].Info;
                WarehouseAI wh = info != null ? info.m_buildingAI as WarehouseAI : null;
                if (wh == null) continue;

                ushort bid = (ushort)id;
                if (!TradeDepots.IsTagged(instances, bid)) continue;

                TransferManager.TransferReason reason = wh.GetActualTransferReason(bid, ref buffer[id]);
                if (reason == TransferManager.TransferReason.None) continue;

                long units = (long)buffer[id].m_customBuffer1 * 100L; // m_customBuffer1 is in hundreds of transfer units
                long s; sum.TryGetValue(reason, out s); sum[reason] = s + units;
            }

            _stored = sum;
        }

        /// <summary>SIM THREAD. Move <paramref name="transferUnitsDelta"/> of <paramref name="reason"/> across the
        /// player's [trade] depots (negative = REMOVE/deliver, positive = ADD/receive). Returns the SIGNED transfer
        /// units actually moved. Pass a multiple of 100 (sub-100 rounds away).</summary>
        public static long MoveStock(TransferManager.TransferReason reason, long transferUnitsDelta)
        {
            return MoveStockScoped(reason, transferUnitsDelta, true);
        }

        /// <summary>SIM THREAD. ADD <paramref name="transferUnits"/> of <paramref name="reason"/> to the player's
        /// UNTAGGED warehouses storing it — the receive-overflow spill when the [trade] depots are full, so traded
        /// goods aren't lost if you have any storage for them. Returns transfer units actually added.</summary>
        public static long SpillToUntagged(TransferManager.TransferReason reason, long transferUnits)
        {
            if (transferUnits <= 0L) return 0L;
            return MoveStockScoped(reason, transferUnits, false);
        }

        // taggedOnly = true → only [trade] depots (give/receive primary); false → only UNTAGGED warehouses (spill).
        private static long MoveStockScoped(TransferManager.TransferReason reason, long transferUnitsDelta, bool taggedOnly)
        {
            if (transferUnitsDelta == 0 || !SteamHelper.IsDLCOwned(SteamHelper.DLC.IndustryDLC)) return 0L;

            Building[] buffer = BuildingManager.instance.m_buildings.m_buffer;
            InstanceManager instances = Singleton<InstanceManager>.instance;
            long remaining = transferUnitsDelta;   // signed; consumed as warehouses accept the move

            for (int id = 1; id < buffer.Length && remaining != 0L; id++)
            {
                Building.Flags flags = buffer[id].m_flags;
                if ((flags & Building.Flags.Created) == 0) continue;
                if ((flags & Building.Flags.Completed) == 0) continue;
                if ((flags & Building.Flags.Deleted) != 0) continue;

                BuildingInfo info = buffer[id].Info;
                WarehouseAI wh = info != null ? info.m_buildingAI as WarehouseAI : null;
                if (wh == null) continue;

                ushort bid = (ushort)id;
                if (wh.GetActualTransferReason(bid, ref buffer[id]) != reason) continue;
                // Only custom-named buildings can carry the tag; an untagged warehouse is any of-this-commodity
                // warehouse that isn't a [trade] depot (no custom name, or a name without the marker).
                bool tagged = (flags & Building.Flags.CustomName) != 0 && TradeDepots.IsTagged(instances, bid);
                if (taggedOnly != tagged) continue;

                int delta = remaining > int.MaxValue ? int.MaxValue : (remaining < int.MinValue ? int.MinValue : (int)remaining);
                wh.ModifyMaterialBuffer(bid, ref buffer[id], reason, ref delta); // delta := actually moved (clamped)
                remaining -= delta;
            }
            return transferUnitsDelta - remaining; // signed amount actually moved
        }

        /// <summary>Whole commodity units currently stored in tagged depots for a commodity (0 if none).</summary>
        public static long StoredUnits(TransferManager.TransferReason reason)
        {
            long t;
            return _stored.TryGetValue(reason, out t) ? t / TransferUnitsPerUnit : 0L;
        }

        /// <summary>Snapshot copy of stored whole-units per commodity (for the Inventory tab). Main-thread safe read.</summary>
        public static Dictionary<TransferManager.TransferReason, long> StoredUnitsSnapshot()
        {
            Dictionary<TransferManager.TransferReason, long> src = _stored;
            Dictionary<TransferManager.TransferReason, long> outv =
                new Dictionary<TransferManager.TransferReason, long>(src.Count);
            foreach (KeyValuePair<TransferManager.TransferReason, long> kv in src)
                outv[kv.Key] = kv.Value / TransferUnitsPerUnit;
            return outv;
        }
    }
}
