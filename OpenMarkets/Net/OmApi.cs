using System;
using UnityEngine;

namespace OpenMarkets.Net
{
    /// <summary>
    /// High-level facade over <see cref="OmHttp"/> for the online API. Builds the base URL + bearer from
    /// <see cref="Settings"/> and parses JSON responses with <c>JsonUtility</c>. All callbacks run on the
    /// MAIN thread (UI-safe). MAIN THREAD ONLY (touches <see cref="OmHttp.Instance"/>, which creates a
    /// GameObject). Network/parse failures surface as <c>ok=false</c> with a null DTO — callers degrade,
    /// never throw (dead-server posture).
    /// </summary>
    public static class OmApi
    {
        private static string Base { get { return Settings.EndpointValue; } }

        private static string Bearer
        {
            get
            {
                string id = Settings.AccountIdValue, secret = Settings.AccountSecretValue;
                return (!string.IsNullOrEmpty(id) && !string.IsNullOrEmpty(secret)) ? id + "." + secret : null;
            }
        }

        // ---- identity / league (driven from the Options UI) ----

        public static void CreateAccount(Action<bool, AccountDto> cb)
        {
            OmHttp.Instance.Request("POST", Base + "/accounts", null, null, (ok, st, body) =>
                cb(ok, ok ? Parse<AccountDto>(body) : null));
        }

        // Set (or clear, with empty) the friendly name leaguemates see instead of the opaque account id.
        public static void SetDisplayName(string name, Action<bool> cb)
        {
            string json = JsonUtility.ToJson(new SetNameBody { name = name ?? string.Empty });
            OmHttp.Instance.Request("POST", Base + "/accounts/name", json, Bearer, (ok, st, body) => cb(ok));
        }

        public static void CreateLeague(string name, Action<bool, LeagueDto> cb)
        {
            string json = JsonUtility.ToJson(new CreateLeagueBody { name = name });
            OmHttp.Instance.Request("POST", Base + "/leagues", json, Bearer, (ok, st, body) =>
                cb(ok, ok ? Parse<LeagueDto>(body) : null));
        }

        public static void JoinLeague(string joinCode, Action<bool, JoinDto> cb)
        {
            string json = JsonUtility.ToJson(new JoinBody { joinCode = joinCode });
            OmHttp.Instance.Request("POST", Base + "/leagues/join", json, Bearer, (ok, st, body) =>
                cb(ok, ok ? Parse<JoinDto>(body) : null));
        }

        // The leagues this account belongs to — backs the in-game league switcher.
        public static void GetMyLeagues(Action<bool, MyLeaguesDto> cb)
        {
            OmHttp.Instance.Request("GET", Base + "/leagues", null, Bearer, (ok, st, body) =>
                cb(ok, ok ? Parse<MyLeaguesDto>(body) : null));
        }

        // The current league's roster — who else is in the friend group. Member-only on the server.
        public static void GetMembers(Action<bool, MembersDto> cb)
        {
            string url = Base + "/leagues/members?league=" + Uri.EscapeDataString(Settings.LeagueIdValue);
            OmHttp.Instance.Request("GET", url, null, Bearer, (ok, st, body) =>
                cb(ok, ok ? Parse<MembersDto>(body) : null));
        }

        // Full co-op transparency: every ACTIVE investment in the league + the durable HISTORY of all investments.
        public static void GetInvestments(Action<bool, InvestmentsDto> cb)
        {
            string url = Base + "/investments?league=" + Uri.EscapeDataString(Settings.LeagueIdValue);
            OmHttp.Instance.Request("GET", url, null, Bearer, (ok, st, body) =>
                cb(ok, ok ? Parse<InvestmentsDto>(body) : null));
        }

        // The league's ranked standings boards + the per-account traveling titles. Member-only on the server.
        // Parsed via OmJson (the boards[]/titles[] arrays would be dropped by JsonUtility).
        public static void GetLeaderboards(Action<bool, LeaderboardsDto> cb)
        {
            string url = Base + "/leaderboards?league=" + Uri.EscapeDataString(Settings.LeagueIdValue);
            OmHttp.Instance.Request("GET", url, null, Bearer, (ok, st, body) =>
                cb(ok, ok ? Parse<LeaderboardsDto>(body) : null));
        }

