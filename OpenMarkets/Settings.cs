using ColossalFramework;

namespace OpenMarkets
{
    /// <summary>
    /// Persisted mod options, backed by a ColossalFramework settings file (survives across saves, not
    /// per-city). The settings file MUST be registered before any <see cref="SavedBool"/> is constructed,
    /// so values are created lazily in <see cref="Init"/> (called from <see cref="Mod.OnEnabled"/>) rather
    /// than via static field initializers.
    /// </summary>
    public static class Settings
    {
        public const string FileName = "OpenMarketsSettings";

        /// <summary>Verbose per-trade console logging. Off by default (it's a per-transfer hot path).</summary>
        public static SavedBool DebugLogging { get; private set; }

        /// <summary>Deduct the treasury for imports (FetchResource). ON by default (trade is two-sided:
        /// net = exports − imports). MANDATORY for online play — see <see cref="IsChargeImports"/>.</summary>
        public static SavedBool ChargeImports { get; private set; }

        /// <summary>Post a Chirper alert when a price-swing event starts on a commodity. On by default.</summary>
        public static SavedBool ChirperAlerts { get; private set; }

        /// <summary>The backend BASE URL (e.g. <c>http://localhost:8080</c>). All online calls append paths
        /// (<c>/prices</c>, <c>/report/batch</c>, <c>/contracts</c>, …). Defaults to <see cref="DefaultEndpoint"/>.
        /// Read via <see cref="EndpointValue"/>; takes effect on the next poll.</summary>
        public static SavedString Endpoint { get; private set; }
        /// <summary>Default server endpoint, used when the Endpoint option is blank — the official hosted instance.
        /// HTTPS so account tokens aren't sent in clear (the host must terminate TLS, e.g. a reverse proxy/cert).
        /// For LOCAL testing, set the Options → "Server base URL" field to <c>http://localhost:8080</c>.</summary>
        public const string DefaultEndpoint = "https://cstrading.udonitus.com";

        /// <summary>Online identity (cross-save, per installation). The secret is shown by the server once at
        /// account creation and stored here; the settings file is plaintext on disk (same posture as the
        /// endpoint). LeagueId is the friend group the city currently trades in.</summary>
        public static SavedString AccountId { get; private set; }
        public static SavedString AccountSecret { get; private set; }
        public static SavedString LeagueId { get; private set; }

        /// <summary>Optional friendly name shown to leaguemates instead of the opaque account id. Stored
        /// client-side so the Options field can show it; the server is the source of truth for what others see.</summary>
        public static SavedString DisplayName { get; private set; }

        /// <summary>Which loaded city is THE league city for this install ("one league city, no hijack"). Holds the
        /// per-save <see cref="CityIdentity"/> token of the bound city; the first configured city to load auto-binds.
        /// A loaded save whose token differs goes inert online so it can't overwrite the bound city — see
        /// <see cref="OpenMarketsLoading"/>. Global (per-install), like the account credentials.</summary>
        public static SavedString BoundCityToken { get; private set; }

        /// <summary>Auto-settle one due installment on each active contract at every in-game day rollover
        /// (Phase B). On by default — the contract model assumes legs settle over time. Off = settle manually
        /// from the Inbox. Booking is idempotent either way.</summary>
        public static SavedBool AutoSettle { get; private set; }

        public static void Init()
        {
            if (GameSettings.FindSettingsFileByName(FileName) == null)
                GameSettings.AddSettingsFile(new SettingsFile { fileName = FileName });

            DebugLogging = new SavedBool("DebugLogging", FileName, false, true);
            ChargeImports = new SavedBool("ChargeImports", FileName, true, true);
            ChirperAlerts = new SavedBool("ChirperAlerts", FileName, true, true);
            Endpoint = new SavedString("Endpoint", FileName, DefaultEndpoint, true);
            AccountId = new SavedString("AccountId", FileName, string.Empty, true);
            AccountSecret = new SavedString("AccountSecret", FileName, string.Empty, true);
            LeagueId = new SavedString("LeagueId", FileName, string.Empty, true);
            DisplayName = new SavedString("DisplayName", FileName, string.Empty, true);
            BoundCityToken = new SavedString("BoundCityToken", FileName, string.Empty, true);
            AutoSettle = new SavedBool("AutoSettle", FileName, true, true);
        }

        // Null-safe reads (in case anything queries before Init on a cold start).
        public static bool IsDebugLogging { get { return DebugLogging != null && DebugLogging.value; } }
        // Charging for imports is MANDATORY under online play — a shared price feed only balances if both
        // legs settle in cash — so online mode forces it on regardless of the saved toggle. Offline, it
        // honours the user's checkbox (default on). See <see cref="OnlineMode.IsActive"/>.
        public static bool IsChargeImports
        {
            get { return OnlineMode.IsActive || (ChargeImports != null && ChargeImports.value); }
        }
        public static bool IsChirperAlerts { get { return ChirperAlerts != null && ChirperAlerts.value; } }
        public static bool IsAutoSettle { get { return AutoSettle == null || AutoSettle.value; } } // default on
        /// <summary>Configured feed endpoint (trimmed), or the default local dev server if unset/blank/whitespace.
        /// Trim is manual: net35 has no <c>string.IsNullOrWhiteSpace</c> (that's .NET 4.0+).</summary>
        public static string EndpointValue
        {
            get
            {
                string v = Endpoint != null ? Endpoint.value : null;
                if (!string.IsNullOrEmpty(v)) v = v.Trim().TrimEnd('/');
                if (string.IsNullOrEmpty(v)) return DefaultEndpoint;
                // UnityWebRequest needs an absolute URL with a scheme. A friend typically shares a bare
                // "host:port"; auto-prefix http:// so a scheme-less endpoint just works (was a silent failure).
                if (v.IndexOf("://") < 0) v = "http://" + v;
                return v;
            }
        }

        public static string AccountIdValue { get { return AccountId != null ? AccountId.value : string.Empty; } }
        public static string DisplayNameValue { get { return DisplayName != null ? DisplayName.value : string.Empty; } }
        public static string AccountSecretValue { get { return AccountSecret != null ? AccountSecret.value : string.Empty; } }
        public static string LeagueIdValue { get { return LeagueId != null ? LeagueId.value : string.Empty; } }
        public static string BoundCityTokenValue { get { return BoundCityToken != null ? BoundCityToken.value : string.Empty; } }

        /// <summary>True when we have an account and a league — i.e. online calls can authenticate. M9: ALL online
        /// HTTP (the /prices feed poll, the net-volume report, contracts) + OnlineMode (import-charging) key off this
        /// alone — the price feed always runs when online (there is no separate on/off toggle).</summary>
        public static bool IsOnlineConfigured
        {
            get
            {
                return !string.IsNullOrEmpty(AccountIdValue) && !string.IsNullOrEmpty(AccountSecretValue)
                    && !string.IsNullOrEmpty(LeagueIdValue);
            }
        }
    }
}
