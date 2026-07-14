using System.Collections.Generic;

namespace OpenMarkets.Data
{
    /// <summary>
    /// The master list of tradable commodities + lookups. Single source of truth for base price and
    /// display name (used by LocalPriceSim and, in M2, the dashboard + DLC-conditional registration).
    /// Base prices mirror vanilla <c>GetResourcePrice</c> where it defines one, and assign sensible
    /// anchors to the goods vanilla leaves at 0 (Goods/Food/Lumber/Petrol/Coal) so every resource trades.
    /// </summary>
    public static class Commodities
    {
        private const int DefaultBasePrice = 300;

        /// <summary>Transfer units in one full cargo TRUCK (vanilla CargoTruckAI.m_cargoCapacity). Prices are shown
        /// PER TRUCK everywhere — the tangible unit a player sees move — instead of per raw transfer unit. A rail/sea/
        /// air consist is an integer number of these truck-units. Cents for a truckload = UnitsPerTruck × scaledPrice / 100.</summary>
        public const int UnitsPerTruck = 8000;

        private static readonly Commodity[] _all =
        {
            // Base game
            new Commodity(TransferManager.TransferReason.Oil,    "Oil",    400, CommodityTier.Base),
            new Commodity(TransferManager.TransferReason.Ore,    "Ore",    300, CommodityTier.Base),
            new Commodity(TransferManager.TransferReason.Logs,   "Logs",   200, CommodityTier.Base),
            new Commodity(TransferManager.TransferReason.Grain,  "Grain",  200, CommodityTier.Base),
            new Commodity(TransferManager.TransferReason.Coal,   "Coal",   150, CommodityTier.Base),
            new Commodity(TransferManager.TransferReason.Petrol, "Petrol", 500, CommodityTier.Base),
            new Commodity(TransferManager.TransferReason.Food,   "Food",   400, CommodityTier.Base),
            new Commodity(TransferManager.TransferReason.Lumber, "Lumber", 300, CommodityTier.Base),
            new Commodity(TransferManager.TransferReason.Goods,  "Goods",  600, CommodityTier.Base),
            // Sunset Harbor DLC
            new Commodity(TransferManager.TransferReason.Fish,   "Fish",   600, CommodityTier.SunsetHarbor),
            // Industries DLC
            new Commodity(TransferManager.TransferReason.AnimalProducts, "Animal Products", 1500,  CommodityTier.Industries),
            new Commodity(TransferManager.TransferReason.Flours,         "Flour",           1500,  CommodityTier.Industries),
            new Commodity(TransferManager.TransferReason.Paper,          "Paper",           1500,  CommodityTier.Industries),
            new Commodity(TransferManager.TransferReason.PlanedTimber,   "Planed Timber",   1500,  CommodityTier.Industries),
            new Commodity(TransferManager.TransferReason.Petroleum,      "Petroleum",       3000,  CommodityTier.Industries),
            new Commodity(TransferManager.TransferReason.Plastics,       "Plastics",        3000,  CommodityTier.Industries),
            new Commodity(TransferManager.TransferReason.Glass,          "Glass",           2250,  CommodityTier.Industries),
            new Commodity(TransferManager.TransferReason.Metals,         "Metals",          2250,  CommodityTier.Industries),
            new Commodity(TransferManager.TransferReason.LuxuryProducts, "Luxury Products", 10000, CommodityTier.Industries),
        };

        private static readonly Dictionary<TransferManager.TransferReason, Commodity> _byReason = BuildIndex();

        /// <summary>All tradable commodities (regardless of DLC ownership).</summary>
        public static IList<Commodity> All { get { return _all; } }

        /// <summary>The commodity for a reason, or null if it isn't one we trade.</summary>
        public static Commodity Get(TransferManager.TransferReason reason)
        {
            Commodity c;
            return _byReason.TryGetValue(reason, out c) ? c : null;
        }

        /// <summary>True if this reason is a commodity we trade.</summary>
        public static bool IsTradable(TransferManager.TransferReason reason)
        {
            return _byReason.ContainsKey(reason);
        }

        /// <summary>Anchor/base price for a reason (generic default if not in the table).</summary>
        public static int BasePrice(TransferManager.TransferReason reason)
        {
            Commodity c;
            return _byReason.TryGetValue(reason, out c) ? c.BasePrice : DefaultBasePrice;
        }

        /// <summary>Display name for a reason (falls back to the enum name).</summary>
        public static string DisplayName(TransferManager.TransferReason reason)
        {
            Commodity c;
            return _byReason.TryGetValue(reason, out c) ? c.DisplayName : reason.ToString();
        }

        // Reverse index from the canonical wire key (the enum NAME, e.g. "Oil", "AnimalProducts") back to
        // the reason. Built from the same table so the online protocol and the local model never disagree.
        private static readonly Dictionary<string, TransferManager.TransferReason> _byKey = BuildKeyIndex();

        /// <summary>Canonical wire key for the online protocol: the stable enum name (NOT the localized
        /// display name). Used by /report (upload) and /prices (download) so server and client agree.</summary>
        public static string Key(TransferManager.TransferReason reason)
        {
            return reason.ToString();
        }

        /// <summary>Resolve a wire key back to its reason. Returns false for unknown keys (net35 has no
        /// generic <c>Enum.TryParse</c>, so we look up our own table instead of parsing).</summary>
        public static bool TryFromKey(string key, out TransferManager.TransferReason reason)
        {
            if (!string.IsNullOrEmpty(key) && _byKey.TryGetValue(key, out reason)) return true;
            reason = TransferManager.TransferReason.None;
            return false;
        }

        private static Dictionary<TransferManager.TransferReason, Commodity> BuildIndex()
        {
            var map = new Dictionary<TransferManager.TransferReason, Commodity>(_all.Length);
            for (int i = 0; i < _all.Length; i++) map[_all[i].Reason] = _all[i];
            return map;
        }

        private static Dictionary<string, TransferManager.TransferReason> BuildKeyIndex()
        {
            var map = new Dictionary<string, TransferManager.TransferReason>(_all.Length);
            for (int i = 0; i < _all.Length; i++) map[_all[i].Reason.ToString()] = _all[i].Reason;
            return map;
        }
    }
}
