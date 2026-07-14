using System.Collections.Generic;
using System.Globalization;
using ColossalFramework.UI;
using OpenMarkets.Data;
using OpenMarkets.Net;
using OpenMarkets.Trade;
using UnityEngine;

namespace OpenMarkets.UI.Terminal
{
    /// <summary>
    /// Projects tab: the league's co-op MEGAPROJECT (a Great Work, social slice 4). Shows the active (open)
    /// project — its name, description, a progress bar per commodity requirement and the § requirement — plus a
    /// CONTRIBUTE control: pick a commodity your city HAS in its [trade] depots, enter a quantity, and Contribute
    /// → the units are REMOVED from your depots (the M6 InventoryService.MoveStock path, on the sim thread) and
    /// then posted to the server. A separate "Contribute §" field pays § toward the project (the § transfers out
    /// of your treasury, conserving cash on the server). On completion every builder receives a lasting demand +
    /// attractiveness buff automatically via the existing CoopBuff/citystate path — no client work here.
    ///
    /// Data is fetched async on Refresh (<see cref="OmApi.GetProjects"/>); goods removal is marshalled to the sim
    /// thread, everything else (UI + HTTP) is main-thread. MAIN THREAD.
    /// </summary>
    internal sealed class ProjectsTab : ITabBody
    {
        private const float Width = 520f;
        private const float BarW = 200f;
        private const float FormH = 92f;
        // Cap the builders list rendered per Rebuild so a long roster can't spawn thousands of CF components.
        private const int MaxBuilderRows = 100;
        private const long ProjectRewardFloorNum = 1L;
        private const long ProjectRewardFloorDen = 2L;
        private const long CentsPerDemandPoint = 250000L;
        private const long CentsPerAttractPoint = 10000L;
        private const int DemandBoostCap = 20;
        private const int AttractRateCap = 500;

        private UIPanel _root;
        private UIScrollablePanel _grid;
        private ProjectsDto _projects;
        private bool _loading;
        private bool _failed;

        // Contribute form state.
        private UIButton _commodityBtn;   // cycles a commodity the active project still needs AND we have stock of
        private UITextField _qtyField;
        private UIButton _contributeBtn;
        private UITextField _goldField;
        private UIButton _goldBtn;
        private UILabel _statusLabel;
        private string _commodityKey = string.Empty;  // wire key of the currently-picked contribute commodity
        private bool _sending;

        public string TabLabel { get { return "Projects"; } }

        public string Title
        {
            get
            {
                ProjectDto p = ActiveProject();
                if (p != null && !string.IsNullOrEmpty(p.name)) return "Open Markets — " + p.name;
                return "Open Markets — Great Works";
            }
        }

        public void Build(UIComponent host, Vector2 size)
        {
            _root = host.AddUIComponent<UIPanel>();
            _root.relativePosition = Vector3.zero;
            _root.size = size;
            _root.autoLayout = false;

            // Contribute form (top strip): commodity picker + qty + Contribute, and a § field + Contribute §.
            _commodityBtn = MakeButton(2f, 4f, 220f, "Give: —");
            _commodityBtn.eventClicked += delegate { CycleCommodity(); SyncForm(); };
            _commodityBtn.tooltip = "Cycle which commodity to contribute (only ones the project still needs and your "
                + "[trade] depots stock). The trucks are removed from your depots.";
            _qtyField = Field(226f, 4f, 84f);
            _qtyField.text = "1";
            _qtyField.tooltip = "Quantity in TRUCKS (1 truck = " + Commodities.UnitsPerTruck.ToString("N0")
                + " units) — the same unit the project needs and the Trade tab uses.";
            _qtyField.eventTextChanged += delegate { SyncForm(); };
            _contributeBtn = MakeButton(314f, 4f, 96f, "Contribute");
            _contributeBtn.eventClicked += delegate { SubmitGoods(); };
            _contributeBtn.tooltip = "Remove this many trucks from your [trade] depots and contribute them to the Great Work.";

            Caption("§", 2f, 36f);
            _goldField = Field(18f, 32f, 100f);
            _goldField.text = "10000";
            _goldField.eventTextChanged += delegate { SyncForm(); };
            _goldBtn = MakeButton(122f, 32f, 110f, "Contribute §");
            _goldBtn.eventClicked += delegate { SubmitGold(); };
            _goldBtn.tooltip = "Pay § toward the Great Work — it transfers out of your treasury.";
            _statusLabel = SmallLabel(240f, 36f, UiKit.Dim);

            _grid = _root.AddUIComponent<UIScrollablePanel>();
            _grid.relativePosition = new Vector3(0f, FormH);
            _grid.size = new Vector2(size.x, size.y - FormH);
            _grid.autoLayout = true;
            _grid.autoLayoutDirection = LayoutDirection.Vertical;
            _grid.autoLayoutPadding = new RectOffset(0, 0, 0, 2);
            _grid.clipChildren = true;
            _grid.scrollWheelDirection = UIOrientation.Vertical;

            SyncForm();
        }

