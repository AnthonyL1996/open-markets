using System.Collections.Generic;
using System.Globalization;
using ColossalFramework.UI;
using OpenMarkets.Data;
using OpenMarkets.Market;
using OpenMarkets.Net;
using OpenMarkets.Trade;
using UnityEngine;

namespace OpenMarkets.UI.Terminal
{
    /// <summary>
    /// Trade tab: compose a Civ5-style two-sided BASKET and offer it to a leaguemate (M5). The player picks a
    /// counterparty, adds commodity and/or gold lines to "You give" / "They give", sees live INDICATIVE totals
    /// and the net balance, sets installments + the negotiated default rate, then POSTs via
    /// <see cref="OmApi.CreateTrade"/>. The counterparty accepts/declines it from their Inbox (next increment).
    ///
    /// The control strip (counterparty/commodity cycles, qty/gold fields, installments/rate) is BUILT ONCE and
    /// mutated in place (rebuilding would drop text-field focus). The two basket PANES are rebuilt from the
    /// in-memory line lists on every add/remove (no text fields live in them, so a rebuild is safe). MAIN THREAD.
    /// Values shown are indicative (neutral index); the server freezes the authoritative values at accept.
    /// </summary>
    internal sealed class TradeTab : ITabBody
    {
        private const float PaneW = 286f;
        private const float RightX = 298f;
        private const float PaneTop = 150f;
        private const float PaneH = 150f;

        // A composed basket line (pre-offer, client-side).
        private sealed class Line
        {
            public bool IsGold;
            public TransferManager.TransferReason Reason;
            public string Display;
            public long QtyFixed;   // commodity: qty × Money.QtyScale
            public long GoldCents;  // gold lines
        }

        // Negotiable default-rate choices (bps); first is the harsh league floor (20%).
        private static readonly int[] RateBps = { 2000, 2500, 3000, 4000, 5000 };

        private UIPanel _root;
        private UIButton _counterpartyBtn;
        private UIButton _commodityBtn;
        private UITextField _qtyField;
        private UITextField _goldField;
        private UIButton _installmentsBtn;
        private UIButton _rateBtn;
        private UIScrollablePanel _giveGrid;
        private UIScrollablePanel _takeGrid;
        private UILabel _giveTotal;
        private UILabel _takeTotal;
        private UILabel _balance;
        private UIButton _sendBtn;
        private UIButton _repeatBtn;
        private UILabel _status;
        private UILabel _available;        // "Available: N" for the selected give-commodity (M6 Phase 1)
        private TradeDto[] _trades;        // active trades, for the reserved-stock calc (fetched on Refresh)

        private readonly List<Line> _give = new List<Line>();
        private readonly List<Line> _take = new List<Line>();
        // Last SUCCESSFULLY-sent basket, for "Repeat last". In-memory static (survives tab rebuilds, NOT level
        // unload — cleared by Forget() on unload). Stored as line copies, side-tagged, so Repeat can rehydrate.
        private static readonly List<Line> _lastGive = new List<Line>();
        private static readonly List<Line> _lastTake = new List<Line>();
        private static bool _hasLast;
        private string _counterpartyId = string.Empty;
        private int _commodityIdx;
        private int _installments = 1;
        private int _rateIdx;
        private bool _sending;

        public string TabLabel { get { return "Trade"; } }
        public string Title { get { return "Open Markets — new trade"; } }

