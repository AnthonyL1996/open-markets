using System.Collections.Generic;
using System.Globalization;
using ColossalFramework.UI;
using OpenMarkets.Data;
using OpenMarkets.Market;
using OpenMarkets.Net;
using UnityEngine;

namespace OpenMarkets.UI.Terminal
{
    /// <summary>
    /// Market dashboard (M9): a per-COMMODITY price board — one row per commodity showing the league index, the
    /// active price-event swing, a BUY/SELL hint, and a trend sparkline. The price is one number per commodity now
    /// (no per-partner columns), read from <see cref="MarketFeed"/> (the server feed when online; static base prices
    /// — index 1.00 — solo). MAIN THREAD. Read-only: trading happens in the Trade tab, not the board. Click a
    /// commodity row to expand a drawn chart of its index history.
    /// </summary>
    internal sealed class MarketTab : ITabBody
    {
        private const float NameCol = 150f;
        private const float PriceCol = 86f;   // §/truck — the headline price (replaces the raw index multiplier)
        private const float EventCol = 64f;
        private const float TrendCol = 96f;
        private const float Width = NameCol + PriceCol + EventCol + TrendCol;

        // Sparkline glyphs U+2581..U+2588 (▁▂▃▄▅▆▇█), low→high. SWAPPABLE — if CS1's UI font renders these as boxes,
        // replace with an ASCII fallback (e.g. ".:-=+*#@"); Spark() needs no other change.
        private const string SparkGlyphs = "▁▂▃▄▅▆▇█";

        // Height of the click-to-expand index-history chart panel inserted under the selected commodity's row
        // (chart + the price-alert strip below it).
        private const float ChartH = 116f;
        private const float AlertStripH = 30f;
        private const float PanelH = ChartH + AlertStripH;

        // The per-commodity price-alert input lives in the expanded panel; keep a handle so Set/Clear can read it.
        private UITextField _alertField;
        private UILabel _alertStatus;

        private UIScrollablePanel _grid;
        // Click-to-expand: which commodity's full index chart is open (if any). Persists across the periodic
        // refreshes so the chart stays open and updates live as new history samples arrive.
        private TransferManager.TransferReason _selected;
        private bool _hasSelected;

        public string TabLabel { get { return "Market"; } }

        public string Title
        {
            get { return "Open Markets — lifetime net §" + (PricingService.LifetimeNetCents / 100).ToString("N0"); }
        }

        public void Build(UIComponent host, Vector2 size)
        {
            _grid = host.AddUIComponent<UIScrollablePanel>();
            _grid.relativePosition = Vector3.zero;
            _grid.size = size;
            _grid.autoLayout = true;
            _grid.autoLayoutDirection = LayoutDirection.Vertical;
            _grid.autoLayoutPadding = new RectOffset(0, 0, 0, 2);
            _grid.clipChildren = true;
            _grid.scrollWheelDirection = UIOrientation.Vertical;
        }

        public void SetVisible(bool on) { if (_grid != null) _grid.isVisible = on; }

