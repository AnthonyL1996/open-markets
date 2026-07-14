using System.Threading;
using ColossalFramework;

namespace OpenMarkets.Market
{
    /// <summary>
    /// Books mod-owned trade income at the cargo-arrival attribution point. This is the money core:
    /// vanilla base-game trade pays nothing and the Industries chain is neutralized
    /// (<see cref="OpenMarkets.Patches.IndustryResourcePricePatch"/>), so the mod owns 100% of trade
    /// income under one uniform model.
    ///
    /// EXPORT is always credited (additive, taxation untouched). IMPORT is charged when the
    /// <see cref="Settings.ChargeImports"/> toggle is on (ON by default; forced on under online play). M9: both legs
    /// settle at the market index EXACTLY — one price per commodity from the <see cref="MarketFeed"/> (the league
    /// index when online, static base prices solo), no bid/ask spread and no per-partner pricing. Called from the
    /// cargo-arrival prefixes, i.e. the SIMULATION thread — the same thread the vanilla Industries chain books on,
    /// so EconomyManager access and the daily counters are safe here.
    /// </summary>
    public static class PricingService
    {
        // volatile: written on the main thread (SetSource, from OnLevelLoaded) and read on the sim thread
        // (Money → GetPrice, per booking). volatile fences the publish so the sim thread sees the swap.
        // Default is the MarketFeed (static base prices until WirePriceSource confirms it / the server feed lands).
        private static volatile IPriceSource _source = MarketFeed.Instance;

        // Daily tallies, drained by the daily summary log. long (a busy in-game day can exceed int cents).
        private static long _dayExportCents, _dayImportCents;
        private static int _dayExportCount, _dayImportCount;

        // Lifetime tallies (this save), persisted by SaveData; shown in the dashboard. Written on the sim
        // thread, read on the UI thread — use Interlocked so the 64-bit reads aren't torn on 32-bit Mono.
        private static long _lifeExportCents, _lifeImportCents;
        public static long LifetimeExportCents { get { return Interlocked.Read(ref _lifeExportCents); } }
        public static long LifetimeImportCents { get { return Interlocked.Read(ref _lifeImportCents); } }
        public static long LifetimeNetCents { get { return Interlocked.Read(ref _lifeExportCents) - Interlocked.Read(ref _lifeImportCents); } }

        /// <summary>Swap the price source. Ignores null.</summary>
        public static void SetSource(IPriceSource source)
        {
            if (source != null) _source = source;
        }

        /// <summary>The price source the mod books against (the MarketFeed). Sim-thread read of the volatile swap.</summary>
        public static IPriceSource ActiveSource { get { return _source; } }

        /// <summary>
        /// Credit the treasury for one export leg; returns money booked (cents, 0 if none).
        /// <paramref name="cityBuildingId"/> = the city-side (source) building, used only to route the
        /// income-breakdown line — cash is added regardless.
        /// </summary>
        public static int BookExport(TransferManager.TransferReason commodity, int amount, ushort cityBuildingId)
        {
            int money = Money(commodity, amount);
            if (money <= 0) return 0;

            // PRICE EDGE (Great Works reward): a builder books a better §/truck when SELLING a themed commodity vs.
            // the OUTSIDE world. This void-sourced income has no counterparty (it's minted, like vanilla trade
            // income), so scaling it conserves nothing-that-needs-conserving; peer contract settlement goes through
            // BookContractSettlement and is deliberately NOT touched. Server-granted + time-boxed via /citystate —
            // the client only multiplies. EXPORT only (import/settlement excluded). Saturating to int.MaxValue.
            int edgeBips = CoopBuff.EdgeBipsFor(commodity);
            if (edgeBips > 0)
                money = (int)System.Math.Min(int.MaxValue, (long)money * (10000 + edgeBips) / 10000);

            ItemClass itemClass = ClassOf(cityBuildingId);
            EconomyManager econ = Singleton<EconomyManager>.instance;
            if (itemClass != null)
                econ.AddResource(EconomyManager.Resource.ResourcePrice, money, itemClass);
            else
                econ.AddResource(EconomyManager.Resource.ResourcePrice, money,
                    ItemClass.Service.None, ItemClass.SubService.None, ItemClass.Level.None);

            _dayExportCents += money;
            _dayExportCount++;
            Interlocked.Add(ref _lifeExportCents, money);
            if (Settings.IsDebugLogging)
                Log.Info("Book EXPORT " + commodity + " x" + amount + ": §" + (PerTruckCents(commodity) / 100)
                         + "/truck → +§" + (money / 100));
            return money;
        }