        // The league's recent activity feed (newest-first), derived from the settlement event log. Member-only on
        // the server. Parsed via OmJson (the items[] array would be dropped by JsonUtility).
        public static void GetFeed(Action<bool, FeedDto> cb)
        {
            string url = Base + "/feed?league=" + Uri.EscapeDataString(Settings.LeagueIdValue);
            OmHttp.Instance.Request("GET", url, null, Bearer, (ok, st, body) =>
                cb(ok, ok ? Parse<FeedDto>(body) : null));
        }

        // The league's narrated saga (social slice 2), oldest→newest. Member-only on the server. Parsed via OmJson
        // (the entries[] array would be dropped by JsonUtility).
        public static void GetChronicle(Action<bool, ChronicleDto> cb)
        {
            string url = Base + "/chronicle?league=" + Uri.EscapeDataString(Settings.LeagueIdValue);
            OmHttp.Instance.Request("GET", url, null, Bearer, (ok, st, body) =>
                cb(ok, ok ? Parse<ChronicleDto>(body) : null));
        }

        // "On this day in league history": prior-day chronicle entries matching today's month/day. Member-only.
        public static void GetOnThisDay(Action<bool, ChronicleDto> cb)
        {
            string url = Base + "/chronicle/onthisday?league=" + Uri.EscapeDataString(Settings.LeagueIdValue);
            OmHttp.Instance.Request("GET", url, null, Bearer, (ok, st, body) =>
                cb(ok, ok ? Parse<ChronicleDto>(body) : null));
        }

        // A member's city-stat HISTORY: per-day snapshots (oldest→newest) + their net §-flow series. Member-only on
        // the server. Parsed via OmJson (the snapshots[]/netSeries[] arrays would be dropped by JsonUtility).
        public static void GetCityHistory(string accountId, Action<bool, CityHistoryDto> cb)
        {
            string url = Base + "/cityprofile/history?account=" + Uri.EscapeDataString(accountId ?? string.Empty)
                + "&league=" + Uri.EscapeDataString(Settings.LeagueIdValue);
            OmHttp.Instance.Request("GET", url, null, Bearer, (ok, st, body) =>
                cb(ok, ok ? Parse<CityHistoryDto>(body) : null));
        }

        // The active SHARED LEAGUE CRISES (social slice 3). Global (no league param); any authenticated account sees
        // the same list. Parsed via OmJson (the crises[] array would be dropped by JsonUtility).
        public static void GetCrises(Action<bool, CrisesDto> cb)
        {
            OmHttp.Instance.Request("GET", Base + "/crises", null, Bearer, (ok, st, body) =>
                cb(ok, ok ? Parse<CrisesDto>(body) : null));
        }

        // The cross-league public standings (no league param). Auth bearer; rows carry no account ids.
        public static void GetGlobalLeaderboards(Action<bool, GlobalLeaderboardsDto> cb)
        {
            OmHttp.Instance.Request("GET", Base + "/global-leaderboards", null, Bearer, (ok, st, body) =>
                cb(ok, ok ? Parse<GlobalLeaderboardsDto>(body) : null));
        }

        // The league's co-op MEGAPROJECTS (Great Works, social slice 4): active + completed. Member-only on the
        // server. Parsed via OmJson (the projects[]/reqs[]/goods[]/by[] arrays would be dropped by JsonUtility).
        public static void GetProjects(Action<bool, ProjectsDto> cb)
        {
            string url = Base + "/projects?league=" + Uri.EscapeDataString(Settings.LeagueIdValue);
            OmHttp.Instance.Request("GET", url, null, Bearer, (ok, st, body) =>
                cb(ok, ok ? Parse<ProjectsDto>(body) : null));
        }

