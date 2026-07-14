using System.Collections.Generic;
using ColossalFramework.UI;
using OpenMarkets.Net;
using UnityEngine;

namespace OpenMarkets.UI.Terminal
{
    /// <summary>
    /// Feed tab: the league's recent activity stream, newest-first — every settlement event (trades, bonds,
    /// garnishments, investments, bailouts, loans, shortfalls) rendered as a one-line human summary. Names resolve
    /// through <see cref="LeagueRoster.Display"/> so traveling titles show; amounts are §(cents/100). Fetched on
    /// Refresh via <see cref="OmApi.GetFeed"/>, cached, drawn from the cache. MAIN THREAD. Read-only.
    /// </summary>
    internal sealed class FeedTab : ITabBody
    {
        private const float IconCol = 26f;
        private const float SummaryCol = 360f;
        private const float AmountCol = 110f;
        private const float TypeCol = 110f;
        private const float Width = IconCol + SummaryCol + AmountCol + TypeCol;
        // Cap rendered rows per Rebuild so a long league history can't spawn thousands of CF components and stall a
        // frame. Items are newest-first, so this shows the most-recent activity.
        private const int MaxRows = 100;

        private UIPanel _root;
        private UIScrollablePanel _grid;

        private FeedDto _feed;
        private bool _loading;
        private bool _failed;

        public string TabLabel { get { return "Feed"; } }

        public string Title { get { return "Open Markets — league activity"; } }

        public void Build(UIComponent host, Vector2 size)
        {
            _root = host.AddUIComponent<UIPanel>();
            _root.relativePosition = Vector3.zero;
            _root.size = size;
            _root.autoLayout = false;

            _grid = _root.AddUIComponent<UIScrollablePanel>();
            _grid.relativePosition = Vector3.zero;
            _grid.size = size;
            _grid.autoLayout = true;
            _grid.autoLayoutDirection = LayoutDirection.Vertical;
            _grid.autoLayoutPadding = new RectOffset(0, 0, 0, 2);
            _grid.clipChildren = true;
            _grid.scrollWheelDirection = UIOrientation.Vertical;
        }

        public void SetVisible(bool on) { if (_root != null) _root.isVisible = on; }

        public void Refresh()
        {
            if (_grid == null) return;
            Rebuild();   // draw current cache (or loading/empty note) immediately
            if (_loading) return;
            _loading = true;
            OmApi.GetFeed(delegate (bool ok, FeedDto dto)
            {
                _loading = false;
                _failed = !ok || dto == null;
                if (!_failed) _feed = dto;
                Rebuild();   // callback is on the main thread (OmHttp contract)
            });
        }

        private void Rebuild()
        {
            if (_grid == null) return;
            List<UIComponent> old = new List<UIComponent>(_grid.components);
            for (int i = 0; i < old.Count; i++)
            {
                _grid.RemoveUIComponent(old[i]);
                Object.Destroy(old[i].gameObject);
            }

            FeedItemDto[] items = _feed != null ? _feed.items : null;
            if (items == null || items.Length == 0)
            {
                string note = _loading ? "Loading..."
                    : _failed ? "Couldn't reach the server — try Refresh."
                    : "No league activity yet.";
                UiKit.Cellate(NewRow(), Width, note, UiKit.Dim, UIHorizontalAlignment.Left);
                return;
            }

            int rows = items.Length < MaxRows ? items.Length : MaxRows;
            for (int i = 0; i < rows; i++)
            {
                FeedItemDto it = items[i];
                if (it == null) continue;
                UIPanel row = NewRow();
                UiKit.Cellate(row, IconCol, IconFor(it.type), UiKit.Accent, UIHorizontalAlignment.Left);
                string summary = LeagueRoster.Display(it.accountA) + " → " + LeagueRoster.Display(it.accountB);
                UiKit.Cellate(row, SummaryCol, summary, UiKit.Flat, UIHorizontalAlignment.Left);
                UiKit.Cellate(row, AmountCol, "§" + (it.cents / 100).ToString("N0"), UiKit.Head, UIHorizontalAlignment.Right);
                UiKit.Cellate(row, TypeCol, "(" + VerbFor(it.type) + ")", UiKit.Dim, UIHorizontalAlignment.Left);
            }
        }

        // A short glyph for each activity type (kept ASCII-safe for the in-game bitmap font).
        private static string IconFor(string type)
        {
            switch (type)
            {
                case "trade": return "$";
                case "bond": return "B";
                case "garnish": return "!";
                case "investment": return "+";
                case "bailout": return "~";
                case "shortfall": return "x";
                case "loan": return "L";
                default: return "-";
            }
        }

        // A human verb for each activity type, shown in the trailing "(...)" column.
        private static string VerbFor(string type)
        {
            switch (type)
            {
                case "trade": return "trade settled";
                case "bond": return "bond payment";
                case "garnish": return "garnished";
                case "investment": return "investment";
                case "bailout": return "bailout";
                case "shortfall": return "shortfall";
                case "loan": return "loan";
                default: return "transfer";
            }
        }

        private UIPanel NewRow()
        {
            UIPanel row = _grid.AddUIComponent<UIPanel>();
            row.size = new Vector2(Width, UiKit.RowH);
            row.autoLayout = true;
            row.autoLayoutDirection = LayoutDirection.Horizontal;
            return row;
        }
    }
}
