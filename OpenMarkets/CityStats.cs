using System;
using ColossalFramework;
using OpenMarkets.Net;

namespace OpenMarkets
{
    /// <summary>
    /// Gathers this city's own stats into a <see cref="CityProfilePostDto"/> for the once-per-in-game-day
    /// leaguemate profile report. Every value is a cheap field read off the game-maintained citywide aggregate
    /// (District index 0) plus a couple of manager singletons — NO BuildingManager/CitizenManager iteration.
    ///
    /// SIM THREAD ONLY: it reads simulation/manager data (the day-rollover hook already runs there). Wrapped in
    /// try/catch → returns null on any failure (e.g. a manager not ready) so the caller simply skips the post.
    /// </summary>
    internal static class CityStats
    {
        public static CityProfilePostDto Gather()
        {
            try
            {
                DistrictManager dm = Singleton<DistrictManager>.instance;
                if (dm == null) return null;
                // The citywide aggregate the game maintains every sim step. Index the buffer per field (no big copy).
                District[] d = dm.m_districts.m_buffer;

                var p = new CityProfilePostDto();

                // Vitals
                p.population = (int)d[0].m_populationData.m_finalCount;
                p.happiness = d[0].m_finalHappiness;            // 0..100 (city popularity)
                p.crime = d[0].m_finalCrimeRate;                // 0..100
                p.unemployment = d[0].GetUnemployment();        // 0..100
                p.landValue = d[0].GetLandValue();

                ImmaterialResourceManager irm = Singleton<ImmaterialResourceManager>.instance;
                if (irm != null)
                {
                    int attr;
                    irm.CheckGlobalResource(ImmaterialResourceManager.Resource.Attractiveness, out attr);
                    p.attractiveness = attr;
                    p.tourists = irm.CheckActualTourismResource(); // tourism index (Attractiveness + LandValue blend)
                }

                EconomyManager econ = Singleton<EconomyManager>.instance;
                if (econ != null)
                {
                    p.cashCents = econ.InternalCashAmount; // cents
                    long income, expenses;
                    econ.GetIncomeAndExpenses(ItemClass.Service.None, ItemClass.SubService.None, ItemClass.Level.None,
                        out income, out expenses);
                    p.weeklyIncomeCents = income;
                    p.weeklyExpensesCents = expenses;
                }

                // Sector breakdown (private-zone building counts + industrial workers).
                p.resBuildings = d[0].m_residentialData.m_finalBuildingCount;
                p.comBuildings = d[0].m_commercialData.m_finalBuildingCount;
                p.offBuildings = d[0].m_officeData.m_finalBuildingCount;
                p.indBuildings = d[0].m_industrialData.m_finalBuildingCount;
                p.indWorkers = (int)d[0].m_industrialData.m_finalAliveCount;

                // Specialized industry workers (Industries DLC; read 0 without it — no DLC guard needed to read).
                p.farmWorkers = (int)d[0].m_farmingData.m_finalAliveCount;
                p.forestWorkers = (int)d[0].m_forestryData.m_finalAliveCount;
                p.oreWorkers = (int)d[0].m_oreData.m_finalAliveCount;
                p.oilWorkers = (int)d[0].m_oilData.m_finalAliveCount;

                BuildingManager bm = Singleton<BuildingManager>.instance;
                if (bm != null) p.buildingCount = (int)bm.m_buildingCount;

                SimulationManager sm = Singleton<SimulationManager>.instance;
                if (sm != null && sm.m_metaData != null) p.cityName = sm.m_metaData.m_CityName;

                return p;
            }
            catch (Exception e)
            {
                Log.Error("CityStats.Gather failed: " + e);
                return null;
            }
        }
    }
}