        // Contribute commodity units toward a Great Work. The client has already REMOVED the physical stock from
        // its [trade] depots (InventoryService.MoveStock) before calling this — the server only records the count
        // (capped at the commodity's remaining requirement) and credits the contributor as a builder.
        public static void ContributeProjectGoods(string projectId, string commodity, long qty, Action<bool, ProjectDto, string> cb)
        {
            string json = JsonUtility.ToJson(new ContributeGoodsBody { commodity = commodity, qty = qty });
            string url = Base + "/projects/" + Uri.EscapeDataString(projectId) + "/contribute-goods";
            OmHttp.Instance.Request("POST", url, json, Bearer, (ok, st, body) =>
                cb(ok, ok ? Parse<ProjectDto>(body) : null, ok ? null : ErrorMessage(st, body)));
        }

        // Contribute § toward a Great Work. The § transfers out of your treasury to the project (conserving cash on
        // the server). Capped server-side at the project's remaining § requirement. Completing the last requirement
        // grants every builder a lasting buff (rides the existing /citystate effect path).
        public static void ContributeProjectGold(string projectId, long cents, Action<bool, ProjectDto, string> cb)
        {
            string json = JsonUtility.ToJson(new ContributeGoldBody { cents = cents });
            string url = Base + "/projects/" + Uri.EscapeDataString(projectId) + "/contribute-gold";
            OmHttp.Instance.Request("POST", url, json, Bearer, (ok, st, body) =>
                cb(ok, ok ? Parse<ProjectDto>(body) : null, ok ? null : ErrorMessage(st, body)));
        }

        // ---- reporting (day-rollover) ----

        public static void PostReports(ReportBatchDto batch, Action<bool> cb)
        {
            // Hand-serialize: Unity 5.6 JsonUtility.ToJson DROPS an object-array field (reports[]) that sits next to a
            // scalar (leagueId) in the same object — the same mixed-shape bug OmJson fixes on the INBOUND path, but it
            // bites OUTBOUND too. With ToJson the body posted an empty reports[], so the server stored nothing and the
            // league price index never moved. Build the JSON ourselves so the array survives.
            System.Text.StringBuilder sb = new System.Text.StringBuilder(64);
            sb.Append("{\"leagueId\":").Append(JsonString(batch.leagueId)).Append(",\"reports\":[");
            if (batch.reports != null)
            {
                for (int i = 0; i < batch.reports.Length; i++)
                {
                    if (i > 0) sb.Append(',');
                    ReportRowDto r = batch.reports[i];
                    sb.Append("{\"commodity\":").Append(JsonString(r.commodity))
                      .Append(",\"netSupply\":")
                      .Append(r.netSupply.ToString(System.Globalization.CultureInfo.InvariantCulture))
                      .Append('}');
                }
            }
            sb.Append("]}");
            OmHttp.Instance.Request("POST", Base + "/report/batch", sb.ToString(), Bearer, (ok, st, body) => cb(ok));
        }

        // Minimal JSON string literal (quotes + escapes) for hand-built bodies that JsonUtility can't serialize
        // correctly (object-arrays mixed with scalars). Sufficient for our ids / enum-name keys; not a general encoder.
        private static string JsonString(string s)
        {
            if (s == null) return "\"\"";
            System.Text.StringBuilder b = new System.Text.StringBuilder(s.Length + 2);
            b.Append('"');
            for (int i = 0; i < s.Length; i++)
            {
                char c = s[i];
                switch (c)
                {
                    case '"': b.Append("\\\""); break;
                    case '\\': b.Append("\\\\"); break;
                    case '\n': b.Append("\\n"); break;
                    case '\r': b.Append("\\r"); break;
                    case '\t': b.Append("\\t"); break;
                    default:
                        if (c < 0x20) b.Append("\\u").Append(((int)c).ToString("x4"));
                        else b.Append(c);
                        break;
                }
            }
            b.Append('"');
            return b.ToString();
        }