        public void Build(UIComponent host, Vector2 size)
        {
            _root = host.AddUIComponent<UIPanel>();
            _root.relativePosition = Vector3.zero;
            _root.size = size;
            _root.autoLayout = false;

            Caption("To", 2f, 6f);
            _counterpartyBtn = MakeButton(40f, 4f, 260f, "—");
            _counterpartyBtn.eventClicked += delegate { CycleCounterparty(); SyncLabels(); };

            // Commodity line entry: [commodity][qty][+ give][+ take].
            _commodityBtn = MakeButton(2f, 36f, 150f, "—");
            _commodityBtn.eventClicked += delegate { Cycle(ref _commodityIdx, Commodities.All.Count); SyncLabels(); };
            _qtyField = Field(158f, 36f, 70f);
            _qtyField.tooltip = "Quantity in TRUCKS (1 truck = " + Commodities.UnitsPerTruck.ToString("N0") + " units). Fractions OK, e.g. 0.5.";
            MakeButton(232f, 36f, 74f, "+ you give").eventClicked += delegate { AddCommodity(true); };
            MakeButton(312f, 36f, 74f, "+ they give").eventClicked += delegate { AddCommodity(false); };
            // Tradeable stock you can still give of the selected commodity (depot stock − reserved − in-basket).
            _available = SmallLabel(392f, 40f, UiKit.Dim);

            // Gold line entry: [§ amount][+ give][+ take].
            Caption("§", 2f, 70f);
            _goldField = Field(20f, 68f, 90f);
            MakeButton(116f, 68f, 90f, "+ you give §").eventClicked += delegate { AddGold(true); };
            MakeButton(212f, 68f, 90f, "+ they give §").eventClicked += delegate { AddGold(false); };

            // Terms: installments + negotiated default rate.
            Caption("Installments", 2f, 102f);
            _installmentsBtn = MakeButton(96f, 100f, 60f, "x 1");
            _installmentsBtn.eventClicked += delegate { _installments = _installments >= 12 ? 1 : _installments + 1; SyncLabels(); };
            Caption("Default rate", 170f, 102f);
            _rateBtn = MakeButton(258f, 100f, 70f, "—");
            _rateBtn.eventClicked += delegate { Cycle(ref _rateIdx, RateBps.Length); SyncLabels(); };

            // Pane headers + the two scrollable line lists.
            HeaderLabel("YOU GIVE", 2f, PaneTop - 20f);
            HeaderLabel("THEY GIVE", RightX, PaneTop - 20f);
            _giveGrid = MakeGrid(2f);
            _takeGrid = MakeGrid(RightX);

            _giveTotal = SmallLabel(2f, PaneTop + PaneH + 4f, UiKit.Flat);
            _takeTotal = SmallLabel(RightX, PaneTop + PaneH + 4f, UiKit.Flat);
            _balance = SmallLabel(2f, PaneTop + PaneH + 22f, UiKit.Head);
            _balance.textScale = 0.84f; // the headline number — let it read louder than the per-side totals

            // Basket utilities (left of the send CTA): empty the composer, or reload the last sent basket.
            MakeButton(2f, PaneTop + PaneH + 22f, 100f, "Clear basket").eventClicked += delegate { ClearBasket(); };
            _repeatBtn = MakeButton(106f, PaneTop + PaneH + 22f, 110f, "Repeat last");
            _repeatBtn.eventClicked += delegate { RepeatLast(); };

            _sendBtn = MakeButton(RightX, PaneTop + PaneH + 22f, 150f, "Send trade");
            _sendBtn.eventClicked += delegate { Send(); };
            UiKit.Primary(_sendBtn); // the call-to-action

            _status = SmallLabel(2f, PaneTop + PaneH + 46f, UiKit.Dim);
            SyncRepeatEnabled();

            // Faint rules group the surface: [recipient] · [build basket + terms] · [the two panes] · [summary].
            UiKit.Divider(_root, 2f, 32f, 580f);
            UiKit.Divider(_root, 2f, PaneTop - 22f, 580f);
            UiKit.Divider(_root, 2f, PaneTop + PaneH + 1f, 580f);

            EnsureCounterpartyValid();
            RebuildPanes();
            SyncLabels();
        }

        public void SetVisible(bool on) { if (_root != null) _root.isVisible = on; }

        public void Refresh()
        {
            if (_root == null) return;
            EnsureCounterpartyValid();
            // Pull active trades so "Available" can subtract stock already committed on other deals (reserved).
            if (Settings.IsOnlineConfigured)
                OmApi.GetTrades(delegate (bool ok, TradeListDto list)
                {
                    if (ok && list != null) _trades = list.trades;
                    SyncLabels();
                });
            SyncLabels();
        }

        // ---- line add / remove ----

        private void AddCommodity(bool give)
        {
            Commodity c = CurrentCommodity();
            if (c == null) { SetStatus("Pick a commodity."); return; }
            double trucks;
            if (!TryParse(_qtyField, out trucks) || trucks <= 0) { SetStatus("Enter a number of trucks greater than 0."); return; }
            long units = (long)(trucks * Commodities.UnitsPerTruck + 0.5); // qty is entered in TRUCKS (1 truck = 8000 units)
            // Inventory-backed (give side): you can only promise commodities you actually hold in [trade] depots.
            if (give)
            {
                long avail = AvailableUnits(c);
                if (units > avail)
                {
                    SetStatus("Only " + Trucks(avail) + " trucks of " + c.DisplayName
                        + " in your [trade] depots — tag/stock a warehouse to give more.");
                    return;
                }
            }
            Line line = new Line
            {
                IsGold = false,
                Reason = c.Reason,
                Display = c.DisplayName,
                QtyFixed = units * Money.QtyScale
            };
            (give ? _give : _take).Add(line);
            SetStatus(string.Empty);
            RebuildPanes();
        }