        public void Refresh()
        {
            if (_grid == null) return;

            // Snapshot before removal: RemoveUIComponent mutates the CF child list, and Destroy is deferred.
            List<UIComponent> old = new List<UIComponent>(_grid.components);
            for (int i = 0; i < old.Count; i++)
            {
                _grid.RemoveUIComponent(old[i]);
                Object.Destroy(old[i].gameObject);
            }

            MarketFeed feed = MarketFeed.Instance;

            // Shared league crises (social slice 3): one banner row per active crisis, ABOVE the commodity grid.
            // Tinted by the swing sign; ASCII-safe text. No rows when none active.
            CrisisDto[] crises = feed.Crises;
            if (crises != null)
            {
                for (int i = 0; i < crises.Length; i++)
                {
                    CrisisDto cr = crises[i];
                    if (cr == null || string.IsNullOrEmpty(cr.name)) continue;
                    TransferManager.TransferReason reason;
                    string com = Commodities.TryFromKey(cr.commodity, out reason)
                        ? Commodities.DisplayName(reason) : cr.commodity;
                    string sign = cr.eventPct > 0 ? "+" : "";
                    string days = cr.ticksLeft == 1 ? "1 day" : (cr.ticksLeft + " days");
                    string text = "(!) " + cr.name + ": " + com + " " + sign + cr.eventPct + "% - " + days + " left";

                    UIPanel banner = NewRow();
                    UILabel cell = UiKit.BannerCell(banner, Width, text);
                    // Tint the text by the swing direction (the banner background is the standard alert red).
                    cell.textColor = cr.eventPct > 0 ? UiKit.Up : (cr.eventPct < 0 ? UiKit.Down : UiKit.BannerText);
                    cell.tooltip = !string.IsNullOrEmpty(cr.narrative) ? cr.narrative : cr.name;
                }
            }

            UIPanel header = NewRow();
            UiKit.Cellate(header, NameCol, "Commodity", UiKit.Head, UIHorizontalAlignment.Left);
            UiKit.Cellate(header, PriceCol, "§/truck", UiKit.Head, UIHorizontalAlignment.Right);
            UiKit.Cellate(header, EventCol, "Event", UiKit.Head, UIHorizontalAlignment.Right);
            UiKit.Cellate(header, TrendCol, "Trend", UiKit.Head, UIHorizontalAlignment.Left);

            IList<Commodity> commodities = Commodities.All;
            for (int c = 0; c < commodities.Count; c++)
            {
                Commodity com = commodities[c];
                float index = feed.IndexOf(com.Reason);
                int eventPct = feed.EventPct(com.Reason);
                int[] hist = feed.History(com.Reason);

                UIPanel row = NewRow();
                // The whole row toggles the expand chart. Reason is copied to a loop-local so the click closure
                // can't capture a mutated loop variable. The row is interactive; clicks on its (non-handling) label
                // cells bubble up to this handler, so the entire row is the hit target.
                TransferManager.TransferReason reason = com.Reason;
                row.isInteractive = true;
                row.eventClick += delegate { Toggle(reason); };

                UILabel nameCell = UiKit.Cellate(row, NameCol, com.DisplayName, UiKit.Flat, UIHorizontalAlignment.Left);
                bool open = _hasSelected && _selected == com.Reason;
                nameCell.text = (open ? "v " : "> ") + com.DisplayName;   // ASCII caret (font-safe), not a glyph
                nameCell.tooltip = "Click for the index-history chart";

                // Price per full truck (the tangible figure). Colour vs base: dearer (index>1) green, cheaper red.
                long truckCents = feed.PricePerTruckCents(com.Reason);
                Color32 priceCol = index > 1.0f ? UiKit.Up : (index < 1.0f ? UiKit.Down : UiKit.Flat);
                UILabel priceCell = UiKit.Cellate(row, PriceCol, "§" + (truckCents / 100).ToString("N0"),
                    priceCol, UIHorizontalAlignment.Right);
                priceCell.tooltip = com.DisplayName + ": §" + (truckCents / 100).ToString("N0") + " per truck ("
                    + Commodities.UnitsPerTruck.ToString("N0") + " units) — " + index.ToString("0.00") + "× base.";

                // Active price-event swing %.
                string evText = eventPct != 0 ? ((eventPct > 0 ? "+" : "") + eventPct + "%") : "—";
                Color32 evCol = eventPct > 0 ? UiKit.Up : (eventPct < 0 ? UiKit.Down : UiKit.Dim);
                UiKit.Cellate(row, EventCol, evText, evCol, UIHorizontalAlignment.Right);

                // Trend sparkline (full history) — the at-a-glance hint; the full chart is one click away.
                string spark = Spark(hist);
                UILabel trend = UiKit.Cellate(row, TrendCol, spark ?? "—", UiKit.Dim, UIHorizontalAlignment.Left);
                if (spark != null) trend.tooltip = "Index trend (oldest → newest) — click the row for the full chart";

                // Expanded drawn chart, inserted directly under the selected commodity's row.
                if (open) AddExpandedChart(com, index, hist);
            }
        }

        // Toggle the expand chart for a commodity (collapse if it's already open) and rebuild. MAIN THREAD (click).
        private void Toggle(TransferManager.TransferReason reason)
        {
            if (_hasSelected && _selected == reason) { _hasSelected = false; }
            else { _selected = reason; _hasSelected = true; }
            Refresh();
        }

