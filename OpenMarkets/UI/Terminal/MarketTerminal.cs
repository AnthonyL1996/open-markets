using System.Collections.Generic;
using ColossalFramework.UI;
using UnityEngine;

namespace OpenMarkets.UI.Terminal
{
    /// <summary>
    /// Tabbed "Open Markets" terminal: one corner button opens a draggable window whose tab bar switches
    /// between content bodies (Market + Balance today; Orders/Contracts/League/… when M4 lands). Replaces the
    /// former standalone MarketPanel + BalancePanel and their two corner buttons. The shell owns all window
    /// chrome (drag, close, Refresh, tab bar, title); each tab is an <see cref="ITabBody"/>. MAIN THREAD only
    /// (created in OnLevelLoaded). Idempotent across recompiles (reuses the prior component names).
    /// </summary>
    public static class MarketTerminal
    {
        // Reuse the OLD names so a stale UIView component from a recompile is found, not duplicated.
        private const string ToggleName = "OpenMarketsToggle";
        private const string PanelName = "OpenMarketsPanel";

        // Window sized for the widest tab (the market grid: NameCol 104 + 8*Cell 58 + scroll). Balance is
        // narrower; both share this window now.
        private const float Width = 600f;
        private const float Height = 460f;
        // Two-level tab bar: a GROUP row (Markets/Trade/League) at GroupBarY, then the active group's TAB row at
        // TabBarY; content starts below both. Grouping keeps ~12 tabs readable instead of one shrunk-to-fit row.
        private const float GroupBarY = 40f;
        private const float TabBarY = 66f;
        private const float ContentY = 94f;   // below title (y≈12) + group row (40) + tab row (66)

        private const float StatusH = 18f;     // bottom strip showing the player's online identity

        private static UIButton _toggle;
        private static UIPanel _panel;
        private static UILabel _title;
        private static UILabel _status;                                   // persistent identity/league line (4.2)
        private static UIButton _switchLeague;                            // cycle active league (4.7)
        private static UIPanel _content;                                  // host the active body builds into
        private static readonly List<ITabBody> _tabs = new List<ITabBody>();
        private static readonly List<UIButton> _tabButtons = new List<UIButton>();   // the CURRENT group's tab buttons
        private static readonly List<int> _tabButtonIdx = new List<int>();            // parallel: button → global tab index
        private static readonly List<UIButton> _groupButtons = new List<UIButton>();
        private static readonly List<TabGroup> _groups = new List<TabGroup>();        // non-empty groups, in order
        private static int _active = -1;
        private static int _activeGroup = -1;

        // A named group of tabs shown together; one group button reveals that group's tab sub-row.
        private sealed class TabGroup
        {
            public readonly string Name;
            public readonly List<int> Tabs = new List<int>();   // indices into _tabs
            public TabGroup(string name) { Name = name; }
        }

        // The tab grouping, by TabLabel. A tab whose label isn't listed falls into the LAST group, so a newly-added
        // tab never disappears — it just lands under "League" until grouped explicitly.
        private static readonly string[] GroupNames = { "Markets", "Trade", "League" };
        private static readonly string[][] GroupLabels =
        {
            new[] { "Market", "Standings", "City" },
            new[] { "Trade", "Inventory", "Inbox", "Bonds" },
            new[] { "Members", "Balance", "Investments", "Projects", "Feed", "Chronicle" },
        };