        public void SetVisible(bool on) { if (_root != null) _root.isVisible = on; }

        public void Refresh()
        {
            if (_grid == null) return;
            InventoryService.Scan();   // refresh the local [trade]-depot snapshot so the picker + caps are current
            SyncForm();
            Rebuild();
            if (!Settings.IsOnlineConfigured || _loading) return;
            _loading = true;
            OmApi.GetProjects(delegate (bool ok, ProjectsDto dto)
            {
                _loading = false;
                _failed = !ok || dto == null;
                if (!_failed) _projects = dto;
                SyncForm();   // the active project (and thus the needed commodities) may have changed
                Rebuild();    // callback is on the main thread (OmHttp contract)
            });
        }

        private void Rebuild()
        {
            if (_grid == null) return;
            List<UIComponent> old = new List<UIComponent>(_grid.components);
            for (int i = 0; i < old.Count; i++) { _grid.RemoveUIComponent(old[i]); Object.Destroy(old[i].gameObject); }

            ProjectDto p = ActiveProject();
            if (p == null)
            {
                string note = _loading ? "Loading..."
                    : _failed ? "Couldn't reach the server — try Refresh."
                    : "No active project.";
                UiKit.Cellate(NewRow(), Width, note, UiKit.Dim, UIHorizontalAlignment.Left);
                RenderHallOfGreatWorks();
                return;
            }

            // Heading: name + description.
            UiKit.Cellate(NewRow(), Width, p.name, UiKit.Head, UIHorizontalAlignment.Left);
            if (!string.IsNullOrEmpty(p.description))
            {
                UIPanel descRow = NewWideRow(34f);
                UILabel desc = descRow.AddUIComponent<UILabel>();
                desc.autoSize = false; desc.wordWrap = true;
                desc.size = new Vector2(Width, 32f);
                desc.textScale = 0.72f;
                desc.textColor = UiKit.Dim;
                desc.text = p.description;
            }
            NewRow();
            UiKit.Cellate(NewRow(), Width, "Reward on completion:", UiKit.Head, UIHorizontalAlignment.Left);
            UiKit.Cellate(NewRow(), Width, "  " + RewardOnCompletionText(p), UiKit.Up, UIHorizontalAlignment.Left);
            NewRow(); // spacer

            // Per-commodity progress rows.
            UiKit.Cellate(NewRow(), Width, "Requirements:", UiKit.Head, UIHorizontalAlignment.Left);
            if (p.reqs != null)
            {
                for (int i = 0; i < p.reqs.Length; i++)
                {
                    ProjectReqDto r = p.reqs[i];
                    if (r == null) continue;
                    long have = ContributedUnits(p, r.commodity);
                    ProgressRow(DisplayNameOf(r.commodity), have, r.qty);
                }
            }
            // § requirement row.
            if (p.goldReqCents > 0)
                ProgressRow("§ (treasury)", p.gold / 100L, p.goldReqCents / 100L);

            NewRow();
            UiKit.Cellate(NewRow(), Width, "Your projected reward:", UiKit.Head, UIHorizontalAlignment.Left);
            UiKit.Cellate(NewRow(), Width, "  " + ProjectedRewardText(p),
                BuilderScore(p, Settings.AccountIdValue) > 0L ? UiKit.Up : UiKit.Dim, UIHorizontalAlignment.Left);

            // Builders.
            if (p.by != null && p.by.Length > 0)
            {
                NewRow();
                UiKit.Cellate(NewRow(), Width, "Builders:", UiKit.Head, UIHorizontalAlignment.Left);
                int builderRows = p.by.Length < MaxBuilderRows ? p.by.Length : MaxBuilderRows;
                for (int i = 0; i < builderRows; i++)
                {
                    BuilderPairDto b = p.by[i];
                    if (b == null) continue;
                    UiKit.Cellate(NewRow(), Width, "  " + LeagueRoster.Display(b.accountId)
                        + "  (" + b.score.ToString("N0") + " pts)", UiKit.Flat, UIHorizontalAlignment.Left);
                }
            }

            RenderHallOfGreatWorks();
        }

