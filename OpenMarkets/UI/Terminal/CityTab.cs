using System.Collections.Generic;
using ColossalFramework.UI;
using OpenMarkets.Net;
using UnityEngine;

namespace OpenMarkets.UI.Terminal
{
    /// <summary>
    /// City tab: time-series GRAPHS of a leaguemate's city stats. A city picker (me + every roster member) chooses
    /// the primary city; a sectioned metric selector (Vitals / Finances / Economy / Trade &amp; Reputation) chooses
    /// what to plot; an optional "Compare vs" overlays one second city's same metric on a SHARED y-scale (so the two
    /// lines are directly comparable). The chart is drawn with <see cref="UiKit.LineChart"/> (dotted UISprite lines,
    /// no font glyphs) plus a two-colour legend and a now/high/low caption. History is fetched per city via
    /// <see cref="OmApi.GetCityHistory"/> on selection change, cached, and redrawn from cache on Rebuild. MAIN THREAD.
    /// Read-only. Lives only inside the OnlineMode.IsActive tab set, so it's inherently league-city-only.
    /// </summary>
    internal sealed class CityTab : ITabBody
    {
        // A selectable metric: its label + which series it reads from a snapshot (or the net-§ series).
        private sealed class Metric
        {
            public string Section;
            public string Label;
            public string Key;       // "net" → netSeries; "reliability" → snapshot.reliability; else a snapshot field
            public string Format;    // "cents" | "pct" | "num"
            public Metric(string section, string label, string key, string format)
            { Section = section; Label = label; Key = key; Format = format; }
        }

        // The metric catalogue, grouped by section (the selector renders one labelled row per section).
        private static readonly Metric[] Metrics =
        {
            new Metric("Vitals",   "Population",       "population",          "num"),
            new Metric("Vitals",   "Happiness",        "happiness",           "pct"),
            new Metric("Vitals",   "Attractiveness",   "attractiveness",      "num"),
            new Metric("Finances", "Treasury",         "cashCents",           "cents"),
            new Metric("Finances", "Weekly income",    "weeklyIncomeCents",   "cents"),
            new Metric("Finances", "Weekly expenses",  "weeklyExpensesCents", "cents"),
            new Metric("Economy",  "Buildings",        "buildingCount",       "num"),
            new Metric("Economy",  "Industry workers", "indWorkers",          "num"),
            new Metric("Economy",  "Land value",       "landValue",           "num"),
            new Metric("Trade & Reputation", "Net §",       "net",         "cents"),
            new Metric("Trade & Reputation", "Reliability", "reliability", "pct"),
        };

        private const float HeaderH = 132f;   // city picker (row 1) + metric selector (sections) + compare row
        private const float LegendH = 18f;

        // The compare overlay colour (primary uses UiKit.Accent). A warm amber, distinct from the accent blue.
        private static readonly Color32 CompareColor = new Color32(240, 196, 110, 255);
        // The league-average overlay colour — a muted green, distinct from the accent blue + compare amber.
        private static readonly Color32 LeagueAvgColor = new Color32(150, 210, 160, 255);

        private UIPanel _root;
        private UIPanel _pickerRow;       // city buttons
        private UIPanel _metricRows;      // sectioned metric buttons
        private UIPanel _compareRow;      // "Compare vs:" buttons
        private UIPanel _chartHost;       // the drawn chart + legend + caption live here
        private readonly List<UIButton> _cityBtns = new List<UIButton>();
        private readonly List<UIButton> _metricBtns = new List<UIButton>();
        private readonly List<UIButton> _compareBtns = new List<UIButton>();

        private string _primaryId = string.Empty;
        private string _compareId = string.Empty;   // empty = no overlay
        private int _metricIdx;                      // default 0 = Population
        private bool _showLeagueAvg;                 // overlay a synthetic league-average series for the metric

        // Per-city fetch cache + state (so switching metrics doesn't refetch; Rebuild redraws from here).
        private readonly Dictionary<string, CityHistoryDto> _cache = new Dictionary<string, CityHistoryDto>();
        private readonly HashSet<string> _loading = new HashSet<string>();
        private readonly HashSet<string> _failed = new HashSet<string>();

        public string TabLabel { get { return "City"; } }

        public string Title { get { return "Open Markets — city stats over time"; } }

