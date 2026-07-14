using CitiesHarmony.API;
using ColossalFramework.UI;
using ICities;
using OpenMarkets.Net;

namespace OpenMarkets
{
    /// <summary>
    /// Mod entry point. The game's PluginManager discovers this via the IUserMod interface.
    /// Harmony references are deliberately kept OUT of this class (they live in Patcher) so the
    /// mod still loads if Harmony isn't ready yet — boformer's recommended bootstrapper pattern.
    /// </summary>
    public sealed class Mod : IUserMod
    {
        public const string Version = "0.1.0";

        // Holds the join code typed in Options until the player clicks "Join" (textfield → button).
        private string _pendingJoinCode = string.Empty;

        public string Name => "Open Markets " + Version;
        public string Description =>
            "Dynamic commodity market: trade resources at a live, league-shared price index.";

        public void OnEnabled()
        {
            Log.Info("OnEnabled");
            Settings.Init();
            HarmonyHelper.DoOnHarmonyReady(() => Patcher.PatchAll());
        }

        public void OnDisabled()
        {
            Log.Info("OnDisabled");
            // NFR-3: if a tax-lock is active, restore the player's real rates BEFORE we unpatch — m_taxRates is
            // baked into the vanilla save, so removing the mod must not strand the city at the austerity rate.
            TaxLock.RestoreOnDisable();
            // NFR-3 (M8 lever #2): the budget cap is also baked into the vanilla save (m_serviceBudgetDay/Night), so
            // restore the player's real budgets BEFORE we unpatch — removing the mod must not strand services low.
            BudgetLock.RestoreOnDisable();
            // The M8 demand slump (lever #1) is transient (no vanilla state to restore — it stops being applied once
            // the extension is no longer called); just drop the flag so a re-enable without a reload starts clean.
            DemandLever.Clear();
            // Stop the online price-feed worker (if any) so it can't outlive the mod being switched off /
            // recompiled — OnDisabled can fire without an OnLevelUnloading.
            OpenMarketsLoading.StopOnlineFeed();
            if (HarmonyHelper.IsHarmonyInstalled)
            {
                Patcher.UnpatchAll();
            }
        }