        public static void Create()
        {
            UIView view = UIView.GetAView();
            if (view == null) return;

            // Already fully built this assembly-load → nothing to do (OnLevelLoaded can re-fire). Guard on the
            // terminal's OWN state, not on FindUIComponent-by-name: a stale component (old DLL / pre-refactor
            // panel) sharing a name must NOT satisfy the early return, or the tabbed terminal never builds.
            if (_panel != null && _content != null && _tabs.Count > 0) return;

            // Statics were reset (recompile) or a prior/old-DLL build left components behind: sweep ALL known
            // names (current + the pre-refactor MarketPanel/BalancePanel names) so we never re-attach to a stale
            // panel or duplicate buttons, then build fresh. CF UI belongs to Unity's main thread; Create() is
            // only called from level/settings UI callbacks, never from the simulation/worker side.
            SweepStale(view);

            _tabs.Clear();
            _tabButtons.Clear();
            _tabs.Add(new MarketTab());                                   // tab order = list order
            // League balance (net §/outstanding/reliability per leaguemate) replaces the offline NPC
            // balance-of-trade view for now; BalanceTab is kept (unregistered) so it's trivial to restore.
            _tabs.Add(new LeagueBalanceTab());
            // M4 progressive disclosure: the online-only tabs appear once the player has an online identity
            // (account + league). Gated on OnlineMode.IsActive — i.e. configured AND this loaded save is the bound
            // LEAGUE CITY ("one league city, no hijack"): a different save stays inert online, so we hide the
            // write-capable tabs (offer/accept/settle) so it can't act as the shared account. OnlineMode is set in
            // WirePriceSource just before this runs; OnIdentityChanged()/rebind rebuild the terminal so tabs appear
            // the moment a league is joined or this city is made the league city.
            if (OnlineMode.IsActive)
            {
                _tabs.Add(new TradeTab());
                _tabs.Add(new InventoryTab());      // tradeable stock in [trade] depots (M6 Phase 1)
                _tabs.Add(new InboxTab());
                _tabs.Add(new BondsTab());
                _tabs.Add(new MembersTab());
                _tabs.Add(new InvestmentsTab());   // full co-op transparency: league-wide active + history
                _tabs.Add(new ProjectsTab());      // co-op MEGAPROJECTS (Great Works, social slice 4)
                _tabs.Add(new StandingsTab());     // ranked leaderboards (league + global) + traveling titles
                _tabs.Add(new FeedTab());          // recent league activity stream (settlements, newest-first)
                _tabs.Add(new ChronicleTab());     // the league's persistent narrated saga (social slice 2)
                _tabs.Add(new CityTab());          // time-series graphs of each leaguemate's city stats
            }

            BuildToggle(view);
            BuildShell(view);
            SelectTab(0);
            Log.Info("Terminal created (" + _tabs.Count + " tab(s)).");   // validates refactor build + idempotency
        }

        // Destroy any leftover Open Markets UI components by name — the terminal's own (after a stat-reset
        // recompile) AND the pre-refactor MarketPanel/BalancePanel names, so an old-DLL straggler can't shadow
        // us and we never duplicate. FindUIComponent returns only one match; duplicate same-name roots can be
        // left by earlier partial builds, so sweep a snapshot of UIView's descendants (filtered to our known
        // root names — destroying a root also destroys its children) instead. Do not loop
        // Find+Destroy: Unity's Destroy is deferred, so the same stale component may remain findable this frame.
        private static void SweepStale(UIView view)
        {
            List<UIComponent> roots = new List<UIComponent>(view.GetComponentsInChildren<UIComponent>());
            int swept = 0;
            for (int i = 0; i < roots.Count; i++)
            {
                UIComponent c = roots[i];
                if (c != null && IsKnownRootName(c.name)) { Object.Destroy(c.gameObject); swept++; }
            }
            // A nonzero sweep on a recompile is expected (statics reset, GameObjects survived); a nonzero sweep
            // at first load would mean a stale/old-DLL straggler — either way the log makes idempotency visible.
            if (swept > 0) Log.Info("Terminal: swept " + swept + " stale UI component(s) before rebuild.");
            _toggle = null; _panel = null; _content = null; _title = null; _status = null; _switchLeague = null; _active = -1; _activeGroup = -1;
            _tabs.Clear(); _tabButtons.Clear(); _tabButtonIdx.Clear(); _groupButtons.Clear(); _groups.Clear();
        }

        private static bool IsKnownRootName(string name)
        {
            return name == ToggleName
                || name == PanelName
                || name == "OpenMarketsBalanceToggle"
                || name == "OpenMarketsBalancePanel";
        }