        public void Build(UIComponent host, Vector2 size)
        {
            _root = host.AddUIComponent<UIPanel>();
            _root.relativePosition = Vector3.zero;
            _root.size = size;
            _root.autoLayout = false;

            _pickerRow = WrapRow(2f, 4f, size.x - 4f, 24f);
            _metricRows = _root.AddUIComponent<UIPanel>();
            _metricRows.relativePosition = new Vector3(2f, 32f);
            _metricRows.size = new Vector2(size.x - 4f, 72f);
            _metricRows.autoLayout = false;
            _compareRow = WrapRow(2f, 106f, size.x - 4f, 24f);

            _chartHost = _root.AddUIComponent<UIPanel>();
            _chartHost.relativePosition = new Vector3(2f, HeaderH);
            _chartHost.size = new Vector2(size.x - 4f, size.y - HeaderH);
            _chartHost.autoLayout = false;

            if (string.IsNullOrEmpty(_primaryId)) _primaryId = Settings.AccountIdValue;
        }

        public void SetVisible(bool on) { if (_root != null) _root.isVisible = on; }

        public void Refresh()
        {
            if (_root == null) return;
            // Default the primary city to me; ensure it's still a valid roster member otherwise.
            EnsureValidSelection();
            BuildPickers();
            // Re-fetch the cities currently in view (Refresh = force reload), then redraw.
            Fetch(_primaryId, true);
            if (!string.IsNullOrEmpty(_compareId)) Fetch(_compareId, true);
            Rebuild();
        }

        // Force = drop any cached/failed marker first (Refresh); otherwise only fetch if we have nothing cached yet.
        private void Fetch(string id, bool force)
        {
            if (string.IsNullOrEmpty(id)) return;
            if (force) { _cache.Remove(id); _failed.Remove(id); }
            else if (_cache.ContainsKey(id)) return;
            if (_loading.Contains(id)) return;
            _loading.Add(id);
            string target = id;
            OmApi.GetCityHistory(target, delegate (bool ok, CityHistoryDto dto)
            {
                _loading.Remove(target);
                if (ok && dto != null) _cache[target] = dto;
                else _failed.Add(target);
                Rebuild();   // callback runs on the main thread (OmHttp contract)
            });
        }

        // ---- pickers ----

        private void BuildPickers()
        {
            BuildCityPicker();
            BuildMetricSelector();
            BuildComparePicker();
        }

        private void BuildCityPicker()
        {
            ClearRow(_pickerRow, _cityBtns);
            Caption(_pickerRow, "City:");
            List<string> ids = AllCities();
            for (int i = 0; i < ids.Count; i++)
            {
                string id = ids[i];   // loop-local for the closure
                UIButton b = SelButton(_pickerRow, LeagueRoster.Display(id), id == _primaryId);
                b.eventClicked += delegate { _primaryId = id; if (_compareId == id) _compareId = string.Empty; OnSelectionChanged(); };
                _cityBtns.Add(b);
            }
        }

        private void BuildMetricSelector()
        {
            for (int i = 0; i < _metricBtns.Count; i++)
            {
                _metricRows.RemoveUIComponent(_metricBtns[i]);
                Object.Destroy(_metricBtns[i].gameObject);
            }
            _metricBtns.Clear();
            // Clear any prior section captions too (children that aren't buttons).
            List<UIComponent> old = new List<UIComponent>(_metricRows.components);
            for (int i = 0; i < old.Count; i++) { _metricRows.RemoveUIComponent(old[i]); Object.Destroy(old[i].gameObject); }

            // Lay the buttons out by hand in section rows so the captions line up (autoLayout wrap can't label rows).
            string section = null;
            float x = 0f, y = 0f;
            const float rowH = 24f;
            for (int i = 0; i < Metrics.Length; i++)
            {
                Metric m = Metrics[i];
                if (m.Section != section)
                {
                    if (section != null) { y += rowH; }
                    section = m.Section;
                    x = 0f;
                    UILabel cap = _metricRows.AddUIComponent<UILabel>();
                    cap.autoSize = false;
                    cap.size = new Vector2(116f, rowH);
                    cap.textScale = 0.62f;
                    cap.textColor = UiKit.Head;
                    cap.verticalAlignment = UIVerticalAlignment.Middle;
                    cap.relativePosition = new Vector3(0f, y + 2f);
                    cap.text = m.Section;
                    x = 118f;
                }
                int idx = i;   // loop-local for the closure
                float w = Mathf.Max(56f, m.Label.Length * 6.2f + 12f);
                UIButton b = _metricRows.AddUIComponent<UIButton>();
                b.text = m.Label;
                b.textScale = 0.62f;
                b.normalBgSprite = i == _metricIdx ? "ButtonMenuPressed" : "ButtonMenu";
                b.hoveredBgSprite = "ButtonMenuHovered";
                b.autoSize = false;
                b.size = new Vector2(w, 22f);
                b.relativePosition = new Vector3(x, y + 1f);
                b.eventClicked += delegate { _metricIdx = idx; OnSelectionChanged(); };
                _metricBtns.Add(b);
                x += w + 4f;
            }
        }

