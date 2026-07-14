using System;

namespace OpenMarkets.Net
{
    // Wire DTOs for the Open Markets backend. UnityEngine.JsonUtility requires PLAIN [Serializable] types
    // with PUBLIC FIELDS and CANNOT (de)serialize maps — hence the array shapes (e.g. commodities[]).
    // Keep these in lockstep with /server's JSON.

    // Every non-2xx response from the server carries a JSON body of the form {"error":"<reason>"} (see the
    // server's writeErr). Parsing it lets the UI show the real rejection reason instead of a generic message.
    [Serializable]
    public class ErrorDto
    {
        public string error;
    }

    [Serializable]
    public class AccountDto
    {
        public string accountId;
        public string secret; // returned once at creation
    }

    [Serializable]
    public class LeagueDto
    {
        public string leagueId;
        public string joinCode;
        public string name;
    }

    [Serializable]
    public class JoinDto
    {
        public string leagueId;
        public bool joined;
    }

    // GET /leagues — one entry per league the caller belongs to (for the in-game league switcher). joinCode
    // is present only for leagues the caller owns.
    [Serializable]
    public class LeagueSummaryDto
    {
        public string leagueId;
        public string name;
        public string joinCode;
        public bool isOwner;
    }

    [Serializable]
    public class MyLeaguesDto
    {
        public LeagueSummaryDto[] leagues;
    }

    [Serializable]
    public class MemberDto
    {
        public string accountId;
        public string displayName; // optional friendly name; empty → fall back to the account id
        public bool isOwner;
        public int reliability;    // 0..100 on-time score (100 with no history)
        // League transparency (M7): each member's public standing, shown to every leaguemate.
        public bool austerity;             // in austerity (owes terminally-defaulted debt)
        public long outstandingDebtCents;  // garnishable defaulted balance still owed
        public long netCents;              // net cash position in the league (+ up / − down)
        // Presence + city profile (flattened from the server's CityProfile; 0/empty when not reported yet).
        public bool online;          // seen within the server's offline threshold
        public long lastSeenSec;     // Unix seconds of last activity / last profile report (0 = never)
        public string cityName;
        public int population;
        public int happiness;        // 0..100 (city popularity)
        public int attractiveness;
        public long cashCents;       // treasury
        public long weeklyIncomeCents;
        public long weeklyExpensesCents;
        public int resBuildings;
        public int comBuildings;
        public int offBuildings;
        public int indBuildings;
        public int indWorkers;
        public int farmWorkers;
        public int forestWorkers;
        public int oreWorkers;
        public int oilWorkers;
        public int unemployment;     // 0..100
        public int buildingCount;
        public int tourists;
        public int landValue;
        public int crime;            // 0..100
    }

    // POST /cityprofile body — this city's own snapshot, gathered on the sim thread and reported each in-game day so
    // leaguemates can see it. Field names MUST match the server's CityProfile json tags. accountId + reportedAt are
    // set server-side (never sent). Serialized with JsonUtility (outgoing scalars only — the array bug is inbound).
    [Serializable]
    public class CityProfilePostDto
    {
        public string cityName;
        public int population;
        public int happiness;
        public int attractiveness;
        public long cashCents;
        public long weeklyIncomeCents;
        public long weeklyExpensesCents;
        public int resBuildings;
        public int comBuildings;
        public int offBuildings;
        public int indBuildings;
        public int indWorkers;
        public int farmWorkers;
        public int forestWorkers;
        public int oreWorkers;
        public int oilWorkers;
        public int unemployment;
        public int buildingCount;
        public int tourists;
        public int landValue;
        public int crime;
        // NOTE: do NOT add accountId/reportedAt here. This DTO is serialized OUTBOUND via JsonUtility.ToJson; a
        // string reportedAt would emit "reportedAt":"" which the server's time.Time field rejects (400). The
        // inbound history snapshot is a SEPARATE shape — see CitySnapshotDto.
    }

