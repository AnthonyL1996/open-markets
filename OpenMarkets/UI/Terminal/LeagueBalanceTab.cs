using System.Collections.Generic;
using ColossalFramework.UI;
using OpenMarkets.Net;
using UnityEngine;

namespace OpenMarkets.UI.Terminal
{
    /// <summary>
    /// League balance tab: one row per leaguemate showing the net § that has SETTLED between us (+ they paid me,
    /// − I paid them; summed from the caller-scoped /settlements feed), the OUTSTANDING net § still to flow on
    /// active trades/loans (+ they will pay me, − I will pay them), and the member's reliability. Replaces the
    /// offline NPC balance-of-trade view (<see cref="BalanceTab"/>, kept but unregistered) while we focus on
    /// online play. Stateless: fetches members + settlements + trades + bonds on Refresh and combines them; no
    /// new persistence. MAIN THREAD (the OmApi callbacks run on the main thread). Read-only view of server truth.
    /// </summary>
    internal sealed class LeagueBalanceTab : ITabBody
    {
        private const float NameCol = 150f;
        private const float NumCol = 110f;
        private const float RelCol = 80f;
        private const float FullWidth = NameCol + 2f * NumCol + RelCol;

        private UIScrollablePanel _grid;
        private bool _loading;
        private bool _failed;

        // Last successful fetch of each feed (kept across a Refresh so a single failing feed doesn't blank the rest).
        private MemberDto[] _members;
        private SettlementEventDto[] _settlements;
        private TradeDto[] _trades;
        private BondDto[] _bonds;

        public string TabLabel { get { return "Balance"; } }
        public string Title { get { return "Open Markets — league balance"; } }

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
            Rebuild(); // draw current cache (or a loading/offline note) immediately
            if (!Settings.IsOnlineConfigured || _loading) return;
            _loading = true;

            // Four independent feeds; combine once all four return. Callbacks run on the main thread in sequence,
            // so the shared counter needs no locking. The roster is the anchor — without it we show an error note.
            int[] pending = { 4 };
            bool[] membersOk = { false };
            System.Action done = delegate
            {
                if (--pending[0] > 0) return;
                _loading = false;
                _failed = !membersOk[0];
                Rebuild();
            };
            OmApi.GetMembers(delegate (bool ok, MembersDto dto)
            { if (ok && dto != null) { _members = dto.members; membersOk[0] = true; } done(); });
            OmApi.GetSettlements(0L, delegate (bool ok, SettlementListDto dto)
            { if (ok && dto != null) _settlements = dto.events; done(); });
            OmApi.GetTrades(delegate (bool ok, TradeListDto dto)
            { if (ok && dto != null) _trades = dto.trades; done(); });
            OmApi.GetBonds(delegate (bool ok, BondListDto dto)
            { if (ok && dto != null) _bonds = dto.bonds; done(); });
        }