        private void BuildComparePicker()
        {
            ClearRow(_compareRow, _compareBtns);
            Caption(_compareRow, "Compare vs:");
            UIButton none = SelButton(_compareRow, "none", string.IsNullOrEmpty(_compareId));
            none.eventClicked += delegate { _compareId = string.Empty; OnSelectionChanged(); };
            _compareBtns.Add(none);
            List<string> ids = AllCities();
            for (int i = 0; i < ids.Count; i++)
            {
                string id = ids[i];
                if (id == _primaryId) continue;   // can't compare a city with itself
                UIButton b = SelButton(_compareRow, LeagueRoster.Display(id), id == _compareId);
                b.eventClicked += delegate { _compareId = id; OnSelectionChanged(); };
                _compareBtns.Add(b);
            }

            // A toggle that overlays the synthetic LEAGUE-AVERAGE series for the selected metric (computed client-side
            // from every member's cached history). Sits at the end of the compare row.
            UIButton avg = SelButton(_compareRow, "League avg", _showLeagueAvg);
            avg.eventClicked += delegate { _showLeagueAvg = !_showLeagueAvg; OnSelectionChanged(); };
            _compareBtns.Add(avg);
        }

        // Ensure every league member's history is fetched (for the league-average overlay). Friend-scale (≤8), so a
        // batch of cheap cached GETs. Non-forcing: only members not already cached/loading are requested.
        private void FetchAllMembers()
        {
            List<string> ids = AllCities();
            for (int i = 0; i < ids.Count; i++) Fetch(ids[i], false);
        }

        // A selection changed → re-sync the picker highlights, fetch any newly-needed history, redraw.
        private void OnSelectionChanged()
        {
            BuildPickers();
            Fetch(_primaryId, false);
            if (!string.IsNullOrEmpty(_compareId)) Fetch(_compareId, false);
            if (_showLeagueAvg) FetchAllMembers();
            Rebuild();
        }

        // ---- chart ----

        private void Rebuild()
        {
            if (_chartHost == null) return;
            List<UIComponent> old = new List<UIComponent>(_chartHost.components);
            for (int i = 0; i < old.Count; i++) { _chartHost.RemoveUIComponent(old[i]); Object.Destroy(old[i].gameObject); }

            Metric metric = Metrics[_metricIdx];

            long[] primary = SeriesFor(_primaryId, metric);
            long[] compare = string.IsNullOrEmpty(_compareId) ? null : SeriesFor(_compareId, metric);

            // Loading / failure / empty notes (primary drives the state line).
            if (_loading.Contains(_primaryId) && primary == null) { Note("Loading..."); return; }
            if (_failed.Contains(_primaryId) && !_cache.ContainsKey(_primaryId)) { Note("Couldn't reach the server — try Refresh."); return; }
            if (primary == null || primary.Length == 0) { Note("No history yet — graphs fill in as days pass."); return; }

            // Optional synthetic league-average series for this metric (null if not enough members cached yet).
            long[] leagueAvg = _showLeagueAvg ? LeagueAverage(metric) : null;

            // Shared y-range across ALL drawn series so the overlays are fair.
            long minY = primary[0], maxY = primary[0];
            Extend(ref minY, ref maxY, primary);
            if (compare != null) Extend(ref minY, ref maxY, compare);
            if (leagueAvg != null && leagueAvg.Length > 0) Extend(ref minY, ref maxY, leagueAvg);

            // Assemble the series + matching colours in a fixed order (primary, compare, league-avg).
            List<long[]> seriesList = new List<long[]>(3);
            List<Color32> colorList = new List<Color32>(3);
            seriesList.Add(primary); colorList.Add(UiKit.Accent);
            if (compare != null) { seriesList.Add(compare); colorList.Add(CompareColor); }
            if (leagueAvg != null && leagueAvg.Length > 0) { seriesList.Add(leagueAvg); colorList.Add(LeagueAvgColor); }
            long[][] series = seriesList.ToArray();
            Color32[] colors = colorList.ToArray();

            // Legend (top) → colour swatches + city names / overlay labels.
            float legendY = 2f;
            AddLegend(2f, legendY, UiKit.Accent, LeagueRoster.Display(_primaryId) + " — " + metric.Label);
            if (compare != null)
                AddLegend(_chartHost.width * 0.5f, legendY, CompareColor, LeagueRoster.Display(_compareId));
            if (leagueAvg != null && leagueAvg.Length > 0)
                AddLegend(_chartHost.width * 0.75f, legendY, LeagueAvgColor, "League avg");

            float chartTop = LegendH + 4f;
            float chartW = _chartHost.width - 110f;
            float chartH = _chartHost.height - chartTop - 6f;
            UIPanel chart = UiKit.LineChart(_chartHost, chartW, chartH, series, colors, minY, maxY);
            chart.relativePosition = new Vector3(2f, chartTop);
            // Minimal axis labels: y high/low (the shared scale) + x oldest→newest, formatted per the metric.
            UiKit.AxisLabels(chart, chartW, chartH, Fmt(maxY, metric.Format), Fmt(minY, metric.Format),
                "oldest", "newest");

            // Caption on the right: now / high / low for the PRIMARY series, formatted per the metric.
            long now = primary[primary.Length - 1], hi = primary[0], lo = primary[0];
            Extend(ref lo, ref hi, primary);
            UILabel stats = _chartHost.AddUIComponent<UILabel>();
            stats.textScale = 0.72f;
            stats.textColor = UiKit.Flat;
            stats.relativePosition = new Vector3(_chartHost.width - 100f, chartTop);
            stats.text = "now  " + Fmt(now, metric.Format)
                + "\nhigh  " + Fmt(hi, metric.Format)
                + "\nlow   " + Fmt(lo, metric.Format)
                + "\n" + primary.Length + " samples";
        }

