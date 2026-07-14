using System;
using System.Collections.Generic;
using System.Globalization;
using ColossalFramework.UI;
using OpenMarkets.Net;
using UnityEngine;

namespace OpenMarkets.UI.Terminal
{
    /// <summary>
    /// Investments tab: full co-op transparency for the M8 investment office. Two sections — ACTIVE (every live
    /// issuer→grantee grant in the league right now, with the buff details) and HISTORY (every investment ever made,
    /// from the durable settlement-event log, so it survives buff expiry — money trail only). Fetched on Refresh via
    /// <see cref="OmApi.GetInvestments"/>; the callback (main thread) caches + rebuilds. MAIN THREAD, read-only.
    /// </summary>
    internal sealed class InvestmentsTab : ITabBody
    {
        private const float Width = 560f;

        private UIScrollablePanel _grid;
        private InvestmentsDto _data;
        private bool _loading;
        private bool _failed;

        public string TabLabel { get { return "Investments"; } }
        public string Title { get { return "Open Markets — co-op investments"; } }

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
            if (_loading) return;
            _loading = true;
            OmApi.GetInvestments(delegate (bool ok, InvestmentsDto dto)
            {
                _loading = false;
                _failed = !ok || dto == null;
                if (!_failed) _data = dto;
                Rebuild(); // main thread (OmHttp contract)
            });
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

            string me = Settings.AccountIdValue;

            // ---- Active (league-wide) ----
            Head("Active co-op effects (league):");
            CityEffectDto[] active = _data != null ? _data.active : null;
            if (active == null || active.Length == 0)
            {
                Note(_loading ? "Loading..." : _failed ? "Couldn't reach the server — try Refresh." : "None active.");
            }
            else
            {
                for (int i = 0; i < active.Length; i++)
                {
                    CityEffectDto e = active[i];
                    if (e == null) continue;
                    string line;
                    if (e.kind == "marketShield" || e.kind == "priceEdge")
                    {
                        line = Name(e.granteeId, me) + "   " + CoopBuff.TradeRewardText(e)
                            + "   (" + e.ticksRemaining + "d left)";
                    }
                    else
                    {
                        line = Name(e.issuerId, me) + "  →  " + Name(e.granteeId, me)
                            + "   §" + (e.costCents / 100).ToString("N0")
                            + "   (+" + e.demandBoost + " " + CoopBuff.KindLabel(e.demandKind) + " demand, " + e.ticksRemaining + "d left)";
                    }
                    Row(line, e.issuerId == me || e.granteeId == me ? UiKit.Up : UiKit.Flat);
                }
            }

            Row(string.Empty, UiKit.Dim); // spacer

            // ---- History (durable, incl. expired) ----
            Head("History (all investments ever):");
            InvestEventDto[] hist = _data != null ? _data.history : null;
            if (hist == null || hist.Length == 0)
            {
                Note("No investments yet.");
            }
            else
            {
                for (int i = 0; i < hist.Length; i++)
                {
                    InvestEventDto h = hist[i];
                    if (h == null) continue;
                    string line = When(h.created) + "   " + Name(h.payerId, me) + "  →  " + Name(h.receiverId, me)
                        + "   §" + (h.cents / 100).ToString("N0");
                    Row(line, h.payerId == me || h.receiverId == me ? UiKit.Flat : UiKit.Dim);
                }
            }
        }

        // A friendly name with a "(you)" marker for the local account.
        private static string Name(string id, string me)
        {
            string n = LeagueRoster.Display(id);
            return id == me ? n + " (you)" : n;
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