        public static void Destroy()
        {
            if (_panel != null) { Object.Destroy(_panel.gameObject); _panel = null; }
            if (_toggle != null) { Object.Destroy(_toggle.gameObject); _toggle = null; }
            _tabs.Clear();
            _tabButtons.Clear();
            _tabButtonIdx.Clear();
            _groupButtons.Clear();
            _groups.Clear();
            _content = null;
            _title = null;
            _status = null;
            _switchLeague = null;
            _active = -1;
            _activeGroup = -1;
            Log.Info("Terminal destroyed.");
        }

        private static void BuildToggle(UIView view)
        {
            _toggle = (UIButton)view.AddUIComponent(typeof(UIButton));
            _toggle.name = ToggleName;
            _toggle.text = "Open Markets";
            _toggle.textScale = 0.8f;
            _toggle.normalBgSprite = "ButtonMenu";
            _toggle.hoveredBgSprite = "ButtonMenuHovered";
            _toggle.pressedBgSprite = "ButtonMenuPressed";
            _toggle.size = new Vector2(110f, 30f);
            _toggle.relativePosition = new Vector3(10f, 64f);
            _toggle.eventClicked += delegate
            {
                if (_panel == null) return;
                _panel.isVisible = !_panel.isVisible;
                if (_panel.isVisible) RefreshActive();
            };
        }

        private static void BuildShell(UIView view)
        {
            _panel = (UIPanel)view.AddUIComponent(typeof(UIPanel));
            _panel.name = PanelName;
            _panel.backgroundSprite = "MenuPanel2";
            _panel.size = new Vector2(Width, Height);
            _panel.relativePosition = new Vector3(130f, 64f);
            _panel.isVisible = false;

            _title = _panel.AddUIComponent<UILabel>();
            _title.textScale = 1.0f;
            _title.relativePosition = new Vector3(12f, 12f);

            // AddUIComponent<T>() returns a live CF component immediately; set size/target after the panel has
            // dimensions so the drag handle's hit area matches the visible chrome in Unity 5.6.
            UIDragHandle drag = _panel.AddUIComponent<UIDragHandle>();
            drag.target = _panel;
            drag.size = new Vector2(_panel.width - 180f, 36f);   // leaves room for Refresh + close at top-right
            drag.relativePosition = Vector3.zero;

            UIButton refresh = ChromeButton("Refresh", _panel.width - 104f, 10f, 66f);
            refresh.eventClicked += delegate { RefreshActive(); };

            UIButton close = ChromeButton("X", _panel.width - 32f, 10f, 28f);
            close.eventClicked += delegate { if (_panel != null) _panel.isVisible = false; };

            // Two-level tab bar: compute the (non-empty) groups and render a GROUP button row here; the active
            // group's tab sub-row is built lazily by SelectTab, so switching groups swaps only the sub-row. This
            // keeps ~12 tabs readable instead of one shrunk-to-fit row of slivers.
            BuildGroups();
            const float gMargin = 12f, gGap = 6f;
            float gStep = Mathf.Min(140f, (Width - gMargin * 2f) / Mathf.Max(1, _groups.Count));
            float gW = gStep - gGap;
            float gx = gMargin;
            for (int g = 0; g < _groups.Count; g++)
            {
                int gi = g;                                              // loop-local for the closure
                UIButton gb = ChromeButton(_groups[g].Name, gx, GroupBarY, gW);
                gb.textScale = 0.8f;
                gb.eventClicked += delegate { SelectGroup(gi); };
                _groupButtons.Add(gb);
                gx += gStep;
            }

            // Content host — every body builds into this once; SelectTab toggles which is visible. Leave a
            // bottom strip for the identity/league status line.
            _content = _panel.AddUIComponent<UIPanel>();
            _content.relativePosition = new Vector3(8f, ContentY);
            _content.size = new Vector2(_panel.width - 16f, _panel.height - ContentY - 8f - StatusH);

            // Persistent identity line: who you are + which league you're in, regardless of the active tab or
            // the price-feed toggle (4.2). Shown offline too, as a hint to configure in Options.
            _status = _panel.AddUIComponent<UILabel>();
            _status.textScale = 0.7f;
            _status.textColor = new Color32(150, 170, 200, 255);
            _status.relativePosition = new Vector3(12f, _panel.height - StatusH - 2f);

            // League switcher (4.7): cycles to the next league the account is in. Hidden unless there are at
            // least two leagues (visibility synced in UpdateStatus, since the league list arrives async).
            _switchLeague = ChromeButton("Switch league", _panel.width - 132f, _panel.height - StatusH - 4f, 120f);
            _switchLeague.textScale = 0.65f;
            _switchLeague.isVisible = false;
            _switchLeague.eventClicked += delegate { SwitchLeague(); };

            for (int i = 0; i < _tabs.Count; i++)
            {
                _tabs[i].Build(_content, _content.size);
                _tabs[i].SetVisible(false);
            }
        }