    // One city-history snapshot (GET /cityprofile/history). INBOUND only (parsed via OmJson), so it can carry the
    // server-stamped accountId/reportedAt/reliability that the outbound CityProfilePostDto must not. Field names
    // match the server's CityProfile json tags; omitempty fields absent in the JSON default to 0/empty.
    [Serializable]
    public class CitySnapshotDto
    {
        public string accountId;
        public string reportedAt;    // RFC3339 timestamp (server-stamped)
        public int reliability;      // 0..100 on-time score at the snapshot
        public string cityName;
        public int population;
        public int happiness;
        public int attractiveness;
        public long cashCents;
        public long weeklyIncomeCents;
        public long weeklyExpensesCents;
        public int resBuildings;
        public int comBuildings;
        public int offBuildings;
        public int indBuildings;
        public int indWorkers;
        public int farmWorkers;
        public int forestWorkers;
        public int oreWorkers;
        public int oilWorkers;
        public int unemployment;
        public int buildingCount;
        public int tourists;
        public int landValue;
        public int crime;
    }

    // GET /cityprofile/history?account=ID&league=ID — a member's city-stat snapshots (oldest→newest) plus their net
    // §-flow series. Member-only, bearer auth. Parsed via OmJson (the snapshots[]/netSeries[] object arrays alongside
    // the accountId scalar would be dropped by JsonUtility).
    [Serializable]
    public class CityHistoryDto
    {
        public string accountId;
        public CitySnapshotDto[] snapshots;
        public NetPointDto[] netSeries;
    }

    // One point on a city's net §-flow series (its own timestamps/cadence — plotted by index in the chart).
    [Serializable]
    public class NetPointDto
    {
        public string ts;     // RFC3339 timestamp
        public long cents;    // net § ×100 at that point
    }

    // GET /leagues/members?league=ID — the league roster (the friend group). Owner first, then the server's
    // lexicographic order; account ids are the only server-side identity (no display names yet).
    [Serializable]
    public class MembersDto
    {
        public string leagueId;
        public string name;
        public string ownerId;
        public MemberDto[] members;
    }

    [Serializable]
    public class CommodityIndexDto
    {
        public string commodity; // canonical wire key (enum name)
        public float index;      // effective index (elasticity × global event), clamped 0.5..2.0 server-side
        public int eventPct;     // M9: active price-shock swing % (0 = none)
        public float[] history;  // M9: rolling effective-index ring (oldest→newest) for the sparkline
    }

    [Serializable]
    public class PricesDto
    {
        public string version;
        public long ts;
        public CommodityIndexDto[] commodities;
    }

    [Serializable]
    public class ReportRowDto
    {
        public string commodity;
        public double netSupply; // +export (supply) / -import (demand)
    }

    [Serializable]
    public class ReportBatchDto
    {
        public string leagueId;
        public ReportRowDto[] reports;
    }

    // ---- Trades (two-sided basket), bonds, settlement events (M5 Phase 2a) ----

    // One directional component of a basket. For a commodity line value = qtyFixed × unitPriceCents / 1000
    // (unitPriceCents frozen server-side at accept); for a gold line value = goldCents. dir is give|take
    // relative to offeredBy. qtyFixed is the quantity scaled by Money.QtyScale (1000).
    [Serializable]
    public class LineItemDto
    {
        public string kind;               // "commodity" | "gold"
        public string commodity;          // commodity lines (wire/enum key)
        public long qtyFixed;             // commodity lines, scaled by Money.QtyScale
        public long unitPriceCents;       // frozen at accept (commodity)
        public long goldCents;            // gold lines
        public string dir;                // "give" | "take"
        public long valueCentsAtAccept;   // frozen at accept; 0 until then
    }

    [Serializable]
    public class TradeDto
    {
        public string id;
        public string leagueId;
        public string offeredBy;
        public string counterparty;
        public LineItemDto[] items;
        public long defaultRateBps;
        public int installments;
        public string status;            // offered|active|completed|declined|cancelled
        public int settled;              // net installments settled
        public long acceptedDay;
    }

