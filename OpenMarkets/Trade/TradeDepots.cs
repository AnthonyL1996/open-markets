using System;
using ColossalFramework;

namespace OpenMarkets.Trade
{
    /// <summary>
    /// Shared helpers for the player's "trade depots" — Industries warehouses opted into the market by putting
    /// the marker <see cref="Tag"/> ("[trade]") in their custom name. The M6 inventory/delivery path uses
    /// only these, and (M6) <see cref="InventoryService"/> reads tradeable stock only from these. A warehouse is
    /// enrolled iff its player-typed custom name contains the marker (case-insensitive, anywhere in the name).
    /// </summary>
    public static class TradeDepots
    {
        public const string Tag = "[trade]";

        /// <summary>True when the building's custom name contains the marker. SIM THREAD (GetName locks a managed
        /// dictionary, touches no Unity objects). Callers should pre-check <c>Building.Flags.CustomName</c> so the
        /// (locked) name lookup is skipped for the vast majority of buildings that have no custom name.</summary>
        public static bool IsTagged(InstanceManager instances, ushort buildingId)
        {
            InstanceID iid = default(InstanceID);
            iid.Building = buildingId;
            string name = instances.GetName(iid);
            return !string.IsNullOrEmpty(name)
                && name.IndexOf(Tag, StringComparison.OrdinalIgnoreCase) >= 0;
        }
    }
}