        private void RenderHallOfGreatWorks()
        {
            if (_projects == null || _projects.projects == null) return;
            int completed = 0;
            for (int i = 0; i < _projects.projects.Length; i++)
            {
                ProjectDto p = _projects.projects[i];
                if (p != null && p.status == "completed") completed++;
            }
            if (completed == 0) return;

            NewRow();
            UiKit.Cellate(NewRow(), Width, "Hall of Great Works:", UiKit.Head, UIHorizontalAlignment.Left);
            for (int i = 0; i < _projects.projects.Length; i++)
            {
                ProjectDto p = _projects.projects[i];
                if (p == null || p.status != "completed") continue;
                int builders = ProjectBuilderCount(p);
                string cityWord = builders == 1 ? "city" : "cities";
                string name = !string.IsNullOrEmpty(p.name) ? p.name : "Completed Great Work";
                UiKit.Cellate(NewRow(), Width, "  " + name + " — built by " + builders + " " + cityWord,
                    UiKit.Flat, UIHorizontalAlignment.Left);
            }
        }

        // One "label  have/need  [bar]" progress row.
        private void ProgressRow(string label, long have, long need)
        {
            UIPanel row = NewWideRow(UiKit.RowH);
            UiKit.Cellate(row, 150f, label, UiKit.Flat, UIHorizontalAlignment.Left);
            bool met = need <= 0 || have >= need;
            UiKit.Cellate(row, 120f, have.ToString("N0") + " / " + need.ToString("N0"),
                met ? UiKit.Up : UiKit.Flat, UIHorizontalAlignment.Right);
            int filled = need > 0 ? (int)System.Math.Min(have, need) : 0;
            int total = need > 0 ? (int)need : 1;
            // SegmentBar preserves the filled ratio when total exceeds its cap, so large requirements still render.
            UIPanel barHost = row.AddUIComponent<UIPanel>();
            barHost.size = new Vector2(BarW + 8f, UiKit.RowH);
            barHost.autoLayout = false;
            UiKit.SegmentBar(barHost, BarW, 8f, filled, total, met ? UiKit.Up : UiKit.Accent, UiKit.Dim)
                .relativePosition = new Vector3(8f, (UiKit.RowH - 8f) * 0.5f);
        }

        // ---- contribute actions ----

