using System.Collections.Generic;
using ColossalFramework.UI;
using OpenMarkets.Data;
using OpenMarkets.Market;
using OpenMarkets.Net;
using UnityEngine;

namespace OpenMarkets.UI.Terminal
{
    /// <summary>
    /// Inbox tab: the action surface for trades. Two sections — (c) INCOMING trade baskets awaiting your consent
    /// (accept/decline), and (d) UPCOMING trade payments (informational — these auto-settle on the server's
    /// due-clock once a trade is agreed). Accept/decline are main-thread UI/HTTP; the booked cash + goods arrive
    /// via the background /settlements and /trades polls.
    /// MAIN THREAD.
    /// </summary>
    internal sealed class InboxTab : ITabBody
    {
        private const float DescCol = 360f;
        private const float BtnCol = 90f;
        private const float Width = DescCol + BtnCol + BtnCol;

        private UIScrollablePanel _grid;
        private TradeDto[] _trades;
        private bool _tradesLoading;
        private string _status = string.Empty;   // last action's failure reason (cleared on any success)

        public string TabLabel { get { return "Inbox"; } }

        public string Title { get { return "Open Markets — inbox"; } }

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
            Rebuild();
            if (!_tradesLoading)
            {
                _tradesLoading = true;
                OmApi.GetTrades(delegate (bool ok, TradeListDto list)
                {
                    _tradesLoading = false;
                    if (ok && list != null) _trades = list.trades;
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
                Object.Destroy(old[i].gameObject);
            }

            string me = Settings.AccountIdValue;

            // A failed accept/decline leaves its reason here until the next successful action.
            if (!string.IsNullOrEmpty(_status))
            {
                UIPanel srow = NewRow();
                UiKit.Cellate(srow, Width, _status, UiKit.Down, UIHorizontalAlignment.Left);
            }

            // Section C: incoming trade baskets awaiting my consent (offered by someone else).
            Header("Incoming trades");
            int tradeOffers = 0;
            if (_trades != null)
            {
                for (int i = 0; i < _trades.Length; i++)
                {
                    TradeDto t = _trades[i];
                    if (t == null || t.status != "offered" || t.offeredBy == me) continue;
                    tradeOffers++;
                    UIPanel row = NewRow();
                    UiKit.Cellate(row, DescCol, DescribeTrade(t, me), UiKit.Flat, UIHorizontalAlignment.Left);
                    string id = t.id;
                    AddButton(row, "accept", delegate { ActTrade(id, "accept"); });
                    AddButton(row, "decline", delegate { ActTrade(id, "decline"); });
                }
            }
            if (tradeOffers == 0) Note("No trades awaiting you.");

            // Section D: upcoming trade payments — these settle AUTOMATICALLY on the server's due-clock once a trade
            // is agreed (no manual action). This is an informational readout of what this city will pay next; the
            // booked cash arrives via the background /settlements poll and goods deliver via the /trades poll.
            Header("Upcoming trade payments (auto-settled)");
            int tradeDue = 0;
            if (_trades != null)
            {
                for (int i = 0; i < _trades.Length; i++)
                {
                    TradeDto t = _trades[i];
                    if (t == null || t.status != "active" || t.settled >= t.installments) continue;
                    if (!TradeMath.IsNetPayer(t, me)) continue; // I'm the receiver — nothing to pay
                    tradeDue++;
                    long owe = TradeMath.OffererNetCents(t.items);
                    if (owe < 0) owe = -owe;
                    long gross = TradeMath.GrossCents(t.items, PriceOf); // active → uses the frozen accept values
                    string desc = "Trade w/ " + LeagueRoster.Display(Other(t, me))
                                + "  —  deal §" + (gross / 100)
                                + ", installment " + (t.settled + 1) + "/" + t.installments
                                + " (auto-pays §" + (owe / t.installments / 100) + " next)";
                    UIPanel row = NewRow();
                    UiKit.Cellate(row, DescCol, desc, UiKit.Flat, UIHorizontalAlignment.Left);
                }
            }
            if (tradeDue == 0) Note("No upcoming trade payments.");
        }

        // ---- actions (main thread) ----

        // Accept/decline an incoming trade basket (atomic), then refresh.
        private void ActTrade(string tradeId, string action)
        {
            OmApi.TradeTransition(tradeId, action, delegate (bool ok, TradeDto updated, string error)
            {
                if (ok) { _status = string.Empty; Refresh(); }
                else { _status = Cap(action) + " trade failed: " + Reason(error); Rebuild(); }
            });
        }

        // One-line summary of an incoming basket, from MY (the counterparty's) perspective.
        private static string DescribeTrade(TradeDto t, string me)
        {
            int give = 0, take = 0;
            if (t.items != null)
            {
                for (int i = 0; i < t.items.Length; i++)
                {
                    if (t.items[i] == null) continue;
                    if (t.items[i].dir == "give") give++; else take++;
                }
            }
            // Offered trades aren't frozen yet (valueCentsAtAccept = 0), so value them INDICATIVELY off the live feed.
            long gross = TradeMath.GrossCents(t.items, PriceOf);
            long netToMe = -TradeMath.OffererNetCents(t.items, PriceOf); // offerer's net is the negation of mine
            string sign = netToMe >= 0 ? "+§" : "-§";
            long mag = netToMe < 0 ? -netToMe : netToMe;
            // The offerer's "give" lines flow to me, their "take" lines come from me.
            return "From " + LeagueRoster.Display(t.offeredBy) + ": you get " + give + " / give " + take
                + "  —  deal §" + (gross / 100) + " (net to you " + sign + (mag / 100) + ")";
        }

        // Index-aware unit price (scaled index units) for a commodity wire key, for indicative offer valuation:
        // key → reason → MarketFeed (the live league index; neutral base when offline). Unknown key → 0.
        private static long PriceOf(string commodityKey)
        {
            TransferManager.TransferReason r;
            return Commodities.TryFromKey(commodityKey, out r) ? MarketFeed.Instance.GetPrice(r) : 0L;
        }

        private static string Other(TradeDto t, string me)
        {
            return t.offeredBy == me ? t.counterparty : t.offeredBy;
        }

        private static string Reason(string error)
        {
            return string.IsNullOrEmpty(error) ? "check the server is reachable." : error;
        }

        private static string Cap(string s)
        {
            if (string.IsNullOrEmpty(s)) return s;
            return char.ToUpper(s[0]) + s.Substring(1);
        }

        // ---- row helpers ----

        private void Header(string text)
        {
            UIPanel row = NewRow();
            UiKit.Cellate(row, Width, text, UiKit.Head, UIHorizontalAlignment.Left);
        }

        private void Note(string text)
        {
            UIPanel row = NewRow();
            UiKit.Cellate(row, Width, text, UiKit.Dim, UIHorizontalAlignment.Left);
        }

        private void AddButton(UIPanel row, string text, MouseEventHandler onClick)
        {
            UIButton b = row.AddUIComponent<UIButton>();
            b.text = text;
            b.textScale = 0.7f;
            b.normalBgSprite = "ButtonMenu";
            b.hoveredBgSprite = "ButtonMenuHovered";
            b.size = new Vector2(BtnCol - 4f, UiKit.RowH - 2f);
            b.eventClicked += onClick;
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