    [Serializable]
    public class TradeListDto
    {
        public TradeDto[] trades;
    }

    // POST /trades body. Line values are frozen server-side, so unitPriceCents/valueCentsAtAccept on the items
    // are ignored by the server (we only send kind/commodity/qtyFixed/goldCents/dir).
    [Serializable]
    public class TradeOfferDto
    {
        public string leagueId;
        public string counterparty;
        public long defaultRateBps;
        public int installments;
        public LineItemDto[] items;
    }

    [Serializable]
    public class BondDto
    {
        public string id;
        public string leagueId;
        public string creditorId;
        public string debtorId;
        public long principalCents;
        public long interestBps;
        public int installments;
        public int settled;
        public int missedCount;
        public long totalDueCents;
        public string status;            // offered|active|delinquent|defaultedReceivable|completed|cleared|writtenOff|...
        public string origin;            // "manual" | "trade:<id>"
        public string proposedBy;        // manual-loan negotiation: whose terms stand (the OTHER party acts)
    }

    [Serializable]
    public class BondListDto
    {
        public BondDto[] bonds;
    }

    // POST /loans body — offer a negotiated peer loan as lender ("lend") or borrower ("borrow").
    [Serializable]
    public class LoanOfferDto
    {
        public string leagueId;
        public string role;          // "lend" | "borrow"
        public string counterparty;
        public long principalCents;
        public long interestBps;
        public int installments;
    }

    // POST /loans/{id}/counter body — revised terms.
    [Serializable]
    public class LoanTermsDto
    {
        public long principalCents;
        public long interestBps;
        public int installments;
    }

    // A server-authored money movement. The reconciler books each exactly once (keyed by seq): if I'm the
    // receiver → credit; if I'm the payer → debit. (ref/created are present on the wire but unused here.)
    [Serializable]
    public class SettlementEventDto
    {
        public long seq;
        public string leagueId;
        public string payerId;
        public string receiverId;
        public long cents;
    }

    [Serializable]
    public class SettlementListDto
    {
        public SettlementEventDto[] events;
        public long latestSeq;  // the caller's highest server seq (informational)
        // The server's data EPOCH (stable across restarts, new after a wipe). When it changes, the server's data
        // was genuinely reset — the client resets its cursors and replays the fresh economy (safe: no old events).
        public string epoch;
    }

    // GET /citystate — the caller's austerity status + active co-op effects (M8) in a league. In austerity while it
    // owes any terminally defaulted bond. NOTE parsed via OmJson (Parse<T>), NOT JsonUtility — the effects[] object
    // array alongside the scalar fields would otherwise be dropped (the JsonUtility mixed-array bug).
    [Serializable]
    public class CityStateDto
    {
        public bool austerity;
        public long outstandingDebtCents;
        public int defaultedBonds;
        public CityEffectDto[] effects;          // active co-op buffs RECEIVED by this city (M8 investment-office)
        public CityEffectDto[] investmentsMade;  // active investments this city has GRANTED to others
        public int dueIntervalSec;               // server's wall-clock due period (s); paces the client auto-settle sweep
    }

    // POST /bailout result — how many cents of the debtor's defaulted balance the bail-out actually paid down
    // (capped at their outstanding default). The `events` array in the response is ignored here.
    [Serializable]
    public class BailoutResultDto
    {
        public long appliedCents;
    }

    // One active co-op effect on a city. The server computes + caps magnitudes; the client only applies/displays
    // transient effects from /citystate (nothing persisted to the save).
    [Serializable]
    public class CityEffectDto
    {
        public string id;
        public string kind;            // investmentOffice | projectBuff | marketShield | priceEdge
        public string issuerId;        // who granted it (the investor)
        public string granteeId;       // whose city receives it (the beneficiary)
        public long costCents;         // the § the issuer invested
        public int demandBoost;        // demand points to add to the targeted channel
        public string demandKind;      // which demand the boost targets: res | com | work (empty = res, back-compat)
        public int attractRate;        // attractiveness rate to re-apply each cycle
        public string commodity;       // trade reward commodity, if any
        public int tradePctBips;       // trade reward magnitude in basis points
        public int ticksRemaining;     // due-cycle ticks (≈ in-game days) left
    }