        private void SubmitGoods()
        {
            if (_sending) return;
            ProjectDto p = ActiveProject();
            if (p == null) { SetStatus("No active project."); return; }
            if (string.IsNullOrEmpty(_commodityKey)) { SetStatus("Nothing to contribute — no needed commodity in your depots."); return; }
            TransferManager.TransferReason reason;
            if (!Commodities.TryFromKey(_commodityKey, out reason)) { SetStatus("Unknown commodity."); return; }

            double qd;
            if (!TryParse(_qtyField, out qd) || qd < 1d) { SetStatus("Enter a quantity (≥ 1)."); return; }
            long want = (long)qd;
            // Cap at what's still needed AND what we have in [trade] depots.
            long stillNeeded = RemainingUnits(p, _commodityKey);
            long haveStock = InventoryService.StoredUnits(reason);
            long qty = want;
            if (qty > stillNeeded) qty = stillNeeded;
            if (qty > haveStock) qty = haveStock;
            if (qty <= 0) { SetStatus("No more " + DisplayNameOf(_commodityKey) + " needed, or none in your depots."); return; }

            string projectId = p.id;
            string commodity = _commodityKey;
            _sending = true;
            SyncForm();
            SetStatus("Removing " + qty.ToString("N0") + " " + DisplayNameOf(commodity) + " from your depots...");

            // Remove the stock on the SIM THREAD (InventoryService.MoveStock is sim-thread only), then POST the
            // actually-removed count from the MAIN thread (OmHttp + OmApi are main-thread). 1 unit = TransferUnitsPerUnit
            // transfer units (matches TradeDelivery's give-side removal).
            long tu = qty * InventoryService.TransferUnitsPerUnit;
            SimulationManager sm = SimulationManager.instance;
            if (sm == null) { _sending = false; SyncForm(); SetStatus("Simulation not ready — try again."); return; }
            sm.AddAction(delegate
            {
                long removedTU = -InventoryService.MoveStock(reason, -tu);   // signed; negative arg = REMOVE
                long removedUnits = removedTU / InventoryService.TransferUnitsPerUnit;
                OmHttp.OnMainThread(delegate { OnGoodsRemoved(projectId, commodity, reason, removedUnits); });
            });
        }

        // Main-thread continuation after the sim-thread depot removal: POST the removed count (if any), then refund
        // any units the server did NOT credit (it caps at the remaining requirement; a failed POST credits 0) so the
        // surplus stock returns to the depots instead of being lost.
        private void OnGoodsRemoved(string projectId, string commodity, TransferManager.TransferReason reason, long removedUnits)
        {
            if (removedUnits <= 0)
            {
                _sending = false;
                SyncForm();
                SetStatus("Couldn't remove " + DisplayNameOf(commodity) + " from your depots.");
                return;
            }
            OmApi.ContributeProjectGoods(projectId, commodity, removedUnits, delegate (bool ok, ProjectDto dto, string err)
            {
                _sending = false;
                // credited = units the server actually applied; on a failed POST nothing was applied → credited 0.
                long credited = (ok && dto != null) ? dto.credited : 0L;
                if (credited < 0L) credited = 0L;
                if (credited > removedUnits) credited = removedUnits;
                long refundUnits = removedUnits - credited;
                if (refundUnits > 0L) RefundGoods(reason, refundUnits);

                if (ok && dto != null) ReplaceProject(dto);
                if (ok)
                {
                    SetStatus(refundUnits > 0L
                        ? "Server accepted " + credited.ToString("N0") + " of " + removedUnits.ToString("N0")
                            + " " + DisplayNameOf(commodity) + "; the rest was returned to your depots."
                        : "Contributed " + credited.ToString("N0") + " " + DisplayNameOf(commodity) + ".");
                }
                else
                {
                    SetStatus("Server rejected it — returned " + refundUnits.ToString("N0") + " "
                        + DisplayNameOf(commodity) + " to your depots: "
                        + (string.IsNullOrEmpty(err) ? "check the server." : err));
                }
                SyncForm();
                Rebuild();
            });
        }

        // Refund un-credited units back to the [trade] depots on the SIM THREAD (InventoryService.MoveStock is
        // sim-thread only). 1 unit = TransferUnitsPerUnit transfer units (mirrors the removal in SubmitGoods).
        private void RefundGoods(TransferManager.TransferReason reason, long units)
        {
            if (units <= 0L) return;
            SimulationManager sm = SimulationManager.instance;
            if (sm == null) return;
            long tu = units * InventoryService.TransferUnitsPerUnit;
            sm.AddAction(delegate { InventoryService.MoveStock(reason, +tu); });   // positive arg = ADD back
        }