        public void OnSettingsUI(UIHelperBase helper)
        {
            UIHelperBase group = helper.AddGroup("Open Markets");

            group.AddCheckbox(
                "Charge the treasury for imports (default: on — net = exports − imports; required for online play)",
                Settings.IsChargeImports,
                isChecked => { if (Settings.ChargeImports != null) Settings.ChargeImports.value = isChecked; });

            group.AddCheckbox(
                "Chirper alerts when a price-swing event starts (on by default)",
                Settings.IsChirperAlerts,
                isChecked => { if (Settings.ChirperAlerts != null) Settings.ChirperAlerts.value = isChecked; });

            // Price-feed endpoint. Persist as typed (autoUpdate writes immediately); re-wire the live feed only
            // on submit (Enter) so we don't restart the worker per keystroke. Main-thread callbacks — WirePriceSource
            // owns the worker and runs here, so no sim marshaling. Blank falls back to the default local server.
            // The "in effect" label below resolves blank/scheme-less input to the actual URL online calls will
            // hit (EndpointValue), and refreshes on each keystroke/submit so the player always sees the real target.
            UILabel endpointInEffect = AddStatusLabel(group);
            SetServerInEffect(endpointInEffect);
            group.AddTextfield(
                "Server base URL (e.g. http://localhost:8080; blank = default local server; press Enter to apply)",
                Settings.EndpointValue,
                text =>
                {
                    if (Settings.Endpoint != null) Settings.Endpoint.value = text;
                    SetServerInEffect(endpointInEffect);           // live-update the resolved server as you type
                },
                text =>
                {
                    if (Settings.Endpoint != null) Settings.Endpoint.value = text;
                    SetServerInEffect(endpointInEffect);
                    OpenMarketsLoading.OnOnlineSettingChanged();   // rebuild the feed against the new endpoint
                });

            group.AddCheckbox(
                "Verbose per-trade logging (debug; off by default — the daily summary is always logged)",
                Settings.IsDebugLogging,
                isChecked => { if (Settings.DebugLogging != null) Settings.DebugLogging.value = isChecked; });

            // Online account / league setup. The group title shows status at open time; a live status label
            // (added below) gives immediate feedback on each async action (4.1) — previously results only hit
            // the log, so successful clicks looked like "nothing happened".
            string status = Settings.IsOnlineConfigured
                ? "account + league set"
                : (string.IsNullOrEmpty(Settings.AccountIdValue) ? "no account yet" : "account set, no league");
            UIHelperBase online = helper.AddGroup("Open Markets — online account (" + status + ")");
            UILabel identity = AddStatusLabel(online);
            SetIdentityReadout(identity);                       // who you are + which league, visible here too (4.2)
            UILabel feedback = AddStatusLabel(online);

            online.AddButton("Create account (stores a secret on this PC)", () =>
            {
                SetStatus(feedback, "Creating account...");
                OmApi.CreateAccount((ok, dto) =>
                {
                    if (ok && dto != null)
                    {
                        if (Settings.AccountId != null) Settings.AccountId.value = dto.accountId;
                        if (Settings.AccountSecret != null) Settings.AccountSecret.value = dto.secret;
                        OpenMarketsLoading.OnIdentityChanged();   // rewire online services now that we have an account
                        SetStatus(feedback, "Account created: " + OnlineSync.ShortId(dto.accountId) + ". Now create or join a league.");
                    }
                    else SetStatus(feedback, "Account creation failed — check the Server base URL and that the server is reachable.");
                });
            });

            online.AddButton("Create a league (you become the owner)", () =>
            {
                SetStatus(feedback, "Creating league...");
                OmApi.CreateLeague("My League", (ok, dto) =>
                {
                    if (ok && dto != null)
                    {
                        if (Settings.LeagueId != null) Settings.LeagueId.value = dto.leagueId;
                        OpenMarketsLoading.OnIdentityChanged();   // league set → show the online tabs + start polling
                        SetStatus(feedback, "League created. Share this join code with friends: " + dto.joinCode);
                    }
                    else SetStatus(feedback, "League creation failed (create an account first?).");
                });
            });

            online.AddTextfield("Friend's join code", _pendingJoinCode,
                t => { _pendingJoinCode = t; },
                t => { _pendingJoinCode = t; });

            online.AddButton("Join league with the code above", () =>
            {
                SetStatus(feedback, "Joining league...");
                OmApi.JoinLeague(_pendingJoinCode, (ok, dto) =>
                {
                    if (ok && dto != null)
                    {
                        if (Settings.LeagueId != null) Settings.LeagueId.value = dto.leagueId;
                        OpenMarketsLoading.OnIdentityChanged();   // joined → show the online tabs + start polling
                        SetStatus(feedback, "Joined league " + OnlineSync.ShortId(dto.leagueId) + ". Open the Markets terminal to trade.");
                    }
                    else SetStatus(feedback, "Join failed — check the code and that you have an account.");
                });
            });

            // Display name leaguemates see instead of your opaque id (4.1 + roster/offer readability). Persisted
            // client-side so this field shows the current value; the button pushes it to the server.
            online.AddTextfield("Your display name (blank = your id)", Settings.DisplayNameValue,
                t => { if (Settings.DisplayName != null) Settings.DisplayName.value = t; },
                t => { if (Settings.DisplayName != null) Settings.DisplayName.value = t; });

            online.AddButton("Set display name", () =>
            {
                SetStatus(feedback, "Setting display name...");
                OmApi.SetDisplayName(Settings.DisplayNameValue, ok =>
                    SetStatus(feedback, ok
                        ? "Display name set."
                        : "Couldn't set name — create an account and check the server first."));
            });

            online.AddCheckbox(
                "Auto-settle contract installments each in-game day (on by default; off = settle from the Inbox)",
                Settings.IsAutoSettle,
                isChecked => { if (Settings.AutoSettle != null) Settings.AutoSettle.value = isChecked; });

            // "One league city, no hijack": only one loaded city per install acts in the league. The first city you
            // load auto-binds; a different save stays offline in the league (so it can't overwrite your league city)
            // until you press this to deliberately switch. Only meaningful with a city loaded.
            online.AddButton("Make the loaded city my league city (switch which save plays in the league)", () =>
            {
                OpenMarketsLoading.BindLoadedCityToLeague();
                SetStatus(feedback, "The loaded city is now your league city — its profile + economy sync to the league. " +
                    "(Load a city first if nothing changed.)");
            });

            // M4 SPIKE: prove HTTPS/TLS-1.2 works from this build before building the online layer.
            // Result is logged to output_log.txt under [OpenMarkets] (UnityWebRequest, main thread).
            UIHelperBase diagnostics = helper.AddGroup("Open Markets — diagnostics (M4 spike)");
            diagnostics.AddButton("Run TLS smoke test (logs HTTPS reachability to output_log)",
                () => TlsSmokeTest.Run());
        }