        /// <summary>
        /// Charge the treasury for one import leg when the import-charge toggle is on; returns money
        /// charged (cents, 0 if disabled / none). <paramref name="cityBuildingId"/> = the city-side
        /// (target) building.
        /// </summary>
        public static int BookImport(TransferManager.TransferReason commodity, int amount, ushort cityBuildingId)
        {
            if (!Settings.IsChargeImports) return 0;

            int money = Money(commodity, amount);
            if (money <= 0) return 0;

            ItemClass itemClass = ClassOf(cityBuildingId);
            EconomyManager econ = Singleton<EconomyManager>.instance;
            if (itemClass != null)
                econ.FetchResource(EconomyManager.Resource.ResourcePrice, money, itemClass);
            else
                econ.FetchResource(EconomyManager.Resource.ResourcePrice, money,
                    ItemClass.Service.None, ItemClass.SubService.None, ItemClass.Level.None);

            _dayImportCents += money;
            _dayImportCount++;
            Interlocked.Add(ref _lifeImportCents, money);
            if (Settings.IsDebugLogging)
                Log.Info("Book IMPORT " + commodity + " x" + amount + ": §" + (PerTruckCents(commodity) / 100)
                         + "/truck → -§" + (money / 100));
            return money;
        }

        // Contracts settle in cash but are NOT trades — keep them out of the trade ledger (lifetime/day/
        // per-partner) so /report's net-supply numbers and the Balance tab stay clean. A separate in-memory
        // net tally (not persisted; derivable from server contract data) is exposed for display only.
        private static long _contractNetCents;
        public static long ContractNetCents { get { return Interlocked.Read(ref _contractNetCents); } }

        /// <summary>
        /// Settle one contract installment in in-game cash: credit (seller receives) or debit (buyer pays)
        /// <paramref name="cents"/>. SIM THREAD ONLY (mutates <c>EconomyManager</c>) — callers marshal via
        /// <c>SimulationManager.instance.AddAction</c>. A contract has no city building, so it uses the
        /// Service.None overload (no income-breakdown routing); it touches only the contracts tally, never
        /// the trade ledger. No-op for non-positive amounts.
        /// </summary>
        public static void BookContractSettlement(int cents, bool credit)
        {
            if (cents <= 0) return;
            EconomyManager econ = Singleton<EconomyManager>.instance;
            if (credit)
            {
                econ.AddResource(EconomyManager.Resource.ResourcePrice, cents,
                    ItemClass.Service.None, ItemClass.SubService.None, ItemClass.Level.None);
                Interlocked.Add(ref _contractNetCents, cents);
            }
            else
            {
                econ.FetchResource(EconomyManager.Resource.ResourcePrice, cents,
                    ItemClass.Service.None, ItemClass.SubService.None, ItemClass.Level.None);
                Interlocked.Add(ref _contractNetCents, -cents);
            }
            if (Settings.IsDebugLogging)
                Log.Info("Book CONTRACT " + (credit ? "+§" : "-§") + (cents / 100) + " (settlement).");
        }

        /// <summary>Read and reset the day's tallies (called by the daily summary log).</summary>
        public static void DrainDailyTotals(out long exportCents, out int exportCount,
            out long importCents, out int importCount)
        {
            exportCents = _dayExportCents;
            exportCount = _dayExportCount;
            importCents = _dayImportCents;
            importCount = _dayImportCount;
            ResetDailyTotals();
        }

        /// <summary>Clear the day's tallies without logging (level unload, so counts don't bleed cities).</summary>
        public static void ResetDailyTotals()
        {
            _dayExportCents = _dayImportCents = 0;
            _dayExportCount = _dayImportCount = 0;
        }

        /// <summary>Restore persisted lifetime tallies on load.</summary>
        public static void SetLifetime(long exportCents, long importCents)
        {
            Interlocked.Exchange(ref _lifeExportCents, exportCents);
            Interlocked.Exchange(ref _lifeImportCents, importCents);
        }

        /// <summary>Clear lifetime tallies (level unload — re-loaded from the save).</summary>
        public static void ResetLifetime()
        {
            Interlocked.Exchange(ref _lifeExportCents, 0L);
            Interlocked.Exchange(ref _lifeImportCents, 0L);
        }

        // Mirror vanilla rounding (IndustryBuildingAI): money cents = (units * priceIndex + 50) / 100. Computed in
        // long (units can reach ~1e9 on a large consist), then clamped to int for EconomyManager. M9: no spread —
        // both legs book at the market index exactly (one price per commodity).
        private static int Money(TransferManager.TransferReason commodity, int amount)
        {
            if (amount <= 0) return 0;
            int price = _source.GetPrice(commodity);
            if (price <= 0) return 0;

            long money = ((long)amount * price + 50) / 100;
            if (money <= 0) return 0;
            return money > int.MaxValue ? int.MaxValue : (int)money;
        }

        // Cents for one full truckload of a commodity at the current price — for the per-truck booking log.
        private static long PerTruckCents(TransferManager.TransferReason commodity)
        {
            return ((long)OpenMarkets.Data.Commodities.UnitsPerTruck * _source.GetPrice(commodity) + 50) / 100;
        }

        private static ItemClass ClassOf(ushort buildingId)
        {
            if (buildingId == 0) return null;
            BuildingInfo info = BuildingManager.instance.m_buildings.m_buffer[buildingId].Info;
            return info != null ? info.m_class : null;
        }
    }
}