        private void AddGold(bool give)
        {
            double amount;
            if (!TryParse(_goldField, out amount) || amount <= 0) { SetStatus("Enter a § amount greater than 0."); return; }
            Line line = new Line { IsGold = true, Display = "§", GoldCents = (long)(amount * 100.0 + 0.5) };
            (give ? _give : _take).Add(line);
            SetStatus(string.Empty);
            RebuildPanes();
        }

        private void Remove(List<Line> side, Line line)
        {
            side.Remove(line);
            RebuildPanes();
        }

        // ---- panes ----

        private void RebuildPanes()
        {
            FillPane(_giveGrid, _give);
            FillPane(_takeGrid, _take);
            SyncTotals();
            SyncLabels();   // keep "Available" in step with the basket (give lines consume headroom)
        }

        private void FillPane(UIScrollablePanel grid, List<Line> lines)
        {
            if (grid == null) return;
            List<UIComponent> old = new List<UIComponent>(grid.components);
            for (int i = 0; i < old.Count; i++)
            {
                grid.RemoveUIComponent(old[i]);
                Object.Destroy(old[i].gameObject);
            }
            if (lines.Count == 0)
            {
                UIPanel empty = NewRow(grid);
                UiKit.Cellate(empty, PaneW, "(nothing yet)", UiKit.Dim, UIHorizontalAlignment.Left);
                return;
            }
            for (int i = 0; i < lines.Count; i++)
            {
                Line line = lines[i];
                List<Line> side = lines; // captured for the remove handler
                UIPanel row = NewRow(grid);
                UiKit.Cellate(row, PaneW - 26f, LineLabel(line), UiKit.Flat, UIHorizontalAlignment.Left);
                UIButton x = row.AddUIComponent<UIButton>();
                x.text = "x";
                x.textScale = 0.7f;
                x.normalBgSprite = "ButtonMenu";
                x.hoveredBgSprite = "ButtonMenuHovered";
                x.size = new Vector2(22f, UiKit.RowH - 2f);
                Line captured = line;
                x.eventClicked += delegate { Remove(side, captured); };
            }
        }

        private string LineLabel(Line line)
        {
            if (line.IsGold) return "§ " + Cash(line.GoldCents) + "  (=" + Cash(LineValue(line)) + ")";
            long units = line.QtyFixed / Money.QtyScale;
            return line.Display + " × " + Trucks(units) + " trk  (~§" + Cash(LineValue(line)) + ")";
        }

        // Whole-or-fractional truck count for a unit amount (1 truck = Commodities.UnitsPerTruck units).
        private static string Trucks(long units)
        {
            return (units / (double)Commodities.UnitsPerTruck).ToString("0.##", CultureInfo.InvariantCulture);
        }

        // ---- totals ----

        private void SyncTotals()
        {
            long giveSum = SumValue(_give);
            long takeSum = SumValue(_take);
            long net = NetToMe();
            if (_giveTotal != null) _giveTotal.text = "You give: §" + Cash(giveSum);
            if (_takeTotal != null) _takeTotal.text = "They give: §" + Cash(takeSum);
            if (_balance != null)
            {
                _balance.text = "Deal value: §" + Cash(giveSum + takeSum) + "  ·  net to you: "
                    + (net >= 0 ? "+§" : "-§") + Cash(net < 0 ? -net : net) + "  (indicative)";
                _balance.textColor = net >= 0 ? UiKit.Up : UiKit.Down;
            }
        }

        private long SumValue(List<Line> lines)
        {
            long sum = 0;
            for (int i = 0; i < lines.Count; i++) sum += LineValue(lines[i]);
            return sum;
        }

        // Net indicative cents to me (offerer): commodity give +, take -; gold give -, take +.
        private long NetToMe()
        {
            long net = 0;
            for (int i = 0; i < _give.Count; i++) net += BasketValuation.FlowToOfferer(_give[i].IsGold, true, LineValue(_give[i]));
            for (int i = 0; i < _take.Count; i++) net += BasketValuation.FlowToOfferer(_take[i].IsGold, false, LineValue(_take[i]));
            return net;
        }

        private static long LineValue(Line line)
        {
            if (line.IsGold) return line.GoldCents;
            // Index-aware (live league price), matching the Market board + Inbox. Still indicative — the server
            // freezes the authoritative value at accept.
            return BasketValuation.IndicativeCommodityCents(line.QtyFixed, MarketFeed.Instance.GetPrice(line.Reason));
        }

        // ---- send ----