        // Build the long[] series for a city + metric. Net § reads the netSeries; everything else reads a snapshot
        // field per sample (missing → 0). Returns null when the city has no cached history at all.
        private long[] SeriesFor(string id, Metric metric)
        {
            CityHistoryDto dto;
            if (string.IsNullOrEmpty(id) || !_cache.TryGetValue(id, out dto) || dto == null) return null;

            if (metric.Key == "net")
            {
                NetPointDto[] pts = dto.netSeries;
                if (pts == null) return new long[0];
                long[] outv = new long[pts.Length];
                for (int i = 0; i < pts.Length; i++) outv[i] = pts[i] != null ? pts[i].cents : 0L;
                return outv;
            }

            CitySnapshotDto[] snaps = dto.snapshots;
            if (snaps == null) return new long[0];
            long[] vals = new long[snaps.Length];
            for (int i = 0; i < snaps.Length; i++) vals[i] = snaps[i] != null ? FieldValue(snaps[i], metric.Key) : 0L;
            return vals;
        }

        // The synthetic LEAGUE-AVERAGE series for a metric: average, index-by-index, every member's history that we
        // have cached, RIGHT-ALIGNED to the most recent sample (members have different sample counts; aligning at the
        // newest end keeps "now" comparable). The length is the shortest cached member series, so every averaged point
        // has a value from each member. Returns null with <2 members cached (no meaningful average yet). MAIN THREAD.
        private long[] LeagueAverage(Metric metric)
        {
            List<long[]> members = new List<long[]>();
            List<string> ids = AllCities();
            int shortest = int.MaxValue;
            for (int i = 0; i < ids.Count; i++)
            {
                long[] s = SeriesFor(ids[i], metric);
                if (s == null || s.Length == 0) continue;   // not cached / no history → skip this member
                members.Add(s);
                if (s.Length < shortest) shortest = s.Length;
            }
            if (members.Count < 2 || shortest == int.MaxValue || shortest <= 0) return null;

            long[] avg = new long[shortest];
            for (int k = 0; k < shortest; k++)
            {
                long sum = 0;
                for (int m = 0; m < members.Count; m++)
                {
                    long[] s = members[m];
                    sum += s[s.Length - shortest + k];   // right-aligned index
                }
                avg[k] = sum / members.Count;
            }
            return avg;
        }

