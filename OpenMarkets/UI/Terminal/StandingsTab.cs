using System.Collections.Generic;
using ColossalFramework.UI;
using OpenMarkets.Net;
using UnityEngine;

namespace OpenMarkets.UI.Terminal
{
    /// <summary>
    /// Standings tab: the league's ranked leaderboards (Net Worth, trade volume, reliability, the Deadbeat shame
    /// board, …) and the cross-league GLOBAL boards. A League/Global toggle picks the scope; within League a board
    /// selector picks which board to rank. Rows render "#rank  name  value" — league names resolve
    /// through <see cref="LeagueRoster.Display"/> so traveling titles show; global rows have no account id, so they
    /// use the server's anonymised handle plus a percentile + tier. Fetched on Refresh (OmApi), cached, drawn from
    /// the cache. MAIN THREAD. Read-only.
    /// </summary>
    internal sealed class StandingsTab : ITabBody
    {
        private const float RankCol = 44f;
        private const float NameCol = 300f;
        private const float ValueCol = 110f;
        private const float TierCol = 130f;   // global only: "{tier} · top {n}%"
        private const float Width = RankCol + NameCol + ValueCol + TierCol;

        private const float HeaderH = 60f;    // scope toggle (row 1) + board selector / caption (rows 2-3)

        private UIPanel _root;
        private UIScrollablePanel _grid;
        private UIButton _leagueBtn;
        private UIButton _globalBtn;
        private UIButton _jumpBtn;
        private UIComponent _myRow;   // the caller's own row in the current grid (null if not present), for "Jump to me"
        private UIPanel _selectorRow;          // hosts the per-board selector buttons (League view only)
        private readonly List<UIButton> _selectorBtns = new List<UIButton>();
        private UILabel _caption;

        private LeaderboardsDto _league;
        private GlobalLeaderboardsDto _global;
        private bool _showGlobal;
        private int _boardIdx;                  // selected league board (default 0 = netWorth)
        private bool _leagueLoading, _globalLoading;
        private bool _leagueFailed, _globalFailed;

        public string TabLabel { get { return "Standings"; } }

        public string Title { get { return _showGlobal ? "Open Markets — global standings" : "Open Markets — league standings"; } }

        public void Build(UIComponent host, Vector2 size)
        {
            _root = host.AddUIComponent<UIPanel>();
            _root.relativePosition = Vector3.zero;
            _root.size = size;
            _root.autoLayout = false;

            _leagueBtn = MakeButton(2f, 4f, 90f, "League");
            _leagueBtn.eventClicked += delegate { _showGlobal = false; SyncScope(); Refresh(); };
            _globalBtn = MakeButton(96f, 4f, 90f, "Global");
            _globalBtn.eventClicked += delegate { _showGlobal = true; SyncScope(); Refresh(); };

            // "Jump to me" scrolls the caller's own row into view (it can be far down a long board).
            _jumpBtn = MakeButton(size.x - 96f, 4f, 90f, "Jump to me");
            _jumpBtn.eventClicked += delegate { JumpToMe(); };

            _selectorRow = _root.AddUIComponent<UIPanel>();
            _selectorRow.relativePosition = new Vector3(2f, 32f);
            _selectorRow.size = new Vector2(size.x - 4f, 24f);
            _selectorRow.autoLayout = true;
            _selectorRow.autoLayoutDirection = LayoutDirection.Horizontal;
            _selectorRow.autoLayoutPadding = new RectOffset(0, 4, 0, 0);
            _selectorRow.wrapLayout = true;

            _caption = _root.AddUIComponent<UILabel>();
            _caption.autoSize = false;
            _caption.wordWrap = true;
            _caption.size = new Vector2(size.x - 6f, 16f);
            _caption.relativePosition = new Vector3(4f, HeaderH - 16f);
            _caption.textScale = 0.65f;
            _caption.textColor = UiKit.Dim;
            _caption.text = string.Empty;

            _grid = _root.AddUIComponent<UIScrollablePanel>();
            _grid.relativePosition = new Vector3(0f, HeaderH);
            _grid.size = new Vector2(size.x, size.y - HeaderH);
            _grid.autoLayout = true;
            _grid.autoLayoutDirection = LayoutDirection.Vertical;
            _grid.autoLayoutPadding = new RectOffset(0, 0, 0, 2);
            _grid.clipChildren = true;
            _grid.scrollWheelDirection = UIOrientation.Vertical;

            SyncScope();
        }

        public void SetVisible(bool on) { if (_root != null) _root.isVisible = on; }