        // Report this city's own profile (population/happiness/industry/treasury) so leaguemates see it. Server sets
        // the identity + timestamp; we send only the scalar stats. Fire-and-forget on the day rollover.
        public static void PostCityProfile(CityProfilePostDto profile, Action<bool> cb)
        {
            string json = JsonUtility.ToJson(profile);
            OmHttp.Instance.Request("POST", Base + "/cityprofile", json, Bearer, (ok, st, body) => cb(ok));
        }

        // ---- trades / bonds / settlements (M5 Phase 2a) ----

        public static void CreateTrade(TradeOfferDto offer, Action<bool, TradeDto, string> cb)
        {
            // Hand-serialize: like the report batch, TradeOfferDto mixes scalars with an object-array (items[]),
            // which JsonUtility.ToJson silently drops — a trade would post with an EMPTY basket. Build it ourselves.
            System.Globalization.CultureInfo inv = System.Globalization.CultureInfo.InvariantCulture;
            System.Text.StringBuilder sb = new System.Text.StringBuilder(128);
            sb.Append("{\"leagueId\":").Append(JsonString(offer.leagueId))
              .Append(",\"counterparty\":").Append(JsonString(offer.counterparty))
              .Append(",\"defaultRateBps\":").Append(offer.defaultRateBps.ToString(inv))
              .Append(",\"installments\":").Append(offer.installments.ToString(inv))
              .Append(",\"items\":[");
            if (offer.items != null)
            {
                for (int i = 0; i < offer.items.Length; i++)
                {
                    if (i > 0) sb.Append(',');
                    LineItemDto it = offer.items[i];
                    sb.Append("{\"kind\":").Append(JsonString(it.kind))
                      .Append(",\"commodity\":").Append(JsonString(it.commodity))
                      .Append(",\"qtyFixed\":").Append(it.qtyFixed.ToString(inv))
                      .Append(",\"goldCents\":").Append(it.goldCents.ToString(inv))
                      .Append(",\"dir\":").Append(JsonString(it.dir))
                      .Append('}');
                }
            }
            sb.Append("]}");
            OmHttp.Instance.Request("POST", Base + "/trades", sb.ToString(), Bearer, (ok, st, body) =>
                cb(ok, ok ? Parse<TradeDto>(body) : null, ok ? null : ErrorMessage(st, body)));
        }

        public static void GetTrades(Action<bool, TradeListDto> cb)
        {
            string url = Base + "/trades?league=" + Uri.EscapeDataString(Settings.LeagueIdValue);
            OmHttp.Instance.Request("GET", url, null, Bearer, (ok, st, body) =>
                cb(ok, ok ? Parse<TradeListDto>(body) : null));
        }

        /// <summary>accept | decline | cancel. Returns the updated trade (bare TradeDto).</summary>
        public static void TradeTransition(string tradeId, string action, Action<bool, TradeDto, string> cb)
        {
            string url = Base + "/trades/" + Uri.EscapeDataString(tradeId) + "/" + action;
            OmHttp.Instance.Request("POST", url, null, Bearer, (ok, st, body) =>
                cb(ok, ok ? Parse<TradeDto>(body) : null, ok ? null : ErrorMessage(st, body)));
        }

        /// <summary>Settle the current installment (net payer). The result carries the booked event.</summary>
        public static void SettleTrade(string tradeId, Action<bool, TradeSettleResultDto, string> cb)
        {
            string url = Base + "/trades/" + Uri.EscapeDataString(tradeId) + "/settle";
            OmHttp.Instance.Request("POST", url, null, Bearer, (ok, st, body) =>
                cb(ok, ok ? Parse<TradeSettleResultDto>(body) : null, ok ? null : ErrorMessage(st, body)));
        }

        // Report an undelivered give-goods shortfall for an installment (M6). The server mints a cash-debt bond
        // (me → counterparty) for the undelivered value and dings my reliability. Fire-and-forget (ok only).
        public static void ReportShortfall(string tradeId, int installment, long cents, Action<bool> cb)
        {
            string json = JsonUtility.ToJson(new ShortfallBody { installment = installment, cents = cents });
            string url = Base + "/trades/" + Uri.EscapeDataString(tradeId) + "/shortfall";
            OmHttp.Instance.Request("POST", url, json, Bearer, (ok, st, body) => cb(ok));
        }