        private void SubmitGold()
        {
            if (_sending) return;
            ProjectDto p = ActiveProject();
            if (p == null) { SetStatus("No active project."); return; }
            if (p.goldReqCents <= 0) { SetStatus("This project needs no §."); return; }
            double amt;
            if (!TryParse(_goldField, out amt) || amt < 1d) { SetStatus("Enter a § amount."); return; }
            long cents = (long)(amt * 100.0);
            string projectId = p.id;
            _sending = true;
            SyncForm();
            SetStatus("Contributing §...");
            OmApi.ContributeProjectGold(projectId, cents, delegate (bool ok, ProjectDto dto, string err)
            {
                _sending = false;
                if (ok && dto != null) ReplaceProject(dto);
                SetStatus(ok
                    ? "Contributed § to the Great Work."
                    : "Couldn't contribute §: " + (string.IsNullOrEmpty(err) ? "check the server." : err));
                SyncForm();
                Rebuild();
            });
        }

        // ---- form state ----

        private void SyncForm()
        {
            EnsureCommodityValid();
            bool haveProject = ActiveProject() != null;
            bool haveCommodity = !string.IsNullOrEmpty(_commodityKey);
            if (_commodityBtn != null)
                _commodityBtn.text = "Give: " + (haveCommodity ? DisplayNameOf(_commodityKey) : "(nothing needed/in stock)");
            if (_contributeBtn != null) _contributeBtn.isEnabled = haveProject && haveCommodity && !_sending;
            ProjectDto p = ActiveProject();
            bool goldWanted = p != null && p.goldReqCents > 0 && p.gold < p.goldReqCents;
            if (_goldBtn != null) _goldBtn.isEnabled = haveProject && goldWanted && !_sending;
        }

        // The commodities the active project still needs AND we currently stock in [trade] depots (wire keys).
        private List<string> ContributableCommodities()
        {
            List<string> outv = new List<string>();
            ProjectDto p = ActiveProject();
            if (p == null || p.reqs == null) return outv;
            for (int i = 0; i < p.reqs.Length; i++)
            {
                ProjectReqDto r = p.reqs[i];
                if (r == null) continue;
                if (RemainingUnits(p, r.commodity) <= 0) continue;          // already met
                TransferManager.TransferReason reason;
                if (!Commodities.TryFromKey(r.commodity, out reason)) continue;
                if (InventoryService.StoredUnits(reason) <= 0) continue;    // we have none to give
                outv.Add(r.commodity);
            }
            return outv;
        }

        private void CycleCommodity()
        {
            List<string> opts = ContributableCommodities();
            if (opts.Count == 0) { _commodityKey = string.Empty; return; }
            int idx = opts.IndexOf(_commodityKey);
            _commodityKey = opts[(idx + 1) % opts.Count];
        }

        private void EnsureCommodityValid()
        {
            List<string> opts = ContributableCommodities();
            if (opts.Count == 0) { _commodityKey = string.Empty; return; }
            if (!opts.Contains(_commodityKey)) _commodityKey = opts[0];
        }

        // ---- data helpers ----

        private ProjectDto ActiveProject()
        {
            if (_projects == null || _projects.projects == null) return null;
            for (int i = 0; i < _projects.projects.Length; i++)
            {
                ProjectDto p = _projects.projects[i];
                if (p != null && p.status == "open") return p;
            }
            return null;
        }

        // Replace the cached active project with a server-returned updated copy (after a contribution).
        private void ReplaceProject(ProjectDto updated)
        {
            if (updated == null || _projects == null || _projects.projects == null) return;
            for (int i = 0; i < _projects.projects.Length; i++)
            {
                if (_projects.projects[i] != null && _projects.projects[i].id == updated.id)
                {
                    _projects.projects[i] = updated;
                    return;
                }
            }
        }

        private static long ContributedUnits(ProjectDto p, string commodity)
        {
            if (p.goods == null) return 0L;
            for (int i = 0; i < p.goods.Length; i++)
                if (p.goods[i] != null && p.goods[i].commodity == commodity) return p.goods[i].qty;
            return 0L;
        }

        private static long RequiredUnits(ProjectDto p, string commodity)
        {
            if (p.reqs == null) return 0L;
            for (int i = 0; i < p.reqs.Length; i++)
                if (p.reqs[i] != null && p.reqs[i].commodity == commodity) return p.reqs[i].qty;
            return 0L;
        }

        private static long RemainingUnits(ProjectDto p, string commodity)
        {
            long rem = RequiredUnits(p, commodity) - ContributedUnits(p, commodity);
            return rem < 0 ? 0L : rem;
        }

