using System;
using System.Collections.Generic;
using System.IO;
using ICities;
using OpenMarkets.Market;

namespace OpenMarkets.Persistence
{
    /// <summary>
    /// Per-save persistence (NFR-3). Stores a single versioned blob under our own data id: lifetime trade tallies
    /// (§3) + the append-only §7–§10 tail (settlement/delivery/tax-lock/budget-lock/city-token/price-alerts). v11 (M10) appends §12 (per-commodity
    /// price-alert thresholds). v8 (M9) retired the early
    /// §1/§2/§4/§5 sections; v9 retired §6 (the legacy contract ledger); the reader is version-gated so pre-v9
    /// saves still load (their §6 — and pre-v8 §1/§2/§4/§5 — are read + discarded). Reads are fully guarded
    /// and fall back to clean defaults on any error or absent blob, so a save made with the mod still loads without
    /// it (and a save made without the mod loads with it). We never touch vanilla manager data.
    ///
    /// Runs on the simulation thread (ISerializableDataExtension).
    /// </summary>
    public sealed class SaveData : SerializableDataExtensionBase
    {
        private const string DataId = "OpenMarkets";
        // v8 (M9): the early sections §1 (local prices), §2 (partner names), §4 (embargo), §5 (per-partner) are GONE
        // — the price model is server-owned and partner-less. v9 additionally RETIRES the legacy single-commodity
        // contract system, so §6 (the contract settlement ledger) is no longer WRITTEN. A v9 blob is just:
        // version · §3 lifetime tallies · the append-only §7–§10 tail. The reader is version-gated so older saves
        // still load cleanly: pre-v8 saves' §1/§2/§4/§5 are read + discarded, and v8 saves' §6 is read + discarded,
        // keeping the stream aligned for the §7+ sections that follow.
        private const int Version = 11;

        public override void OnSaveData()
        {
            try
            {
                byte[] blob;
                using (MemoryStream ms = new MemoryStream())
                using (BinaryWriter w = new BinaryWriter(ms))
                {
                    w.Write(Version);

                    // Section 3: lifetime income tallies (cents). §1/§2/§4/§5 are RETIRED in v8 (not written).
                    w.Write(PricingService.LifetimeExportCents);
                    w.Write(PricingService.LifetimeImportCents);

                    // Section 6 (v2): the contract settlement ledger is RETIRED in v9 (the single-commodity contract
                    // system is gone) — no longer written. Older saves that still carry it are read + discarded on
                    // load (see OnLoadData), keeping the stream aligned for the §7+ sections below.

                    // Section 7 (v3): settlement-event cursor — highest /settlements seq booked locally, per
                    // league, so a reload can't re-book already-applied trade/bond installments. Append-only.
                    List<KeyValuePair<string, long>> seqs = SettlementLedger.Entries();
                    w.Write(seqs.Count);
                    for (int i = 0; i < seqs.Count; i++)
                    {
                        w.Write(seqs[i].Key ?? string.Empty);
                        w.Write(seqs[i].Value);
                    }
                    // (v4) the synced server epoch, appended after the cursors — lets the client detect a server
                    // data wipe across sessions and reset its cursors. Append-only; a v3 reader stops before it.
                    w.Write(SettlementLedger.ServerEpoch ?? string.Empty);

                    // Section 8 (v5): per-trade delivery cursor — installments already physically delivered
                    // (give-goods removed + receive-goods added) so a reload can't double-move goods. Append-only.
                    List<KeyValuePair<string, int>> delivered = DeliveryLedger.Entries();
                    w.Write(delivered.Count);
                    for (int i = 0; i < delivered.Count; i++)
                    {
                        w.Write(delivered[i].Key ?? string.Empty);
                        w.Write(delivered[i].Value);
                    }

                    // Section 9 (v6): austerity tax-lock — the locked flag + the player's stashed pre-austerity tax
                    // rates, so a save made mid-austerity restores the REAL rates on reload (the forced rates are
                    // baked into the vanilla save). Append-only.
                    w.Write(TaxLock.PersistLocked);
                    int[] taxStash = TaxLock.PersistSaved();
                    w.Write(taxStash != null ? taxStash.Length : 0);
                    if (taxStash != null)
                        for (int i = 0; i < taxStash.Length; i++) w.Write(taxStash[i]);

                    // Section 10 (v7): austerity budget cap — the locked flag + the player's stashed pre-austerity
                    // day/night budgets, so a save made mid-austerity restores the REAL budgets on reload (the capped
                    // budgets are baked into the vanilla save). Append-only.
                    w.Write(BudgetLock.PersistLocked);
                    WriteIntArray(w, BudgetLock.PersistSavedDay());
                    WriteIntArray(w, BudgetLock.PersistSavedNight());

                    // Section 11 (v10): this save's stable per-CITY token (CityIdentity). Persisting it in the blob is
                    // what makes the token survive autosaves/Save-As of the same city (so they all share one league
                    // identity) while a brand-new city gets a fresh one. Append-only; empty if never went online.
                    w.Write(CityIdentity.Token ?? string.Empty);

                    // Section 12 (v11): per-commodity PRICE-ALERT thresholds (§/truck) the player set in the Market
                    // tab, so an alert survives a reload. Append-only: count, then each (commodity wire key, §/truck).
                    // The crossing baseline + fired flags are session state (not saved) — they re-seed on the next poll.
                    List<KeyValuePair<string, long>> alerts = Market.PriceAlerts.Entries();
                    w.Write(alerts.Count);
                    for (int i = 0; i < alerts.Count; i++)
                    {
                        w.Write(alerts[i].Key ?? string.Empty);
                        w.Write(alerts[i].Value);
                    }

                    w.Flush();
                    blob = ms.ToArray();
                }

                serializableDataManager.SaveData(DataId, blob);
                Log.Info("Saved market state (" + blob.Length + " bytes).");
            }
            catch (Exception e)
            {
                Log.Error("OnSaveData failed (save will be missing market state): " + e);
            }
        }

