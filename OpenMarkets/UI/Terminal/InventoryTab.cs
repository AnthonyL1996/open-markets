using System.Collections.Generic;
using ColossalFramework.UI;
using OpenMarkets.Data;
using OpenMarkets.Net;
using OpenMarkets.Trade;
using UnityEngine;

namespace OpenMarkets.UI.Terminal
{
    /// <summary>
    /// Inventory tab: tradeable stock in your <c>[trade]</c>-tagged depots, per commodity — Stored / Reserved
    /// (committed to give on active trades) / Available (= Stored − Reserved). Stored comes from
    /// <see cref="InventoryService"/>'s snapshot (local depot scan, shown offline too); Reserved is computed from
    /// the league's active trades, fetched async on Refresh. M6 Phase 1 — read-only; no stock moves yet. MAIN THREAD.
    /// </summary>
    internal sealed class InventoryTab : ITabBody
    {
        private const float NameCol = 160f;
        private const float NumCol = 100f;
        private const float FullWidth = NameCol + 3f * NumCol;

        private UIScrollablePanel _grid;
        private TradeDto[] _trades;   // last fetched active trades (for Reserved); null until first fetch
        private bool _loading;

        public string TabLabel { get { return "Inventory"; } }
        public string Title { get { return "Open Markets — trade inventory ([trade] depots)"; } }

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
            Rebuild(); // draw stored stock immediately (local snapshot); Reserved fills in after the trades fetch
            if (!Settings.IsOnlineConfigured || _loading) return;
            _loading = true;
            OmApi.GetTrades(delegate (bool ok, TradeListDto list)
            {
                _loading = false;
                if (ok && list != null) _trades = list.trades;
                Rebuild();
            });
        }

        private void Rebuild()
        {
            if (_grid == null) return;
            List<UIComponent> old = new List<UIComponent>(_grid.components);
            for (int i = 0; i < old.Count; i++) { _grid.RemoveUIComponent(old[i]); Object.Destroy(old[i].gameObject); }

            UIPanel header = NewRow();
            UiKit.Cellate(header, NameCol, "Commodity", UiKit.Head, UIHorizontalAlignment.Left);
            UiKit.Cellate(header, NumCol, "Stored", UiKit.Head, UIHorizontalAlignment.Right);
            UiKit.Cellate(header, NumCol, "Reserved", UiKit.Head, UIHorizontalAlignment.Right);
            UiKit.Cellate(header, NumCol, "Available", UiKit.Head, UIHorizontalAlignment.Right);

            Dictionary<TransferManager.TransferReason, long> stored = InventoryService.StoredUnitsSnapshot();
            string me = Settings.AccountIdValue;

            int shown = 0;
            IList<Commodity> all = Commodities.All;
            for (int i = 0; i < all.Count; i++)
            {
                Commodity c = all[i];
                long s; stored.TryGetValue(c.Reason, out s);
                long r = InventoryReservations.ReservedUnits(_trades, me, c.Reason);
                if (s == 0 && r == 0) continue;           // only commodities with stock or commitments
                long avail = s - r; if (avail < 0) avail = 0;

                UIPanel row = NewRow();
                UiKit.Cellate(row, NameCol, c.DisplayName, UiKit.Flat, UIHorizontalAlignment.Left);
                UiKit.Cellate(row, NumCol, s.ToString("N0"), UiKit.Flat, UIHorizontalAlignment.Right);
                UiKit.Cellate(row, NumCol, r.ToString("N0"), r > 0 ? UiKit.Down : UiKit.Dim, UIHorizontalAlignment.Right);
                UiKit.Cellate(row, NumCol, avail.ToString("N0"), avail > 0 ? UiKit.Up : UiKit.Down, UIHorizontalAlignment.Right);
                shown++;
            }

            if (shown == 0)
                Note("No tradeable stock. Rename an Industries warehouse to include [trade] and store a commodity in it.");
        }

        private void Note(string text)
        {
            UiKit.Cellate(NewRow(), FullWidth, text, UiKit.Dim, UIHorizontalAlignment.Left);
        }

        private UIPanel NewRow()
        {
            UIPanel row = _grid.AddUIComponent<UIPanel>();
            row.size = new Vector2(FullWidth, UiKit.RowH);
            row.autoLayout = true;
            row.autoLayoutDirection = LayoutDirection.Horizontal;
            return row;
        }
    }
}