        private static string ProjectedRewardText(ProjectDto p)
        {
            if (p == null) return "No active project.";
            long mine = BuilderScore(p, Settings.AccountIdValue);
            if (mine <= 0L) return "No reward yet — contribute goods or § to become a builder.";
            long maxBy = MaxBuilderScore(p);
            long scaled = ScaledBuffCents(p.buffMagnitudeCents, mine, maxBy);
            int demand, attract;
            InvestBuffMagnitude(scaled, out demand, out attract);
            string text = "+" + demand + " " + CoopBuff.KindLabel(p.buffKind) + " demand, +" + attract.ToString("N0")
                + " attractiveness for " + ClampBuffDays(p.buffDays) + "d if completed now"
                + " (your score " + mine.ToString("N0") + " / top " + maxBy.ToString("N0") + ")";
            string trade = ProjectTradeRewardText(p);
            return string.IsNullOrEmpty(trade) ? text : text + "; " + trade;
        }

        private static string RewardOnCompletionText(ProjectDto p)
        {
            int demand, attract;
            InvestBuffMagnitude(p != null ? p.buffMagnitudeCents : 0L, out demand, out attract);
            string text = "+" + demand + " " + CoopBuff.KindLabel(p != null ? p.buffKind : null)
                + " demand & +" + attract.ToString("N0") + " attractiveness for " + ClampBuffDays(p != null ? p.buffDays : 0) + "d";
            string trade = ProjectTradeRewardText(p);
            return string.IsNullOrEmpty(trade) ? text : text + ", and " + trade + " for builders";
        }

        private static string ProjectTradeRewardText(ProjectDto p)
        {
            if (p == null || string.IsNullOrEmpty(p.tradeRewardKind) || string.IsNullOrEmpty(p.tradeRewardCommodity)
                || p.tradeRewardPctBips <= 0) return string.Empty;
            string c = DisplayNameOf(p.tradeRewardCommodity);
            if (p.tradeRewardKind == "marketShield")
                return c + " market impact reduced by " + PctFromBips(p.tradeRewardPctBips);
            if (p.tradeRewardKind == "priceEdge")
                return "+" + PctFromBips(p.tradeRewardPctBips) + " " + c + " export price";
            return string.Empty;
        }

        private static long BuilderScore(ProjectDto p, string accountId)
        {
            if (p == null || p.by == null || string.IsNullOrEmpty(accountId)) return 0L;
            for (int i = 0; i < p.by.Length; i++)
                if (p.by[i] != null && p.by[i].accountId == accountId) return p.by[i].score;
            return 0L;
        }

        private static long MaxBuilderScore(ProjectDto p)
        {
            long maxBy = 0L;
            if (p == null || p.by == null) return maxBy;
            for (int i = 0; i < p.by.Length; i++)
                if (p.by[i] != null && p.by[i].score > maxBy) maxBy = p.by[i].score;
            return maxBy;
        }

        private static int ProjectBuilderCount(ProjectDto p)
        {
            int n = 0;
            if (p == null || p.by == null) return n;
            for (int i = 0; i < p.by.Length; i++)
                if (p.by[i] != null && p.by[i].score > 0L) n++;
            return n;
        }

        // Mirrors server/internal/store.ProjectScaledBuffMagnitude exactly: scale before InvestBuffMagnitude.
        private static long ScaledBuffCents(long advertisedCents, long mine, long maxBy)
        {
            if (advertisedCents <= 0L || mine <= 0L) return 0L;
            if (maxBy <= 0L || mine >= maxBy) return advertisedCents;
            long scaleNum = ProjectRewardFloorNum * maxBy + (ProjectRewardFloorDen - ProjectRewardFloorNum) * mine;
            long scaleDen = ProjectRewardFloorDen * maxBy;
            return advertisedCents * scaleNum / scaleDen;
        }

        private static void InvestBuffMagnitude(long cents, out int demand, out int attract)
        {
            demand = (int)(cents / CentsPerDemandPoint);
            if (demand < 1) demand = 1;
            else if (demand > DemandBoostCap) demand = DemandBoostCap;
            attract = (int)(cents / CentsPerAttractPoint);
            if (attract < 1) attract = 1;
            else if (attract > AttractRateCap) attract = AttractRateCap;
        }