        public static void GetBonds(Action<bool, BondListDto> cb)
        {
            string url = Base + "/bonds?league=" + Uri.EscapeDataString(Settings.LeagueIdValue);
            OmHttp.Instance.Request("GET", url, null, Bearer, (ok, st, body) =>
                cb(ok, ok ? Parse<BondListDto>(body) : null));
        }

        public static void SettleBond(string bondId, Action<bool, BondSettleResultDto, string> cb)
        {
            string url = Base + "/bonds/" + Uri.EscapeDataString(bondId) + "/settle";
            OmHttp.Instance.Request("POST", url, null, Bearer, (ok, st, body) =>
                cb(ok, ok ? Parse<BondSettleResultDto>(body) : null, ok ? null : ErrorMessage(st, body)));
        }

        // ---- manual loan negotiation (Phase 3) ----

        public static void OfferLoan(LoanOfferDto offer, Action<bool, BondDto, string> cb)
        {
            string json = JsonUtility.ToJson(offer);
            OmHttp.Instance.Request("POST", Base + "/loans", json, Bearer, (ok, st, body) =>
                cb(ok, ok ? Parse<BondDto>(body) : null, ok ? null : ErrorMessage(st, body)));
        }

        public static void CounterLoan(string bondId, LoanTermsDto terms, Action<bool, BondDto, string> cb)
        {
            string json = JsonUtility.ToJson(terms);
            string url = Base + "/loans/" + Uri.EscapeDataString(bondId) + "/counter";
            OmHttp.Instance.Request("POST", url, json, Bearer, (ok, st, body) =>
                cb(ok, ok ? Parse<BondDto>(body) : null, ok ? null : ErrorMessage(st, body)));
        }

        // Accept an offered loan: the principal transfers lender→borrower (the result carries that event).
        public static void AcceptLoan(string bondId, Action<bool, BondSettleResultDto, string> cb)
        {
            string url = Base + "/loans/" + Uri.EscapeDataString(bondId) + "/accept";
            OmHttp.Instance.Request("POST", url, null, Bearer, (ok, st, body) =>
                cb(ok, ok ? Parse<BondSettleResultDto>(body) : null, ok ? null : ErrorMessage(st, body)));
        }

        /// <summary>decline (non-proposer rejects) | cancel (proposer withdraws).</summary>
        public static void LoanTransition(string bondId, string action, Action<bool, BondDto, string> cb)
        {
            string url = Base + "/loans/" + Uri.EscapeDataString(bondId) + "/" + action;
            OmHttp.Instance.Request("POST", url, null, Bearer, (ok, st, body) =>
                cb(ok, ok ? Parse<BondDto>(body) : null, ok ? null : ErrorMessage(st, body)));
        }

        // The league's settlement events after `since` (monotonic seq), for idempotent client booking.
        public static void GetSettlements(long since, Action<bool, SettlementListDto> cb)
        {
            string url = Base + "/settlements?league=" + Uri.EscapeDataString(Settings.LeagueIdValue)
                + "&since=" + since;
            OmHttp.Instance.Request("GET", url, null, Bearer, (ok, st, body) =>
                cb(ok, ok ? Parse<SettlementListDto>(body) : null));
        }

        // M9: the league's per-commodity effective price index + active events + sparkline history. Drives the
        // client MarketFeed (the price source) when online. Parsed via OmJson (the history[] array would be dropped
        // by JsonUtility).
        public static void GetPrices(Action<bool, PricesDto> cb)
        {
            string url = Base + "/prices?league=" + Uri.EscapeDataString(Settings.LeagueIdValue);
            OmHttp.Instance.Request("GET", url, null, Bearer, (ok, st, body) =>
                cb(ok, ok ? Parse<PricesDto>(body) : null));
        }