        // A drawn bar chart of the commodity's index history (a real UISprite chart via UiKit.BarChart — no font
        // glyphs), with a caption anchoring "now / high / low" as index multiples. Inserted as a full-width panel
        // in the vertical grid. MAIN THREAD.
        private void AddExpandedChart(Commodity com, float index, int[] hist)
        {
            UIPanel panel = _grid.AddUIComponent<UIPanel>();
            panel.autoLayout = false;
            panel.size = new Vector2(Width, PanelH);

            UILabel head = panel.AddUIComponent<UILabel>();
            head.textScale = 0.75f;
            head.textColor = UiKit.Head;
            head.relativePosition = new Vector3(6f, 3f);
            head.text = com.DisplayName + " — index history (oldest → newest)";

            if (hist == null || hist.Length < 2)
            {
                UILabel none = panel.AddUIComponent<UILabel>();
                none.textScale = 0.75f;
                none.textColor = UiKit.Dim;
                none.relativePosition = new Vector3(6f, 24f);
                none.text = "Not enough history yet — it fills in as the league index updates.";
                AddAlertStrip(panel, com);
                return;
            }

            int min = hist[0], max = hist[0];
            for (int i = 0; i < hist.Length; i++)
            {
                if (hist[i] < min) min = hist[i];
                if (hist[i] > max) max = hist[i];
            }

            const float chartTop = 22f;
            float chartH = ChartH - chartTop - 6f;
            Color32 barCol = index > 1.0f ? UiKit.Up : (index < 1.0f ? UiKit.Down : UiKit.Accent);
            UIPanel chart = UiKit.BarChart(panel, Width - 110f, chartH, hist, barCol);
            chart.relativePosition = new Vector3(6f, chartTop);
            // Minimal axis labels: y high/low as index multiples (× base) + x oldest→newest.
            int bp = Commodities.BasePrice(com.Reason);
            UiKit.AxisLabels(chart, Width - 110f, chartH,
                bp > 0 ? ((float)max / bp).ToString("0.00") + "x" : string.Empty,
                bp > 0 ? ((float)min / bp).ToString("0.00") + "x" : string.Empty,
                "oldest", "newest");

            // Caption on the right: now / high / low as index multiples (× base). BasePrice maps the stored price
            // units back to the index the player reads elsewhere.
            int basePrice = Commodities.BasePrice(com.Reason);
            UILabel stats = panel.AddUIComponent<UILabel>();
            stats.textScale = 0.75f;
            stats.textColor = UiKit.Flat;
            stats.relativePosition = new Vector3(Width - 98f, chartTop);
            stats.text = basePrice > 0
                ? "now  " + index.ToString("0.00") + "x\nhigh  " + ((float)max / basePrice).ToString("0.00")
                    + "x\nlow   " + ((float)min / basePrice).ToString("0.00") + "x\n" + hist.Length + " samples"
                : "now  " + index.ToString("0.00") + "x\n" + hist.Length + " samples";

            AddAlertStrip(panel, com);
        }