        private void Send()
        {
            if (_sending) return;
            EnsureCounterpartyValid();
            if (string.IsNullOrEmpty(_counterpartyId)) { SetStatus("Pick a counterparty (no other league members yet)."); return; }
            if (_give.Count == 0 || _take.Count == 0) { SetStatus("A trade needs at least one line on each side."); return; }

            List<LineItemDto> items = new List<LineItemDto>(_give.Count + _take.Count);
            AppendItems(items, _give, "give");
            AppendItems(items, _take, "take");

            TradeOfferDto offer = new TradeOfferDto
            {
                leagueId = Settings.LeagueIdValue,
                counterparty = _counterpartyId,
                defaultRateBps = RateBps[_rateIdx],
                installments = _installments,
                items = items.ToArray()
            };

            string toName = LeagueRoster.Display(_counterpartyId);
            SetSending(true);
            SetStatus("Sending trade...");
            OmApi.CreateTrade(offer, delegate (bool ok, TradeDto created, string error)
            {
                SetSending(false);
                if (ok && created != null)
                {
                    RememberLast();   // snapshot this basket BEFORE clearing, so "Repeat last" can reload it
                    _give.Clear();
                    _take.Clear();
                    RebuildPanes();
                    SetStatus("Trade sent to " + toName + ".");
                }
                else SetStatus("Trade rejected: " + (string.IsNullOrEmpty(error) ? "check the terms and the server." : error));
            });
        }

        private static void AppendItems(List<LineItemDto> items, List<Line> lines, string dir)
        {
            for (int i = 0; i < lines.Count; i++)
            {
                Line line = lines[i];
                items.Add(new LineItemDto
                {
                    kind = line.IsGold ? "gold" : "commodity",
                    commodity = line.IsGold ? string.Empty : Commodities.Key(line.Reason),
                    qtyFixed = line.IsGold ? 0 : line.QtyFixed,
                    goldCents = line.IsGold ? line.GoldCents : 0,
                    dir = dir
                });
            }
        }

        // ---- clear / repeat ----

        // Empty both line lists and redraw the panes.
        private void ClearBasket()
        {
            _give.Clear();
            _take.Clear();
            RebuildPanes();
            SetStatus("Basket cleared.");
        }

        // Snapshot the just-sent basket (deep copies of the lines) so "Repeat last" can rehydrate it later.
        private void RememberLast()
        {
            _lastGive.Clear();
            _lastTake.Clear();
            for (int i = 0; i < _give.Count; i++) _lastGive.Add(Copy(_give[i]));
            for (int i = 0; i < _take.Count; i++) _lastTake.Add(Copy(_take[i]));
            _hasLast = true;
            SyncRepeatEnabled();
        }

        // Reload the last sent basket into the composer. Commodity lines whose commodity is no longer tradable
        // (e.g. a DLC table change) are skipped; gold lines always carry over. Replaces the current composer.
        private void RepeatLast()
        {
            if (!_hasLast) { SetStatus("No previous trade to repeat yet."); return; }
            _give.Clear();
            _take.Clear();
            int skipped = CopyInto(_lastGive, _give) + CopyInto(_lastTake, _take);
            RebuildPanes();
            SetStatus(skipped > 0
                ? "Reloaded last basket (" + skipped + " line(s) skipped — no longer tradable)."
                : "Reloaded last basket.");
        }

        // Copy valid lines from src into dst; returns the count of commodity lines skipped as no-longer-tradable.
        private static int CopyInto(List<Line> src, List<Line> dst)
        {
            int skipped = 0;
            for (int i = 0; i < src.Count; i++)
            {
                Line l = src[i];
                if (!l.IsGold && !Commodities.IsTradable(l.Reason)) { skipped++; continue; }
                dst.Add(Copy(l));
            }
            return skipped;
        }

        private static Line Copy(Line l)
        {
            return new Line { IsGold = l.IsGold, Reason = l.Reason, Display = l.Display, QtyFixed = l.QtyFixed, GoldCents = l.GoldCents };
        }

        private void SyncRepeatEnabled()
        {
            if (_repeatBtn != null) _repeatBtn.isEnabled = _hasLast;
        }

        /// <summary>Drop the remembered last basket (level unload). MAIN THREAD.</summary>
        public static void Forget()
        {
            _lastGive.Clear();
            _lastTake.Clear();
            _hasLast = false;
        }

        private void SetSending(bool on)
        {
            _sending = on;
            if (_sendBtn != null)
            {
                _sendBtn.isEnabled = !on;
                _sendBtn.text = on ? "Sending..." : "Send trade";
            }
        }

        // ---- state helpers ----