        // Add an updatable status line to an options group. The UIHelperBase API exposes no label handle, so we
        // reach the group's root component (UIHelper.self) and attach our own UILabel. Returns null if the cast
        // fails — callers then degrade to log-only feedback (SetStatus is null-safe). MAIN THREAD (options UI).
        private static UILabel AddStatusLabel(UIHelperBase helper)
        {
            UIHelper h = helper as UIHelper;
            UIComponent root = h != null ? h.self as UIComponent : null;
            if (root == null) return null;
            UILabel label = root.AddUIComponent<UILabel>();
            label.autoSize = true;   // a width-less label can collapse under the group's autolayout
            label.wordWrap = true;
            label.textScale = 0.85f;
            label.padding = new UnityEngine.RectOffset(0, 0, 6, 4);
            label.text = " ";
            return label;
        }

        // Update the on-screen status (if we have a label) and log it, so feedback shows both in Options and in
        // output_log. Called from async HTTP callbacks — all on the main thread (OmApi contract).
        private static void SetStatus(UILabel label, string text)
        {
            if (label != null) label.text = text;
            Log.Info("Online: " + text);
        }

        // Show the player's current online identity in Options (mirrors the terminal's status strip) so the
        // account + league are visible here, not only inside the in-game terminal. The league's friendly NAME is
        // only known after a successful roster fetch (LeagueRoster); before that, show the short league id.
        private static void SetIdentityReadout(UILabel label)
        {
            if (label == null) return;
            string acc = !string.IsNullOrEmpty(Settings.DisplayNameValue)
                ? Settings.DisplayNameValue
                : (!string.IsNullOrEmpty(Settings.AccountIdValue) ? OnlineSync.ShortId(Settings.AccountIdValue) : "—");
            string lg = !string.IsNullOrEmpty(LeagueRoster.LeagueName)
                ? LeagueRoster.LeagueName
                : (!string.IsNullOrEmpty(Settings.LeagueIdValue) ? OnlineSync.ShortId(Settings.LeagueIdValue) : "none");
            label.text = "You: " + acc + "   |   League: " + lg;
        }

        // Show which server online calls will actually hit. EndpointValue resolves a blank field to the default
        // local server and prefixes a missing scheme, so this is the real URL — not just the raw text typed.
        private static void SetServerInEffect(UILabel label)
        {
            if (label == null) return;
            string resolved = Settings.EndpointValue;
            bool isDefault = resolved == Settings.DefaultEndpoint;
            label.text = "Server in effect: " + resolved + (isDefault ? "  (default)" : string.Empty);
        }
    }
}