        // The caller's austerity status + active co-op effects in the active league (backs the Bonds-tab banner +
        // the M8 city levers).
        public static void GetCityState(Action<bool, CityStateDto> cb)
        {
            string url = Base + "/citystate?league=" + Uri.EscapeDataString(Settings.LeagueIdValue);
            OmHttp.Instance.Request("GET", url, null, Bearer, (ok, st, body) =>
                cb(ok, ok ? Parse<CityStateDto>(body) : null));
        }

        // M8 co-op lever: invest in a leaguemate — pay a symmetric § cost (transfers to them) to grant them a
        // temporary demand+attractiveness buff for `days`. The server books the cash + creates the effect.
        public static void Invest(string granteeId, long costCents, int days, string demandKind, Action<bool, string> cb)
        {
            string json = JsonUtility.ToJson(new InvestBody
            {
                granteeId = granteeId, costCents = costCents, days = days, demandKind = demandKind
            });
            // league goes in the QUERY (authMember reads ?league=, not the body). Body keeps it too — harmless/ignored.
            string url = Base + "/investment-office?league=" + Uri.EscapeDataString(Settings.LeagueIdValue);
            OmHttp.Instance.Request("POST", url, json, Bearer, (ok, st, body) =>
                cb(ok, ok ? null : ErrorMessage(st, body)));
        }

        // M8 co-op lever: bail out a leaguemate — pay down their defaulted bonds (the § transfers to each creditor),
        // helping them escape austerity. Reports the cents actually applied (capped at their outstanding default).
        public static void Bailout(string debtorId, long cents, Action<bool, long, string> cb)
        {
            string json = JsonUtility.ToJson(new BailoutBody
            {
                debtorId = debtorId, cents = cents
            });
            // league in the QUERY (authMember reads ?league=, not the body). Body keeps it too — harmless/ignored.
            string url = Base + "/bailout?league=" + Uri.EscapeDataString(Settings.LeagueIdValue);
            OmHttp.Instance.Request("POST", url, json, Bearer, (ok, st, body) =>
            {
                if (!ok) { cb(false, 0L, ErrorMessage(st, body)); return; }
                BailoutResultDto r = Parse<BailoutResultDto>(body);
                cb(true, r != null ? r.appliedCents : 0L, null);
            });
        }

        // Deserialize via OmJson (a field-name-keyed reader), NOT JsonUtility: Unity 5.6's FromJson drops an
        // object-array field that sits alongside scalar fields in the same object (it parsed /leagues/members'
        // `name` but left `members` empty), which would equally break /settlements, /trades and /prices. OmJson
        // already guards empty/malformed input and returns null, so callers keep their dead-server posture.
        private static T Parse<T>(string body) where T : class
        {
            return OmJson.Parse<T>(body);
        }

        // Turn a failed (status, body) into a short player-facing reason. Prefers the server's {"error":...}
        // message; falls back to a status-based hint when the body is empty (network/TLS failures report
        // status 0 — the server was never reached).
        private static string ErrorMessage(long status, string body)
        {
            ErrorDto e = Parse<ErrorDto>(body);
            if (e != null && !string.IsNullOrEmpty(e.error)) return e.error;
            if (status == 0) return "couldn't reach the server";
            return "server returned " + status;
        }

        // Tiny request bodies (JsonUtility needs a concrete [Serializable] type, not anonymous objects).
        [Serializable] private class CreateLeagueBody { public string name; }
        [Serializable] private class JoinBody { public string joinCode; }
        [Serializable] private class SetNameBody { public string name; }
        [Serializable] private class ShortfallBody { public int installment; public long cents; }
        [Serializable] private class InvestBody { public string granteeId; public long costCents; public int days; public string demandKind; }
        [Serializable] private class BailoutBody { public string debtorId; public long cents; }
        [Serializable] private class ContributeGoodsBody { public string commodity; public long qty; }
        [Serializable] private class ContributeGoldBody { public long cents; }
    }
}