        private static int ClampBuffDays(int days)
        {
            if (days < 1) return 1;
            if (days > 14) return 14;
            return days;
        }

        private static string PctFromBips(int bips)
        {
            if (bips < 0) bips = -bips;
            int whole = bips / 100;
            int frac = bips % 100;
            if (frac == 0) return whole + "%";
            if (frac % 10 == 0) return whole + "." + (frac / 10) + "%";
            return whole + "." + (frac < 10 ? "0" : "") + frac + "%";
        }

        private static string DisplayNameOf(string wireKey)
        {
            TransferManager.TransferReason reason;
            if (Commodities.TryFromKey(wireKey, out reason)) return Commodities.DisplayName(reason);
            return wireKey;
        }

        private void SetStatus(string text) { if (_statusLabel != null) _statusLabel.text = text; }

        // ---- ui builders (mirror MembersTab) ----

        private UIPanel NewRow()
        {
            UIPanel row = _grid.AddUIComponent<UIPanel>();
            row.size = new Vector2(Width, UiKit.RowH);
            row.autoLayout = true;
            row.autoLayoutDirection = LayoutDirection.Horizontal;
            return row;
        }

        private UIPanel NewWideRow(float h)
        {
            UIPanel row = _grid.AddUIComponent<UIPanel>();
            row.size = new Vector2(Width, h);
            row.autoLayout = true;
            row.autoLayoutDirection = LayoutDirection.Horizontal;
            return row;
        }

        private UIButton MakeButton(float x, float y, float w, string text)
        {
            UIButton b = _root.AddUIComponent<UIButton>();
            b.text = text;
            b.textScale = 0.7f;
            b.normalBgSprite = "ButtonMenu";
            b.hoveredBgSprite = "ButtonMenuHovered";
            b.pressedBgSprite = "ButtonMenuPressed";
            b.disabledBgSprite = "ButtonMenuDisabled";
            b.size = new Vector2(w, 26f);
            b.relativePosition = new Vector3(x, y);
            return b;
        }

        private UITextField Field(float x, float y, float w)
        {
            UITextField tf = _root.AddUIComponent<UITextField>();
            tf.relativePosition = new Vector3(x, y);
            tf.size = new Vector2(w, 26f);
            tf.builtinKeyNavigation = true;
            tf.isInteractive = true;
            tf.readOnly = false;
            tf.canFocus = true;
            tf.numericalOnly = true;
            tf.allowFloats = true;
            tf.padding = new RectOffset(6, 6, 5, 0);
            UIView view = UIView.GetAView();
            if (view != null) tf.atlas = view.defaultAtlas;
            tf.normalBgSprite = "TextFieldPanel";
            tf.hoveredBgSprite = "TextFieldPanelHovered";
            tf.focusedBgSprite = "TextFieldPanel";
            tf.selectionSprite = "EmptySprite";
            tf.textScale = 0.8f;
            tf.textColor = UiKit.Flat;
            tf.color = Color.white;
            tf.horizontalAlignment = UIHorizontalAlignment.Left;
            return tf;
        }

        private void Caption(string text, float x, float y)
        {
            UILabel l = _root.AddUIComponent<UILabel>();
            l.autoSize = true;
            l.relativePosition = new Vector3(x, y + 4f);
            l.textScale = 0.78f;
            l.textColor = UiKit.Head;
            l.text = text;
        }

        private UILabel SmallLabel(float x, float y, Color32 color)
        {
            UILabel l = _root.AddUIComponent<UILabel>();
            l.autoSize = true;
            l.relativePosition = new Vector3(x, y);
            l.textScale = 0.74f;
            l.textColor = color;
            l.text = " ";
            return l;
        }

        private static bool TryParse(UITextField field, out double value)
        {
            value = 0d;
            if (field == null) return false;
            return double.TryParse(field.text, NumberStyles.Float, CultureInfo.InvariantCulture, out value);
        }
    }
}
