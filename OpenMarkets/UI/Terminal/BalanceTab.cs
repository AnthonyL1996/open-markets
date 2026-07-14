using System.Collections.Generic;
using ColossalFramework.UI;
using OpenMarkets.Market;
using UnityEngine;

namespace OpenMarkets.UI.Terminal
{
    /// <summary>
    /// Balance-of-trade tab: the city's LIFETIME exported §, imported §, and net § from outside-connection trade
    /// (M9: one figure each — no per-partner breakdown). Ties out to <see cref="PricingService.LifetimeNetCents"/>.
    /// MAIN THREAD.
    /// </summary>
    internal sealed class BalanceTab : ITabBody
    {
        private const float NameCol = 140f;
        private const float NumCol = 96f;

        private UIScrollablePanel _grid;

        public string TabLabel { get { return "Balance"; } }

        public string Title
        {
            get { return "Open Markets — net §" + (PricingService.LifetimeNetCents / 100).ToString("N0"); }
        }

        public void Build(UIComponent host, Vector2 size)
        {
            _grid = host.AddUIComponent<UIScrollablePanel>();
            _grid.relativePosition = Vector3.zero;
            _grid.size = size;
            // UIScrollablePanel child layout depends on size + auto-layout being assigned before rows exist.
            // The shell calls this on the Unity main thread; Refresh() is the only place rows are rebuilt.
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

            long export = PricingService.LifetimeExportCents;
            long import = PricingService.LifetimeImportCents;

            UIPanel header = NewRow();
            UiKit.Cellate(header, NameCol, "Lifetime trade", UiKit.Head, UIHorizontalAlignment.Left);
            UiKit.Cellate(header, NumCol, "§", UiKit.Head, UIHorizontalAlignment.Right);

            if (export == 0 && import == 0)
            {
                UIPanel empty = NewRow();
                UiKit.Cellate(empty, NameCol + NumCol, "No trades yet.", UiKit.Dim, UIHorizontalAlignment.Left);
                return;
            }

            ValueRow("Exported", export, UiKit.Up);
            ValueRow("Imported", import, UiKit.Down);

            long net = export - import;
            UIPanel totals = NewRow();
            UiKit.Cellate(totals, NameCol, "Net", UiKit.Head, UIHorizontalAlignment.Left);
            UiKit.Cellate(totals, NumCol, (net / 100).ToString("N0"), net >= 0 ? UiKit.Up : UiKit.Down, UIHorizontalAlignment.Right);
        }

        private void ValueRow(string label, long cents, Color32 color)
        {
            UIPanel row = NewRow();
            UiKit.Cellate(row, NameCol, label, UiKit.Flat, UIHorizontalAlignment.Left);
            UiKit.Cellate(row, NumCol, (cents / 100).ToString("N0"), color, UIHorizontalAlignment.Right);
        }

        private UIPanel NewRow()
        {
            UIPanel row = _grid.AddUIComponent<UIPanel>();
            row.size = new Vector2(NameCol + NumCol, UiKit.RowH);
            row.autoLayout = true;
            row.autoLayoutDirection = LayoutDirection.Horizontal;
            return row;
        }
    }
}