        private void Rebuild()
        {
            if (_grid == null) return;
            List<UIComponent> old = new List<UIComponent>(_grid.components);
            for (int i = 0; i < old.Count; i++) { _grid.RemoveUIComponent(old[i]); Object.Destroy(old[i].gameObject); }

            UIPanel header = NewRow();
            UiKit.Cellate(header, NameCol, "Leaguemate", UiKit.Head, UIHorizontalAlignment.Left);
            UiKit.Cellate(header, NumCol, "Settled §", UiKit.Head, UIHorizontalAlignment.Right);
            UiKit.Cellate(header, NumCol, "Outstanding §", UiKit.Head, UIHorizontalAlignment.Right);
            UiKit.Cellate(header, RelCol, "Reliab.", UiKit.Head, UIHorizontalAlignment.Right);

            if (!Settings.IsOnlineConfigured) { Note("Offline — set account + league in Options."); return; }
            if (_members == null)
            {
                Note(_loading ? "Loading..." : _failed ? "Couldn't reach the server — try Refresh." : "No league members.");
                return;
            }

            string me = Settings.AccountIdValue;
            long totalSettled = 0, totalOutstanding = 0;
            int shown = 0;
            for (int i = 0; i < _members.Length; i++)
            {
                MemberDto m = _members[i];
                if (m == null || m.accountId == me) continue;
                long settled = SettledWith(me, m.accountId);
                long outstanding = OutstandingWith(me, m.accountId);
                totalSettled += settled;
                totalOutstanding += outstanding;
                shown++;

                string name = !string.IsNullOrEmpty(m.displayName) ? m.displayName : OnlineSync.ShortId(m.accountId);
                UIPanel row = NewRow();
                UiKit.Cellate(row, NameCol, name, UiKit.Flat, UIHorizontalAlignment.Left);
                UiKit.Cellate(row, NumCol, Money(settled), settled >= 0 ? UiKit.Up : UiKit.Down, UIHorizontalAlignment.Right);
                UiKit.Cellate(row, NumCol, Money(outstanding), outstanding >= 0 ? UiKit.Up : UiKit.Down, UIHorizontalAlignment.Right);
                Color32 rel = m.reliability >= 80 ? UiKit.Up : (m.reliability < 50 ? UiKit.Down : UiKit.Flat);
                UiKit.Cellate(row, RelCol, m.reliability + "%", rel, UIHorizontalAlignment.Right);
            }

            if (shown == 0) { Note("No other leaguemates yet — share your join code."); return; }

            UIPanel totals = NewRow();
            UiKit.Cellate(totals, NameCol, "TOTALS", UiKit.Head, UIHorizontalAlignment.Left);
            UiKit.Cellate(totals, NumCol, Money(totalSettled), totalSettled >= 0 ? UiKit.Up : UiKit.Down, UIHorizontalAlignment.Right);
            UiKit.Cellate(totals, NumCol, Money(totalOutstanding), totalOutstanding >= 0 ? UiKit.Up : UiKit.Down, UIHorizontalAlignment.Right);
            UiKit.Cellate(totals, RelCol, string.Empty, UiKit.Head, UIHorizontalAlignment.Right);
        }

        // Net § that has SETTLED between me and `other`: + when they paid me, − when I paid them.
        private long SettledWith(string me, string other)
        {
            long net = 0;
            if (_settlements == null) return net;
            for (int i = 0; i < _settlements.Length; i++)
            {
                SettlementEventDto e = _settlements[i];
                if (e == null) continue;
                if (e.receiverId == me && e.payerId == other) net += e.cents;
                else if (e.payerId == me && e.receiverId == other) net -= e.cents;
            }
            return net;
        }

        // Net § still to flow on active trades + loans with `other`: + they will pay me, − I will pay them.
        private long OutstandingWith(string me, string other)
        {
            long net = 0;
            if (_trades != null)
                for (int i = 0; i < _trades.Length; i++)
                {
                    TradeDto t = _trades[i];
                    if (t == null || t.status != "active" || t.settled >= t.installments) continue;
                    bool involved = (t.offeredBy == me && t.counterparty == other)
                                 || (t.offeredBy == other && t.counterparty == me);
                    if (!involved) continue;

                    // value I take − value I give over the basket (item.dir is relative to offeredBy; flip if I'm
                    // the counterparty). perspectiveNet > 0 ⇒ I receive more value ⇒ I owe the difference (payer).
                    bool flip = t.counterparty == me;
                    long take = 0, give = 0;
                    if (t.items != null)
                        for (int k = 0; k < t.items.Length; k++)
                        {
                            LineItemDto it = t.items[k];
                            if (it == null) continue;
                            string dir = it.dir;
                            if (flip) dir = dir == "give" ? "take" : "give";
                            if (dir == "take") take += it.valueCentsAtAccept; else give += it.valueCentsAtAccept;
                        }
                    long perspectiveNet = take - give;
                    int remaining = t.installments - t.settled;
                    long remCash = perspectiveNet * remaining / t.installments; // > 0 ⇒ I still owe this much
                    net -= remCash;                                             // owing is negative to me
                }

            if (_bonds != null)
                for (int i = 0; i < _bonds.Length; i++)
                {
                    BondDto b = _bonds[i];
                    if (b == null || (b.status != "active" && b.status != "delinquent") || b.settled >= b.installments) continue;
                    bool iDebtor = b.debtorId == me && b.creditorId == other;
                    bool iCreditor = b.creditorId == me && b.debtorId == other;
                    if (!iDebtor && !iCreditor) continue;
                    int remaining = b.installments - b.settled;
                    long remDue = b.totalDueCents * remaining / b.installments;
                    net += iCreditor ? remDue : -remDue;
                }
            return net;
        }

        private static string Money(long cents) { return (cents / 100).ToString("N0"); }

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