        private static void Cycle(ref int idx, int count)
        {
            if (count <= 0) { idx = 0; return; }
            idx = (idx + 1) % count;
        }

        private void CycleCounterparty()
        {
            List<string> others = OtherMembers();
            if (others.Count == 0) { _counterpartyId = string.Empty; return; }
            int idx = others.IndexOf(_counterpartyId);
            _counterpartyId = others[(idx + 1) % others.Count];
        }

        private void EnsureCounterpartyValid()
        {
            List<string> others = OtherMembers();
            if (others.Count == 0) { _counterpartyId = string.Empty; return; }
            if (!others.Contains(_counterpartyId)) _counterpartyId = others[0];
        }

        private static List<string> OtherMembers()
        {
            List<string> ids = LeagueRoster.MemberIds();
            ids.Remove(Settings.AccountIdValue);
            return ids;
        }

        private Commodity CurrentCommodity()
        {
            IList<Commodity> all = Commodities.All;
            if (all.Count == 0) return null;
            if (_commodityIdx < 0 || _commodityIdx >= all.Count) _commodityIdx = 0;
            return all[_commodityIdx];
        }

        private void SyncLabels()
        {
            if (_counterpartyBtn != null)
                _counterpartyBtn.text = string.IsNullOrEmpty(_counterpartyId)
                    ? "(no other members)" : LeagueRoster.Display(_counterpartyId);
            Commodity c = CurrentCommodity();
            if (_commodityBtn != null) _commodityBtn.text = c != null ? c.DisplayName : "—";
            if (_installmentsBtn != null) _installmentsBtn.text = "x " + _installments;
            if (_rateBtn != null) _rateBtn.text = (RateBps[_rateIdx] / 100) + "%";
            if (_available != null)
                _available.text = c != null
                    ? ("§" + (MarketFeed.Instance.PricePerTruckCents(c.Reason) / 100).ToString("N0")
                        + "/truck · Available: " + Trucks(AvailableUnits(c)) + " trk")
                    : string.Empty;
        }

        // Whole units of commodity c you can still put on the GIVE side: depot stock − reserved by active trades
        // − what's already in this basket's give pane. Floors at 0. (M6 Phase 1 — see INVENTORY-TRADE.md.)
        private long AvailableUnits(Commodity c)
        {
            if (c == null) return 0L;
            long stored = InventoryService.StoredUnits(c.Reason);
            long reserved = InventoryReservations.ReservedUnits(_trades, Settings.AccountIdValue, c.Reason);
            long inBasket = 0L;
            for (int i = 0; i < _give.Count; i++)
                if (!_give[i].IsGold && _give[i].Reason == c.Reason) inBasket += _give[i].QtyFixed / Money.QtyScale;
            long avail = stored - reserved - inBasket;
            return avail < 0L ? 0L : avail;
        }

        // ---- ui builders ----

        private UIScrollablePanel MakeGrid(float x)
        {
            UIScrollablePanel grid = _root.AddUIComponent<UIScrollablePanel>();
            grid.relativePosition = new Vector3(x, PaneTop);
            grid.size = new Vector2(PaneW, PaneH);
            grid.autoLayout = true;
            grid.autoLayoutDirection = LayoutDirection.Vertical;
            grid.autoLayoutPadding = new RectOffset(0, 0, 0, 2);
            grid.clipChildren = true;
            grid.scrollWheelDirection = UIOrientation.Vertical;
            return grid;
        }

        private UIPanel NewRow(UIScrollablePanel grid)
        {
            UIPanel row = grid.AddUIComponent<UIPanel>();
            row.size = new Vector2(PaneW, UiKit.RowH);
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
            l.autoSize = true;   // a size-less, non-autosize label renders zero-width (invisible)
            l.relativePosition = new Vector3(x, y);
            l.textScale = 0.78f;
            l.textColor = UiKit.Head;
            l.text = text;
        }

        private void HeaderLabel(string text, float x, float y)
        {
            UILabel l = _root.AddUIComponent<UILabel>();
            l.autoSize = true;
            l.relativePosition = new Vector3(x, y);
            l.textScale = 0.8f;
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
            l.text = " ";        // start non-empty so autoSize gives it a box; SetStatus overwrites
            return l;
        }

        private static bool TryParse(UITextField field, out double value)
        {
            value = 0d;
            if (field == null) return false;
            return double.TryParse(field.text, NumberStyles.Float, CultureInfo.InvariantCulture, out value);
        }

        private static string Cash(long cents)
        {
            return (cents / 100).ToString("N0", CultureInfo.InvariantCulture);
        }

        private void SetStatus(string text) { if (_status != null) _status.text = text; }
    }
}
