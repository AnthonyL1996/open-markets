using System;
using System.Collections.Generic;
using System.Globalization;
using ColossalFramework.UI;
using OpenMarkets.Net;
using UnityEngine;

namespace OpenMarkets.UI.Terminal
{
    /// <summary>
    /// Chronicle tab (social slice 2): the league's persistent, narrated saga — founding, members joining,
    /// bailouts, austerity falls/escapes, and record trades, each a frozen full-sentence line the server rendered
    /// once (names already resolved, so it reads the same forever). A "📅 This day in league history" recall sits
    /// at the top when there's anything to surface, then the saga itself runs oldest→newest like a story. Two
    /// reads on Refresh — <see cref="OmApi.GetOnThisDay"/> + <see cref="OmApi.GetChronicle"/> — each cached and
    /// drawn from cache. MAIN THREAD, read-only.
    /// </summary>
    internal sealed class ChronicleTab : ITabBody
    {
        private const float Width = 560f;
        // Cap rendered saga rows per Rebuild so a long league history can't spawn thousands of CF components and
        // stall a frame. The saga reads oldest→newest, so this shows the most-recent 100 entries in order.
        private const int MaxRows = 100;

        private UIScrollablePanel _grid;
        private ChronicleDto _saga;        // the full chronicle (ascending)
        private ChronicleDto _onThisDay;   // prior-day entries matching today's month/day
        private bool _loadingSaga;
        private bool _loadingDay;
        private bool _failed;

        public string TabLabel { get { return "Chronicle"; } }
        public string Title { get { return "Open Markets — the league chronicle"; } }

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
            Rebuild(); // draw current cache (or loading/empty note) immediately

            if (!_loadingSaga)
            {
                _loadingSaga = true;
                OmApi.GetChronicle(delegate (bool ok, ChronicleDto dto)
                {
                    _loadingSaga = false;
                    if (ok && dto != null) { _saga = dto; _failed = false; }
                    else if (_saga == null) { _failed = true; }
                    Rebuild(); // main thread (OmHttp contract)
                });
            }
            if (!_loadingDay)
            {
                _loadingDay = true;
                OmApi.GetOnThisDay(delegate (bool ok, ChronicleDto dto)
                {
                    _loadingDay = false;
                    if (ok && dto != null) _onThisDay = dto; // best-effort; failure just hides the recall section
                    Rebuild();
                });
            }
        }

        private void Rebuild()
        {
            if (_grid == null) return;
            List<UIComponent> old = new List<UIComponent>(_grid.components);
            for (int i = 0; i < old.Count; i++)
            {
                _grid.RemoveUIComponent(old[i]);
                UnityEngine.Object.Destroy(old[i].gameObject);
            }

            // ---- "On this day in league history" (only when non-empty) ----
            ChronicleEntryDto[] otd = _onThisDay != null ? _onThisDay.entries : null;
            if (otd != null && otd.Length > 0)
            {
                Head("This day in league history:");
                for (int i = 0; i < otd.Length; i++)
                {
                    ChronicleEntryDto e = otd[i];
                    if (e == null || string.IsNullOrEmpty(e.text)) continue;
                    Row(When(e.created) + "   " + e.text, UiKit.Up);
                }
                Row(string.Empty, UiKit.Dim); // spacer
            }

            // ---- The saga (oldest -> newest, as a story) ----
            Head("The league's story:");
            ChronicleEntryDto[] saga = _saga != null ? _saga.entries : null;
            if (saga == null || saga.Length == 0)
            {
                bool loading = _loadingSaga && _saga == null;
                Note(loading ? "Loading..."
                    : _failed ? "Couldn't reach the server — try Refresh."
                    : "The league's story begins now.");
                return;
            }

            int start = saga.Length > MaxRows ? saga.Length - MaxRows : 0;
            if (start > 0) Note("… earlier history not shown …");
            for (int i = start; i < saga.Length; i++)
            {
                ChronicleEntryDto e = saga[i];
                if (e == null || string.IsNullOrEmpty(e.text)) continue;
                Row(When(e.created) + "   " + e.text, ColorFor(e.kind));
            }
        }

        // A color cue per chronicle kind (milestones stand out; routine joins read flat).
        private static Color32 ColorFor(string kind)
        {
            switch (kind)
            {
                case "founded": return UiKit.Head;
                case "record-trade": return UiKit.Up;
                case "bailout": return UiKit.Up;
                case "austerity": return UiKit.Down;
                case "escaped": return UiKit.Up;
                default: return UiKit.Flat; // joined / unknown
            }
        }

        // "MM-dd HH:mm" from an RFC3339 timestamp; falls back to the raw (trimmed) string if it won't parse.
        private static string When(string rfc3339)
        {
            if (string.IsNullOrEmpty(rfc3339)) return "";
            DateTime t;
            if (DateTime.TryParse(rfc3339, CultureInfo.InvariantCulture,
                    DateTimeStyles.AdjustToUniversal | DateTimeStyles.AssumeUniversal, out t))
                return t.ToLocalTime().ToString("MM-dd HH:mm", CultureInfo.InvariantCulture);
            return rfc3339.Length > 16 ? rfc3339.Substring(0, 16) : rfc3339;
        }

        private void Head(string text) { Row(text, UiKit.Head); }
        private void Note(string text) { Row("  " + text, UiKit.Dim); }

        private void Row(string text, Color32 color)
        {
            UIPanel row = _grid.AddUIComponent<UIPanel>();
            row.size = new Vector2(Width, UiKit.RowH);
            row.autoLayout = true;
            row.autoLayoutDirection = LayoutDirection.Horizontal;
            UiKit.Cellate(row, Width, text, color, UIHorizontalAlignment.Left);
        }
    }
}