    // GET /investments?league=ID — full league transparency: every ACTIVE grant + the durable HISTORY of all
    // investments ever (from the settlement-event log; survives buff expiry, money trail only). Parsed via OmJson.
    [Serializable]
    public class InvestmentsDto
    {
        public CityEffectDto[] active;     // every active issuer→grantee grant in the league
        public InvestEventDto[] history;   // every investment ever made (newest first)
    }

    // One historical investment (a settlement event tagged "invest:"). Money trail only — no buff details.
    [Serializable]
    public class InvestEventDto
    {
        public string payerId;     // issuer
        public string receiverId;  // grantee
        public long cents;         // § invested ×100
        public string created;     // RFC3339 timestamp
    }

    // ---- Leaderboards / standings (league + global) ----

    // GET /leaderboards?league=ID — every ranked board for the league + the per-account "traveling titles".
    // Parsed via OmJson (the boards[]/titles[] object arrays alongside the leagueId scalar would be dropped by
    // JsonUtility). Board ids in order: netWorth, marketMover, tradeVolume, patron, masterBuilder, reliability,
    // deadbeat, phoenix, population, happiness.
    [Serializable]
    public class LeaderboardsDto
    {
        public string leagueId;
        public BoardDto[] boards;
        public TitleEntryDto[] titles;
    }

    [Serializable]
    public class BoardDto
    {
        public string id;             // wire key (e.g. "netWorth")
        public string label;          // human label (e.g. "Net Worth")
        public bool higherIsBetter;
        public BoardRowDto[] rows;     // ranked, best first
    }

    [Serializable]
    public class BoardRowDto
    {
        public string accountId;       // resolve to a friendly name + title via LeagueRoster.Display
        public string displayName;     // server's snapshot name (we prefer LeagueRoster.Display)
        public long value;
        public int rank;
    }

    // One account's traveling titles (every title it currently holds). LeagueRoster picks ONE primary by priority.
    [Serializable]
    public class TitleEntryDto
    {
        public string accountId;
        public string[] titles;
    }

    // GET /feed — the league's recent activity feed (newest-first), derived from the settlement event log. Parsed
    // via OmJson (the items[] object array alongside the leagueId scalar would be dropped by JsonUtility).
    [Serializable]
    public class FeedDto
    {
        public string leagueId;
        public FeedItemDto[] items;
    }

    [Serializable]
    public class FeedItemDto
    {
        public long seq;
        public string ts;          // ISO timestamp (the event's Created)
        public string type;        // trade | bond | garnish | investment | bailout | shortfall | loan | other
        public string accountA;    // payer — resolve via LeagueRoster.Display
        public string accountB;    // receiver — resolve via LeagueRoster.Display
        public long cents;
        public string @ref;        // the raw settlement ref ("trade:ID:n", …); @ escapes the C# keyword, .Name == "ref"
    }

    // GET /chronicle and /chronicle/onthisday — the league's persistent narrated saga (social slice 2). Parsed
    // via OmJson (the entries[] object array alongside the leagueId scalar would be dropped by JsonUtility). Each
    // entry's `text` is the server-FROZEN narration (names already resolved) — render it verbatim.
    [Serializable]
    public class ChronicleDto
    {
        public string leagueId;
        public ChronicleEntryDto[] entries;   // ascending by seq (oldest→newest)
    }

    [Serializable]
    public class ChronicleEntryDto
    {
        public long seq;
        public string kind;        // founded | joined | bailout | austerity | escaped | record-trade
        public string actorId;
        public string targetId;
        public string text;        // frozen full-sentence narration (names already resolved) — show verbatim
        public long cents;
        public string created;     // RFC3339 timestamp
    }

