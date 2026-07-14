using System.Collections.Generic;
using OpenMarkets.Data;
using OpenMarkets.Net;

namespace OpenMarkets.Market
{
    /// <summary>
    /// M9 price source: a thin consumer of the server's per-commodity index feed (GET /prices). This REPLACES the
    /// old local price simulation as the source of truth:
    ///   • SOLO / offline (no successful poll yet): every commodity trades at its STATIC base price (index 1.0) —
    ///     a flat, predictable, server-free baseline (M9 decision).
    ///   • ONLINE: the server's effective per-commodity index (per-league elasticity × the GLOBAL price event) drives
    ///     the price; <see cref="OnlineSync"/> polls /prices and calls <see cref="Publish"/>.
    ///   • Server unreachable mid-session: FREEZE-AT-LAST — we simply stop publishing, so the last index stays until
    ///     the server returns. Going fully offline (<see cref="Clear"/>) drops back to static base.
    ///
    /// The index is per-COMMODITY (no per-partner price — that dimension is gone in M9). <see cref="GetPrice"/>
    /// therefore ignores the partner id. Events + a short price history ride the same feed for the dashboard.
    ///
    /// Threading: <see cref="Publish"/>/<see cref="Clear"/> run on the MAIN thread (the poll callback);
    /// <see cref="GetPrice"/> runs on the SIM thread (booking); the dashboard reads on the MAIN thread. The three
    /// dictionaries are swapped atomically by reference (volatile), each immutable after publish — the same
    /// lock-free snapshot pattern the old sources used.
    /// </summary>
    public sealed class MarketFeed : IPriceSource
    {
        public static readonly MarketFeed Instance = new MarketFeed();
        private MarketFeed() { }

        // Empty dictionaries = the static-base state (IndexOf → 1.0 → base price). Replaced wholesale by Publish.
        private volatile Dictionary<TransferManager.TransferReason, float> _index =
            new Dictionary<TransferManager.TransferReason, float>();
        private volatile Dictionary<TransferManager.TransferReason, int> _eventPct =
            new Dictionary<TransferManager.TransferReason, int>();
        private volatile Dictionary<TransferManager.TransferReason, int[]> _history =
            new Dictionary<TransferManager.TransferReason, int[]>();
        // Commodities whose event just STARTED on the latest publish (old pct 0 → new pct nonzero), for a one-shot
        // Chirp. Drained by OnlineSync after each publish. MAIN THREAD only.
        private List<TransferManager.TransferReason> _newEvents = new List<TransferManager.TransferReason>();

        // Active SHARED LEAGUE CRISES (social slice 3), published from the /crises poll. Empty = none. Read on the
        // MAIN thread by the dashboard banner; swapped atomically by reference (volatile, immutable after publish).
        private volatile CrisisDto[] _crises = new CrisisDto[0];
        // Crisis names seen on the PREVIOUS /crises publish — diffed to fire a one-shot Chirp for a NEW crisis.
        // Primed on the first publish so existing crises don't all chirp on going online. MAIN THREAD only.
        private readonly HashSet<string> _knownCrises = new HashSet<string>();
        private bool _crisesPrimed;
        private List<CrisisDto> _newCrises = new List<CrisisDto>();

        /// <summary>The current index multiplier for a commodity (1.0 when none — i.e. static base price).</summary>
        public float IndexOf(TransferManager.TransferReason commodity)
        {
            float v;
            return _index.TryGetValue(commodity, out v) && v > 0f ? v : 1.0f;
        }

        /// <summary>SIM THREAD. Per-unit price = base × index (M9: one price per commodity).</summary>
        public int GetPrice(TransferManager.TransferReason commodity)
        {
            return (int)(Commodities.BasePrice(commodity) * IndexOf(commodity) + 0.5f);
        }

        /// <summary>Price of ONE full truckload (<see cref="Commodities.UnitsPerTruck"/> units), in CENTS — the same
        /// figure the booking path mints for a truck. This is the tangible price shown across the UI. Divide by 100 for §.</summary>
        public long PricePerTruckCents(TransferManager.TransferReason commodity)
        {
            return ((long)Commodities.UnitsPerTruck * GetPrice(commodity) + 50) / 100;
        }

        /// <summary>Active price-event swing % for a commodity (0 = none). For the dashboard marker + Chirp.</summary>
        public int EventPct(TransferManager.TransferReason commodity)
        {
            int v;
            return _eventPct.TryGetValue(commodity, out v) ? v : 0;
        }