        private static UIButton ChromeButton(string text, float x, float y, float w)
        {
            UIButton b = _panel.AddUIComponent<UIButton>();
            b.text = text;
            b.textScale = 0.7f;
            b.normalBgSprite = "ButtonMenu";
            b.hoveredBgSprite = "ButtonMenuHovered";
            b.size = new Vector2(w, 24f);
            b.relativePosition = new Vector3(x, y);
            return b;
        }

        // Compute the non-empty groups from the current _tabs, in GroupNames order (unlisted label → last group).
        private static void BuildGroups()
        {
            _groups.Clear();
            TabGroup[] gs = new TabGroup[GroupNames.Length];
            for (int g = 0; g < GroupNames.Length; g++) gs[g] = new TabGroup(GroupNames[g]);
            for (int t = 0; t < _tabs.Count; t++)
            {
                string label = _tabs[t].TabLabel;
                int target = GroupNames.Length - 1;   // default: the last group
                for (int g = 0; g < GroupLabels.Length; g++)
                    for (int k = 0; k < GroupLabels[g].Length; k++)
                        if (GroupLabels[g][k] == label) target = g;
                gs[target].Tabs.Add(t);
            }
            for (int g = 0; g < gs.Length; g++)
                if (gs[g].Tabs.Count > 0) _groups.Add(gs[g]);
        }

        // (Re)build the tab-button sub-row for one group, destroying the previous group's buttons.
        private static void BuildTabButtons(int groupIdx)
        {
            for (int i = 0; i < _tabButtons.Count; i++)
                if (_tabButtons[i] != null) Object.Destroy(_tabButtons[i].gameObject);
            _tabButtons.Clear();
            _tabButtonIdx.Clear();
            if (_panel == null || groupIdx < 0 || groupIdx >= _groups.Count) return;
            List<int> tabs = _groups[groupIdx].Tabs;
            const float margin = 12f, gap = 6f;
            float step = Mathf.Min(110f, (Width - margin * 2f) / Mathf.Max(1, tabs.Count));
            float w = step - gap;
            float scale = w >= 84f ? 0.7f : 0.62f;
            float x = margin;
            for (int j = 0; j < tabs.Count; j++)
            {
                int idx = tabs[j];                                       // loop-local for the closure
                UIButton tab = ChromeButton(_tabs[idx].TabLabel, x, TabBarY, w);
                tab.textScale = scale;
                tab.eventClicked += delegate { SelectTab(idx); };
                _tabButtons.Add(tab);
                _tabButtonIdx.Add(idx);
                x += step;
            }
        }

        // The index of the group that owns tab i (0 as a safe default — every tab is grouped, so this is reached
        // only transiently before the groups are built).
        private static int GroupOf(int tab)
        {
            for (int g = 0; g < _groups.Count; g++)
                if (_groups[g].Tabs.Contains(tab)) return g;
            return 0;
        }

        // Click a group button: keep the active tab if it's already in this group, else jump to the group's first.
        private static void SelectGroup(int g)
        {
            if (g < 0 || g >= _groups.Count) return;
            List<int> tabs = _groups[g].Tabs;
            SelectTab(tabs.Contains(_active) ? _active : tabs[0]);
        }