    // GET /crises — the active SHARED LEAGUE CRISES (social slice 3): named, narrated economic shocks the whole
    // league weathers together. Global (no league param; same list for everyone). Parsed via OmJson (the crises[]
    // object array would be dropped by JsonUtility).
    [Serializable]
    public class CrisesDto
    {
        public CrisisDto[] crises;
    }

    [Serializable]
    public class CrisisDto
    {
        public string name;        // e.g. "The Oil Blight"
        public string narrative;   // full sentence describing the crisis
        public string kind;        // blight | glut | boom | embargo (the EventState Kind is "crisis" while active)
        public string commodity;   // wire key — resolve via Commodities for the display name
        public int eventPct;       // signed swing % (e.g. +60, -50)
        public int ticksLeft;      // due-cycle ticks (≈ in-game days) left
    }

    // GET /global-leaderboards — cross-league public boards (no league param; no account ids). Board ids:
    // globalNetWorth, globalReliability, globalTradeVolume.
    [Serializable]
    public class GlobalLeaderboardsDto
    {
        public GlobalBoardDto[] boards;
    }

    [Serializable]
    public class GlobalBoardDto
    {
        public string id;
        public string label;
        public bool higherIsBetter;
        public GlobalBoardRowDto[] rows;
    }

    [Serializable]
    public class GlobalBoardRowDto
    {
        public int rank;
        public string displayName;     // anonymised cross-league handle (no account id)
        public long value;
        public int percentile;         // 100 = top; "top {100-percentile}%"
        public string tier;            // e.g. "Diamond"
        public bool you;               // highlight this row
    }

    // Returned by POST /trades/{id}/settle and /bonds/{id}/settle.
    [Serializable]
    public class TradeSettleResultDto
    {
        public TradeDto trade;
        public SettlementEventDto @event;
    }

    [Serializable]
    public class BondSettleResultDto
    {
        public BondDto bond;
        public SettlementEventDto @event;
    }

    // GET /projects — the league's co-op MEGAPROJECTS (Great Works, social slice 4). Parsed via OmJson (the
    // projects[]/reqs[]/goods[]/by[] object arrays alongside the leagueId scalar would be dropped by JsonUtility).
    // IMPORTANT: the server emits Goods and By as ARRAYS of {commodity,qty} / {accountId,score} (NOT JSON maps),
    // because OmJson cannot bind dynamic-key maps.
    [Serializable]
    public class ProjectsDto
    {
        public string leagueId;
        public ProjectDto[] projects;
    }

    [Serializable]
    public class ProjectDto
    {
        public string id;
        public string name;
        public string description;
        public ProjectReqDto[] reqs;        // per-commodity unit requirements
        public long goldReqCents;           // optional § requirement (0 = none)
        public GoodsPairDto[] goods;        // commodity → units contributed so far (array, not a map)
        public long gold;                   // § contributed so far (cents)
        public BuilderPairDto[] by;         // accountId → builder score (array, not a map)
        public string buffKind;             // res | com | work — the demand channel the completion buff boosts
        public long buffMagnitudeCents;      // advertised synthetic CostCents; server scales it per builder on completion
        public int buffDays;                // the buff lifetime in due-cycle ticks (≈ in-game days)
        public string tradeRewardKind;       // marketShield | priceEdge | empty
        public string tradeRewardCommodity;  // wire key for the themed commodity
        public int tradeRewardPctBips;       // trade reward magnitude in basis points
        public string status;               // open | completed
        public long credited;               // contribute-goods response only: units actually applied this call (0 on plain reads)
    }

    [Serializable]
    public class ProjectReqDto
    {
        public string commodity;            // wire key (the TransferReason enum name)
        public long qty;                    // whole units required
    }

    [Serializable]
    public class GoodsPairDto
    {
        public string commodity;            // wire key
        public long qty;                    // whole units contributed so far
    }

    [Serializable]
    public class BuilderPairDto
    {
        public string accountId;
        public long score;                  // contribution score (units + cents/100)
    }
}