        // A small price-ALERT strip at the bottom of the expanded panel: a §/truck input + Set/Clear, and a status
        // line showing the live price and the current threshold. One-shot Chirp fires from the /prices poll when the
        // truck price crosses the threshold (see PriceAlerts). MAIN THREAD.
        private void AddAlertStrip(UIPanel panel, Commodity com)
        {
            TransferManager.TransferReason reason = com.Reason;   // loop-local for the closures
            float y = ChartH + 2f;

            UILabel cap = panel.AddUIComponent<UILabel>();
            cap.autoSize = true;
            cap.textScale = 0.7f;
            cap.textColor = UiKit.Head;
            cap.relativePosition = new Vector3(6f, y + 4f);
            cap.text = "Alert §/truck";

            _alertField = panel.AddUIComponent<UITextField>();
            _alertField.relativePosition = new Vector3(96f, y);
            _alertField.size = new Vector2(80f, 24f);
            _alertField.builtinKeyNavigation = true;
            _alertField.isInteractive = true;
            _alertField.readOnly = false;
            _alertField.canFocus = true;
            _alertField.numericalOnly = true;
            _alertField.allowFloats = false;
            _alertField.padding = new RectOffset(6, 6, 5, 0);
            UIView view = UIView.GetAView();
            if (view != null) _alertField.atlas = view.defaultAtlas;
            _alertField.normalBgSprite = "TextFieldPanel";
            _alertField.hoveredBgSprite = "TextFieldPanelHovered";
            _alertField.focusedBgSprite = "TextFieldPanel";
            _alertField.selectionSprite = "EmptySprite";
            _alertField.textScale = 0.8f;
            _alertField.textColor = UiKit.Flat;
            _alertField.color = Color.white;
            _alertField.horizontalAlignment = UIHorizontalAlignment.Left;
            long current = PriceAlerts.ThresholdOf(reason);
            if (current > 0) _alertField.text = (current / 100).ToString(CultureInfo.InvariantCulture);

            AlertButton(panel, 182f, y, 56f, "Set").eventClicked += delegate { SetAlert(reason); };
            AlertButton(panel, 242f, y, 60f, "Clear").eventClicked += delegate
            {
                PriceAlerts.Clear(reason);
                if (_alertField != null) _alertField.text = string.Empty;
                SyncAlertStatus(reason);
            };

            _alertStatus = panel.AddUIComponent<UILabel>();
            _alertStatus.autoSize = true;
            _alertStatus.textScale = 0.68f;
            _alertStatus.relativePosition = new Vector3(312f, y + 5f);
            SyncAlertStatus(reason);
        }

        private void SetAlert(TransferManager.TransferReason reason)
        {
            if (_alertField == null) return;
            long truckS;
            if (!long.TryParse(_alertField.text, NumberStyles.Integer, CultureInfo.InvariantCulture, out truckS) || truckS <= 0)
            {
                if (_alertStatus != null) { _alertStatus.text = "Enter a § amount > 0."; _alertStatus.textColor = UiKit.Down; }
                return;
            }
            PriceAlerts.Set(reason, truckS * 100);   // stored in cents
            SyncAlertStatus(reason);
        }

        private void SyncAlertStatus(TransferManager.TransferReason reason)
        {
            if (_alertStatus == null) return;
            long nowS = MarketFeed.Instance.PricePerTruckCents(reason) / 100;
            long th = PriceAlerts.ThresholdOf(reason);
            _alertStatus.textColor = UiKit.Dim;
            _alertStatus.text = th > 0
                ? "now §" + nowS.ToString("N0") + " · alert at §" + (th / 100).ToString("N0")
                : "now §" + nowS.ToString("N0") + " · no alert set";
        }

        private UIButton AlertButton(UIPanel panel, float x, float y, float w, string text)
        {
            UIButton b = panel.AddUIComponent<UIButton>();
            b.text = text;
            b.textScale = 0.68f;
            b.normalBgSprite = "ButtonMenu";
            b.hoveredBgSprite = "ButtonMenuHovered";
            b.pressedBgSprite = "ButtonMenuPressed";
            b.size = new Vector2(w, 24f);
            b.relativePosition = new Vector3(x, y);
            return b;
        }

        private UIPanel NewRow()
        {
            UIPanel row = _grid.AddUIComponent<UIPanel>();
            row.size = new Vector2(Width, UiKit.RowH);
            row.autoLayout = true;
            row.autoLayoutDirection = LayoutDirection.Horizontal;
            return row;
        }

        // Block-glyph sparkline from a commodity's recent prices, normalized min→max across its own window so the
        // full 8-level range is used regardless of absolute price. Null for <2 points (just loaded — no fake trend).
        private static string Spark(int[] hist)
        {
            if (hist == null || hist.Length < 2) return null;
            int min = hist[0], max = hist[0];
            for (int i = 0; i < hist.Length; i++)
            {
                if (hist[i] < min) min = hist[i];
                if (hist[i] > max) max = hist[i];
            }
            int range = max - min;
            int last = SparkGlyphs.Length - 1;
            char[] outc = new char[hist.Length];
            for (int i = 0; i < hist.Length; i++)
            {
                int level = range == 0 ? 0 : ((hist[i] - min) * last + range / 2) / range;
                if (level < 0) level = 0; else if (level > last) level = last;
                outc[i] = SparkGlyphs[level];
            }
            return new string(outc);
        }
    }
}