        private static void SelectTab(int i)
        {
            if (i < 0 || i >= _tabs.Count) return;
            _active = i;
            // Ensure the sub-row shows this tab's group — rebuild the tab buttons only when the group changes.
            int g = GroupOf(i);
            if (g != _activeGroup) { _activeGroup = g; BuildTabButtons(g); }
            for (int gi = 0; gi < _groupButtons.Count; gi++)
                _groupButtons[gi].normalBgSprite = (gi == _activeGroup) ? "ButtonMenuPressed" : "ButtonMenu";
            for (int t = 0; t < _tabs.Count; t++) _tabs[t].SetVisible(t == i);
            for (int b = 0; b < _tabButtons.Count; b++)
                _tabButtons[b].normalBgSprite = (_tabButtonIdx[b] == i) ? "ButtonMenuPressed" : "ButtonMenu";  // active highlight
            if (Settings.IsDebugLogging) Log.Info("Terminal: tab → " + _tabs[i].TabLabel);
            RefreshActive();
        }

        // Rebuild the active body from current data and sync the shell title (the body's Title is computed live).
        private static void RefreshActive()
        {
            UpdateStatus();
            if (_active < 0 || _active >= _tabs.Count) return;
            _tabs[_active].Refresh();
            if (_title != null) _title.text = _tabs[_active].Title;
        }

        // Identity/league line: friendly name (or trimmed id) + league name (or trimmed id). Recomputed live so
        // it reflects a name set or a league joined this session without a reload.
        private static void UpdateStatus()
        {
            if (_status == null) return;
            if (!Settings.IsOnlineConfigured)
            {
                _status.text = "Offline — set account + league in Options";
                return;
            }
            if (!OnlineMode.IsActive)
            {
                // Configured, but this loaded save isn't the bound league city — it's inert online so it can't
                // overwrite your league city. Options → "Make the loaded city my league city" to switch.
                _status.text = "This save isn't your league city — Options → make it your league city to play here";
                return;
            }
            string me = !string.IsNullOrEmpty(Settings.DisplayNameValue)
                ? Settings.DisplayNameValue : OnlineSync.ShortId(Settings.AccountIdValue);
            string lg = !string.IsNullOrEmpty(LeagueRoster.LeagueName)
                ? LeagueRoster.LeagueName : OnlineSync.ShortId(Settings.LeagueIdValue);
            _status.text = "You: " + me + "   |   League: " + lg;
            if (_switchLeague != null) _switchLeague.isVisible = MyLeagues.Count > 1;
        }

        // Move the active league to the next one the account belongs to, then re-wire services + rebuild the
        // terminal for the new league (OnIdentityChanged) so every tab reflects it. MAIN THREAD (UI click).
        private static void SwitchLeague()
        {
            string next = MyLeagues.NextLeagueId();
            if (string.IsNullOrEmpty(next) || next == Settings.LeagueIdValue) return;
            if (Settings.LeagueId != null) Settings.LeagueId.value = next;
            OpenMarkets.OpenMarketsLoading.OnIdentityChanged();
        }

        /// <summary>Called by a tab body after it changed its own content (e.g. embargo toggle) so the shell
        /// rebuilds the active tab AND re-syncs the title — the body can't reach the title itself. Matches the
        /// old panels, where every Rebuild() re-stamped the lifetime-net title.</summary>
        internal static void RequestRefresh() { RefreshActive(); }

        /// <summary>Called by <see cref="OpenMarkets.OnlineSync"/> after a background poll brings in new
        /// contract data. Redraws the active tab so an OPEN terminal stays live (a fresh offer appears without
        /// a manual Refresh). No-op when the window is hidden — the data is already cached server-side and the
        /// tab refetches on its next show. MAIN THREAD (OnlineSync's callback runs on the main thread).</summary>
        internal static void NotifyDataChanged()
        {
            if (_panel == null || !_panel.isVisible) return;
            RefreshActive();
        }
    }
}