        // Read one snapshot field by metric key. Missing/unknown → 0 (matches the omitempty wire contract).
        private static long FieldValue(CitySnapshotDto s, string key)
        {
            switch (key)
            {
                case "population":          return s.population;
                case "happiness":           return s.happiness;
                case "attractiveness":      return s.attractiveness;
                case "cashCents":           return s.cashCents;
                case "weeklyIncomeCents":   return s.weeklyIncomeCents;
                case "weeklyExpensesCents": return s.weeklyExpensesCents;
                case "buildingCount":       return s.buildingCount;
                case "indWorkers":          return s.indWorkers;
                case "landValue":           return s.landValue;
                case "reliability":         return s.reliability;
                default:                    return 0L;
            }
        }

        private static void Extend(ref long min, ref long max, long[] vals)
        {
            for (int i = 0; i < vals.Length; i++)
            {
                if (vals[i] < min) min = vals[i];
                if (vals[i] > max) max = vals[i];
            }
        }

        // Caption value formatting: cents → "§" + (v/100) N0; pct → v + "%"; num → v N0.
        private static string Fmt(long v, string format)
        {
            switch (format)
            {
                case "cents": return "§" + (v / 100).ToString("N0");
                case "pct":   return v + "%";
                default:      return v.ToString("N0");
            }
        }

        private void Note(string text)
        {
            UILabel l = _chartHost.AddUIComponent<UILabel>();
            l.textScale = 0.78f;
            l.textColor = UiKit.Dim;
            l.relativePosition = new Vector3(6f, 24f);
            l.text = text;
        }

        private void AddLegend(float x, float y, Color32 color, string text)
        {
            UISprite sw = _chartHost.AddUIComponent<UISprite>();
            sw.spriteName = "EmptySprite";
            sw.color = color;
            sw.size = new Vector2(10f, 10f);
            sw.relativePosition = new Vector3(x, y + 2f);
            UILabel l = _chartHost.AddUIComponent<UILabel>();
            l.autoSize = true;
            l.textScale = 0.7f;
            l.textColor = color;
            l.relativePosition = new Vector3(x + 14f, y);
            l.text = text;
        }

        // ---- selection helpers ----

        // The full picker list: me first, then every other roster member.
        private static List<string> AllCities()
        {
            List<string> ids = new List<string>();
            string me = Settings.AccountIdValue;
            if (!string.IsNullOrEmpty(me)) ids.Add(me);
            List<string> members = LeagueRoster.MemberIds();
            for (int i = 0; i < members.Count; i++)
                if (members[i] != me && !ids.Contains(members[i])) ids.Add(members[i]);
            return ids;
        }

        private void EnsureValidSelection()
        {
            List<string> ids = AllCities();
            if (ids.Count == 0) { _primaryId = Settings.AccountIdValue; return; }
            if (string.IsNullOrEmpty(_primaryId) || !ids.Contains(_primaryId)) _primaryId = ids[0];
            if (!string.IsNullOrEmpty(_compareId) && (!ids.Contains(_compareId) || _compareId == _primaryId))
                _compareId = string.Empty;
        }

        // ---- ui builders ----

        private UIPanel WrapRow(float x, float y, float w, float h)
        {
            UIPanel row = _root.AddUIComponent<UIPanel>();
            row.relativePosition = new Vector3(x, y);
            row.size = new Vector2(w, h);
            row.autoLayout = true;
            row.autoLayoutDirection = LayoutDirection.Horizontal;
            row.autoLayoutPadding = new RectOffset(0, 4, 0, 0);
            row.wrapLayout = true;
            return row;
        }

        private static void ClearRow(UIPanel row, List<UIButton> btns)
        {
            List<UIComponent> old = new List<UIComponent>(row.components);
            for (int i = 0; i < old.Count; i++) { row.RemoveUIComponent(old[i]); Object.Destroy(old[i].gameObject); }
            btns.Clear();
        }

        private static void Caption(UIPanel row, string text)
        {
            UILabel l = row.AddUIComponent<UILabel>();
            l.autoSize = false;
            l.size = new Vector2(text.Length * 6.4f + 6f, 22f);
            l.textScale = 0.66f;
            l.textColor = UiKit.Head;
            l.verticalAlignment = UIVerticalAlignment.Middle;
            l.text = text;
        }

        private static UIButton SelButton(UIPanel row, string label, bool selected)
        {
            UIButton b = row.AddUIComponent<UIButton>();
            b.text = label;
            b.textScale = 0.62f;
            b.normalBgSprite = selected ? "ButtonMenuPressed" : "ButtonMenu";
            b.hoveredBgSprite = "ButtonMenuHovered";
            b.autoSize = false;
            b.size = new Vector2(Mathf.Max(56f, label.Length * 6.0f + 14f), 22f);
            return b;
        }
    }
}