        public void Refresh()
        {
            if (_grid == null) return;
            SyncScope();
            Rebuild();   // draw current cache (or loading/empty note) immediately

            if (_showGlobal)
            {
                if (_globalLoading) return;
                _globalLoading = true;
                OmApi.GetGlobalLeaderboards(delegate (bool ok, GlobalLeaderboardsDto dto)
                {
                    _globalLoading = false;
                    _globalFailed = !ok || dto == null;
                    if (!_globalFailed) _global = dto;
                    Rebuild();   // callback is on the main thread (OmHttp contract)
                });
            }
            else
            {
                if (_leagueLoading) return;
                _leagueLoading = true;
                OmApi.GetLeaderboards(delegate (bool ok, LeaderboardsDto dto)
                {
                    _leagueLoading = false;
                    _leagueFailed = !ok || dto == null;
                    if (!_leagueFailed)
                    {
                        _league = dto;
                        LeagueRoster.SetTitles(dto.titles);   // surface traveling titles everywhere
                    }
                    SyncScope();   // boards may have arrived → (re)build the selector
                    Rebuild();
                });
            }
        }

        // Sync the scope buttons + the board selector to the current state. The selector exists only in the League
        // view and is (re)built from the cached boards so its labels match the server's.
        private void SyncScope()
        {
            if (_leagueBtn != null) _leagueBtn.normalBgSprite = _showGlobal ? "ButtonMenu" : "ButtonMenuPressed";
            if (_globalBtn != null) _globalBtn.normalBgSprite = _showGlobal ? "ButtonMenuPressed" : "ButtonMenu";
            BuildSelector();
        }