        public override void OnLoadData()
        {
            try
            {
                byte[] blob = serializableDataManager.LoadData(DataId);
                if (blob == null || blob.Length == 0) return; // fresh / mod added to an existing save

                using (MemoryStream ms = new MemoryStream(blob))
                using (BinaryReader r = new BinaryReader(ms))
                {
                    int version = r.ReadInt32();
                    if (version < 1 || version > Version) return; // unknown/older/newer-than-known → keep defaults
                                                                  // (a future blob must not be mis-parsed as v1)

                    LocalPriceSim.Instance.Clear();

                    if (version <= 7)
                    {
                        // LEGACY layout (pre-M9): §1 prices · §2 names · §3 lifetime · §4 embargo · §5 per-partner.
                        // Only §3 carries forward; the rest are retired, so read + DISCARD them (exact field sizes,
                        // so the stream advances correctly to the §6+ append-only tail). The per-section position
                        // guards match the original reader so a save predating a later section still loads.
                        int priceCount = r.ReadInt32();
                        for (int i = 0; i < priceCount; i++) { r.ReadInt64(); r.ReadInt32(); } // §1 prices

                        if (ms.Position < ms.Length) // §2 names
                        {
                            int nameCount = r.ReadInt32();
                            for (int i = 0; i < nameCount; i++) { r.ReadUInt16(); r.ReadString(); }
                        }
                        if (ms.Position < ms.Length) // §3 lifetime (kept)
                            PricingService.SetLifetime(r.ReadInt64(), r.ReadInt64());
                        if (ms.Position < ms.Length) // §4 embargo
                        {
                            int embargoCount = r.ReadInt32();
                            for (int i = 0; i < embargoCount; i++) r.ReadInt64();
                        }
                        if (ms.Position < ms.Length) // §5 per-partner
                        {
                            int partnerCount = r.ReadInt32();
                            for (int i = 0; i < partnerCount; i++) { r.ReadUInt16(); r.ReadInt64(); r.ReadInt64(); }
                        }
                    }
                    else // v8+ COMPACT layout: §3 lifetime only (the retired sections aren't written).
                    {
                        if (ms.Position < ms.Length)
                            PricingService.SetLifetime(r.ReadInt64(), r.ReadInt64());
                    }

                    // Section 6 (v2..v8): contract settlement ledger — RETIRED in v9 (the contract system is gone).
                    // A v8-or-older blob still carries it, so read + DISCARD the exact bytes (count, then each
                    // string+int entry) to keep the stream aligned for the §7+ sections that follow. A v9+ blob
                    // never wrote it, so we MUST NOT read it here (that would consume §7's bytes). A v1 save lacks
                    // it too, so the position guard skips it. Discarding is safe: contract cash is no longer booked.
                    if (version <= 8 && ms.Position < ms.Length)
                    {
                        int ledgerCount = r.ReadInt32();
                        for (int i = 0; i < ledgerCount; i++)
                        {
                            r.ReadString(); // contractId (discarded)
                            r.ReadInt32();  // booked count (discarded)
                        }
                    }

                    // Section 7 (v3): settlement-event cursor. Append-only — a v1/v2 save lacks it, so the
                    // position guard skips it and the cursor stays empty (clean default → re-fetch from 0).
                    if (ms.Position < ms.Length)
                    {
                        int seqCount = r.ReadInt32();
                        for (int i = 0; i < seqCount; i++)
                        {
                            string leagueId = r.ReadString();
                            long seq = r.ReadInt64();
                            SettlementLedger.Restore(leagueId, seq);
                        }
                        // (v4) the synced server epoch, if this save has it (a v3 save stops at the cursors above).
                        if (ms.Position < ms.Length)
                        {
                            SettlementLedger.SetServerEpoch(r.ReadString());
                        }
                    }

                    // Section 8 (v5): per-trade delivery cursor. Append-only — a v1–v4 save lacks it, so the
                    // position guard skips it and the cursor stays empty (clean default → re-derive from server).
                    if (ms.Position < ms.Length)
                    {
                        int deliveredCount = r.ReadInt32();
                        for (int i = 0; i < deliveredCount; i++)
                        {
                            string tradeId = r.ReadString();
                            int inst = r.ReadInt32();
                            DeliveryLedger.Restore(tradeId, inst);
                        }
                    }

                    // Section 9 (v6): austerity tax-lock state. Append-only — a v1–v5 save lacks it (stays
                    // unlocked, stash empty); the lock re-engages from /citystate if still in austerity.
                    if (ms.Position < ms.Length)
                    {
                        bool taxLocked = r.ReadBoolean();
                        int taxLen = r.ReadInt32();
                        int[] taxStash = taxLen > 0 ? new int[taxLen] : null;
                        for (int i = 0; i < taxLen; i++) taxStash[i] = r.ReadInt32();
                        TaxLock.Restore(taxLocked, taxStash);
                    }

                    // Section 10 (v7): austerity budget-cap state. Append-only — a v1–v6 save lacks it (stays
                    // uncapped, stash empty); the cap re-engages from /citystate if still in austerity.
                    if (ms.Position < ms.Length)
                    {
                        bool budgetLocked = r.ReadBoolean();
                        int[] budgetDay = ReadIntArray(r);
                        int[] budgetNight = ReadIntArray(r);
                        BudgetLock.Restore(budgetLocked, budgetDay, budgetNight);
                    }

                    // Section 11 (v10): this save's per-city token. Append-only — a v1–v9 save lacks it, so the
                    // position guard skips it and the token stays empty (minted fresh the next time the city goes
                    // online; the first configured city to load then auto-binds as the league city).
                    if (ms.Position < ms.Length)
                        CityIdentity.Restore(r.ReadString());

                    // Section 12 (v11): per-commodity price-alert thresholds. Append-only — a v1–v10 save lacks it,
                    // so the position guard skips it and no alerts are armed (clean default). Unknown commodity keys
                    // are skipped by Restore, so removing a commodity later still loads.
                    if (ms.Position < ms.Length)
                    {
                        int alertCount = r.ReadInt32();
                        for (int i = 0; i < alertCount; i++)
                        {
                            string key = r.ReadString();
                            long truckCents = r.ReadInt64();
                            Market.PriceAlerts.Restore(key, truckCents);
                        }
                    }
                }

                Log.Info("Loaded market state (lifetime net §" + (PricingService.LifetimeNetCents / 100) + ").");
            }
            catch (Exception e)
            {
                Log.Error("OnLoadData failed (using clean defaults): " + e);
            }
        }

        // Length-prefixed int[] (length 0 ⇒ a null/empty array). Used for the budget-cap stash (§10).
        private static void WriteIntArray(System.IO.BinaryWriter w, int[] arr)
        {
            w.Write(arr != null ? arr.Length : 0);
            if (arr != null)
                for (int i = 0; i < arr.Length; i++) w.Write(arr[i]);
        }

        private static int[] ReadIntArray(System.IO.BinaryReader r)
        {
            int len = r.ReadInt32();
            if (len <= 0) return null;
            int[] arr = new int[len];
            for (int i = 0; i < len; i++) arr[i] = r.ReadInt32();
            return arr;
        }
    }
}