        /// <summary>Short price history (oldest→newest, in the price unit) for the sparkline; null when none.</summary>
        public int[] History(TransferManager.TransferReason commodity)
        {
            int[] v;
            return _history.TryGetValue(commodity, out v) ? v : null;
        }

        /// <summary>MAIN THREAD. Replace the cache from a server /prices response. Unknown commodity keys are
        /// skipped. History arrives as the effective-index ring and is converted to the price unit (base × index)
        /// so the dashboard sparkline stays in one unit. Atomic swap — readers see the old or new map, never a torn
        /// one.</summary>
        public void Publish(PricesDto dto)
        {
            if (dto == null || dto.commodities == null) return;
            var index = new Dictionary<TransferManager.TransferReason, float>(dto.commodities.Length);
            var events = new Dictionary<TransferManager.TransferReason, int>(dto.commodities.Length);
            var history = new Dictionary<TransferManager.TransferReason, int[]>(dto.commodities.Length);
            for (int i = 0; i < dto.commodities.Length; i++)
            {
                CommodityIndexDto c = dto.commodities[i];
                if (c == null || string.IsNullOrEmpty(c.commodity)) continue;
                TransferManager.TransferReason reason;
                if (!Commodities.TryFromKey(c.commodity, out reason)) continue;
                index[reason] = c.index > 0f ? c.index : 1.0f;
                events[reason] = c.eventPct;
                // New event = was neutral (0/absent) before, nonzero now → queue a one-shot Chirp.
                int prev;
                if (c.eventPct != 0 && (!_eventPct.TryGetValue(reason, out prev) || prev == 0))
                    _newEvents.Add(reason);
                if (c.history != null && c.history.Length > 0)
                {
                    int basePrice = Commodities.BasePrice(reason);
                    int[] ph = new int[c.history.Length];
                    for (int h = 0; h < c.history.Length; h++) ph[h] = (int)(basePrice * c.history[h] + 0.5f);
                    history[reason] = ph;
                }
            }
            _index = index;
            _eventPct = events;
            _history = history;
        }

        /// <summary>MAIN THREAD. Return + clear the commodities whose event just started (for a one-shot Chirp).</summary>
        public List<TransferManager.TransferReason> TakeNewEvents()
        {
            List<TransferManager.TransferReason> n = _newEvents;
            _newEvents = new List<TransferManager.TransferReason>();
            return n;
        }

        /// <summary>The active shared league crises (empty when none). MAIN THREAD read for the dashboard banner.</summary>
        public CrisisDto[] Crises { get { return _crises; } }

        /// <summary>MAIN THREAD. Replace the active-crisis list from a /crises response, diffing names so a NEWLY
        /// appeared crisis is queued for a one-shot Chirp (primed on the first publish so existing crises don't all
        /// chirp on going online). A null/empty dto clears the list. Atomic swap.</summary>
        public void PublishCrises(CrisesDto dto)
        {
            CrisisDto[] list = (dto != null && dto.crises != null) ? dto.crises : new CrisisDto[0];
            // Diff against last-seen names: a name present now but not before is new.
            HashSet<string> now = new HashSet<string>();
            for (int i = 0; i < list.Length; i++)
            {
                CrisisDto c = list[i];
                if (c == null || string.IsNullOrEmpty(c.name)) continue;
                now.Add(c.name);
                if (_crisesPrimed && !_knownCrises.Contains(c.name)) _newCrises.Add(c);
            }
            _knownCrises.Clear();
            foreach (string n in now) _knownCrises.Add(n);
            _crisesPrimed = true;
            _crises = list;
        }

        /// <summary>MAIN THREAD. Return + clear the crises that just appeared since the last publish (one-shot Chirp).</summary>
        public List<CrisisDto> TakeNewCrises()
        {
            List<CrisisDto> n = _newCrises;
            _newCrises = new List<CrisisDto>();
            return n;
        }

        /// <summary>MAIN THREAD. Drop back to static base prices (going offline / level unload).</summary>
        public void Clear()
        {
            _index = new Dictionary<TransferManager.TransferReason, float>();
            _eventPct = new Dictionary<TransferManager.TransferReason, int>();
            _history = new Dictionary<TransferManager.TransferReason, int[]>();
            _newEvents = new List<TransferManager.TransferReason>();
            _crises = new CrisisDto[0];
            _knownCrises.Clear();
            _newCrises = new List<CrisisDto>();
            _crisesPrimed = false;
        }
    }
}