        // (Re)build the per-board selector buttons for the League view from the cached boards. Hidden in Global.
        private void BuildSelector()
        {
            if (_selectorRow == null) return;
            for (int i = 0; i < _selectorBtns.Count; i++)
            {
                _selectorRow.RemoveUIComponent(_selectorBtns[i]);
                Object.Destroy(_selectorBtns[i].gameObject);
            }
            _selectorBtns.Clear();

            _selectorRow.isVisible = !_showGlobal;
            if (_showGlobal) return;

            BoardDto[] boards = _league != null ? _league.boards : null;
            if (boards == null || boards.Length == 0) return;
            if (_boardIdx >= boards.Length) _boardIdx = 0;

            for (int i = 0; i < boards.Length; i++)
            {
                int idx = i;   // capture a loop-local; never close over the loop variable
                BoardDto b = boards[i];
                string label = (b != null && !string.IsNullOrEmpty(b.label)) ? b.label : (b != null ? b.id : "?");
                UIButton btn = _selectorRow.AddUIComponent<UIButton>();
                btn.text = label;
                btn.textScale = 0.6f;
                btn.normalBgSprite = i == _boardIdx ? "ButtonMenuPressed" : "ButtonMenu";
                btn.hoveredBgSprite = "ButtonMenuHovered";
                btn.autoSize = false;
                btn.size = new Vector2(Mathf.Max(56f, label.Length * 6.5f + 12f), 22f);
                btn.eventClicked += delegate { _boardIdx = idx; SyncScope(); Rebuild(); };
                _selectorBtns.Add(btn);
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
            _myRow = null;   // re-found below as the rows are rebuilt

            if (_showGlobal) RebuildGlobal();
            else RebuildLeague();

            if (_jumpBtn != null) _jumpBtn.isEnabled = _myRow != null;
        }

        // Scroll the caller's own row into view. ScrollIntoView takes a DIRECT child of the scrollable grid; our
        // rows are direct children, so the captured _myRow works as-is. MAIN THREAD.
        private void JumpToMe()
        {
            if (_grid != null && _myRow != null) _grid.ScrollIntoView(_myRow);
        }

        private void RebuildLeague()
        {
            _caption.text = string.Empty;
            BoardDto[] boards = _league != null ? _league.boards : null;
            if (boards == null || boards.Length == 0)
            {
                EmptyNote(_leagueLoading, _leagueFailed, "No standings yet.");
                return;
            }
            if (_boardIdx >= boards.Length) _boardIdx = 0;
            BoardDto board = boards[_boardIdx];
            if (board == null) { EmptyNote(false, false, "No standings yet."); return; }

            // A one-line caption for the shame board.
            if (board.id == "deadbeat")
                _caption.text = "Deadbeat = most missed payments (shame board).";

            UIPanel header = NewRow();
            UiKit.Cellate(header, RankCol, "#", UiKit.Head, UIHorizontalAlignment.Right);
            UiKit.Cellate(header, NameCol, board.label ?? "City", UiKit.Head, UIHorizontalAlignment.Left);
            UiKit.Cellate(header, ValueCol, "Value", UiKit.Head, UIHorizontalAlignment.Right);

            BoardRowDto[] rows = board.rows;
            if (rows == null || rows.Length == 0)
            {
                UiKit.Cellate(NewRow(), Width, "No standings yet.", UiKit.Dim, UIHorizontalAlignment.Left);
                return;
            }

            string me = Settings.AccountIdValue;
            for (int i = 0; i < rows.Length; i++)
            {
                BoardRowDto r = rows[i];
                if (r == null) continue;
                bool isMe = !string.IsNullOrEmpty(me) && r.accountId == me;
                UIPanel row = NewRow();
                if (isMe) _myRow = row;
                Color32 col = isMe ? UiKit.Head : UiKit.Flat;
                UiKit.Cellate(row, RankCol, "#" + r.rank, UiKit.Dim, UIHorizontalAlignment.Right);
                UiKit.Cellate(row, NameCol, LeagueRoster.Display(r.accountId), col, UIHorizontalAlignment.Left);
                UiKit.Cellate(row, ValueCol, FormatValue(board.id, r.value), col, UIHorizontalAlignment.Right);
            }
        }

        private void RebuildGlobal()
        {
            GlobalBoardDto[] boards = _global != null ? _global.boards : null;
            if (boards == null || boards.Length == 0)
            {
                _caption.text = string.Empty;
                EmptyNote(_globalLoading, _globalFailed, "No global standings yet.");
                return;
            }
            _caption.text = "Cross-league standings — your row is highlighted.";

            // Global shows every board stacked (only three), each with its own header.
            for (int b = 0; b < boards.Length; b++)
            {
                GlobalBoardDto board = boards[b];
                if (board == null) continue;
                if (b > 0) NewRow();   // spacer between boards

                UIPanel header = NewRow();
                UiKit.Cellate(header, RankCol, "#", UiKit.Head, UIHorizontalAlignment.Right);
                UiKit.Cellate(header, NameCol, board.label ?? "City", UiKit.Head, UIHorizontalAlignment.Left);
                UiKit.Cellate(header, ValueCol, "Value", UiKit.Head, UIHorizontalAlignment.Right);
                UiKit.Cellate(header, TierCol, "Tier", UiKit.Head, UIHorizontalAlignment.Left);

                GlobalBoardRowDto[] rows = board.rows;
                if (rows == null || rows.Length == 0)
                {
                    UiKit.Cellate(NewRow(), Width, "No standings yet.", UiKit.Dim, UIHorizontalAlignment.Left);
                    continue;
                }
                for (int i = 0; i < rows.Length; i++)
                {
                    GlobalBoardRowDto r = rows[i];
                    if (r == null) continue;
                    UIPanel row = NewRow();
                    if (r.you) _myRow = row;
                    Color32 col = r.you ? UiKit.Accent : UiKit.Flat;
                    string name = !string.IsNullOrEmpty(r.displayName) ? r.displayName : "anonymous";
                    if (r.you) name = name + "  (you)";
                    UiKit.Cellate(row, RankCol, "#" + r.rank, UiKit.Dim, UIHorizontalAlignment.Right);
                    UiKit.Cellate(row, NameCol, name, col, UIHorizontalAlignment.Left);
                    UiKit.Cellate(row, ValueCol, FormatValue(board.id, r.value), col, UIHorizontalAlignment.Right);
                    string tier = (!string.IsNullOrEmpty(r.tier) ? r.tier : "—")
                        + " · top " + (100 - r.percentile) + "%";
                    UiKit.Cellate(row, TierCol, tier, UiKit.Dim, UIHorizontalAlignment.Left);
                }
            }
        }

        private void EmptyNote(bool loading, bool failed, string emptyText)
        {
            string note = loading ? "Loading..."
                : failed ? "Couldn't reach the server — try Refresh."
                : emptyText;
            UiKit.Cellate(NewRow(), Width, note, UiKit.Dim, UIHorizontalAlignment.Left);
        }

        // Value formatting by board id. § boards are CENTS; %-boards append "%"; the rest are plain counts.
        private static string FormatValue(string boardId, long value)
        {
            switch (boardId)
            {
                case "netWorth":
                case "patron":
                case "globalNetWorth":
                    return "§" + (value / 100).ToString("N0");
                case "reliability":
                case "happiness":
                case "globalReliability":
                    return value + "%";
                default: // population, tradeVolume, marketMover, deadbeat, phoenix, globalTradeVolume
                    return value.ToString("N0");
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

        private UIButton MakeButton(float x, float y, float w, string text)
        {
            UIButton b = _root.AddUIComponent<UIButton>();
            b.text = text;
            b.textScale = 0.7f;
            b.normalBgSprite = "ButtonMenu";
            b.hoveredBgSprite = "ButtonMenuHovered";
            b.pressedBgSprite = "ButtonMenuPressed";
            b.size = new Vector2(w, 24f);
            b.relativePosition = new Vector3(x, y);
            return b;
        }
    }
}
