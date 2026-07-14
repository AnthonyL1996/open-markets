using System.Collections.Generic;
using System.Globalization;
using ColossalFramework.UI;
using OpenMarkets.Net;
using UnityEngine;

namespace OpenMarkets.UI.Terminal
{
    /// <summary>
    /// Members tab: the roster of the current league — who else is in the friend group, with the owner flagged and
    /// the player's own row marked "you". Also hosts the M8 co-op "investment office" form: pick a leaguemate, a §
    /// amount, and a duration, and grant them a temporary demand+attractiveness buff (the § transfers to them — a
    /// real investment). Roster data is fetched asynchronously (<see cref="OmApi.GetMembers"/>) on Refresh; the
    /// callback (main thread) caches the list and rebuilds. MAIN THREAD. The form posts via <see cref="OmApi.Invest"/>.
    /// </summary>
    internal sealed class MembersTab : ITabBody
    {
        private const float NameCol = 150f;  // includes an online ●/○ prefix; full profile on hover (tooltip)
        private const float RoleCol = 72f;
        private const float RelCol = 50f;
        private const float PopCol = 58f;    // headline city stats; the rest live in the row tooltip
        private const float HappyCol = 52f;
        private const float NumCol = 92f;    // Net § / Debt § (M7 transparency)
        private const float Width = NameCol + RoleCol + RelCol + PopCol + HappyCol + 2f * NumCol;
        private const float FormH = 90f;     // co-op form occupies the top strip (two rows + an explainer); roster below

        // Investment-duration presets (in-game days), within the server's InvestMaxDays (14).
        private static readonly int[] DaysPresets = { 1, 3, 7, 14 };

        // Demand channels the investment can target (wire key + label). CS1 exposes only these three — industrial
        // and office share the single "work" channel — so there's no finer split than these.
        private static readonly string[] DemandKinds = { "res", "com", "work" };
        private static readonly string[] DemandLabels = { "Residential", "Commercial", "Industry & Office" };

        // Mirror of the server's cost→demand-points formula (effect.go) for the live preview: §2,500/pt, cap 20.
        private const long CentsPerDemandPt = 250000L;
        private const int DemandPtCap = 20;

        private UIPanel _root;
        private UIScrollablePanel _grid;
        private MembersDto _members;
        private bool _loading;
        private bool _failed;   // last fetch returned an error (vs. a genuinely empty roster)

        // Investment form state.
        private UIButton _targetBtn;
        private UITextField _amountField;
        private UIButton _daysBtn;
        private UIButton _investBtn;
        private UIButton _bailBtn;
        private UIButton _demandBtn;     // which demand channel the investment boosts
        private UILabel _statusLabel;
        private UILabel _explainLabel;   // plain-language explainer of what Invest / Bail do + the live cost
        private string _targetId = string.Empty;
        private int _daysIdx = 2;   // default 7 days
        private int _demandIdx;     // default 0 = Residential
        private bool _sending;

        public string TabLabel { get { return "Members"; } }

        public string Title
        {
            get
            {
                if (_members != null && !string.IsNullOrEmpty(_members.name)) return "Open Markets — " + _members.name;
                return "Open Markets — league members";
            }
        }

        public void Build(UIComponent host, Vector2 size)
        {
            _root = host.AddUIComponent<UIPanel>();
            _root.relativePosition = Vector3.zero;
            _root.size = size;
            _root.autoLayout = false;

            // Investment-office form (top strip).
            _targetBtn = MakeButton(2f, 4f, 286f, "Invest in: —");
            _targetBtn.eventClicked += delegate { CycleTarget(); SyncForm(); };
            _targetBtn.tooltip = "Cycle which leaguemate the Invest / Bail-out actions target.";
            Caption("§", 294f, 8f);
            _amountField = Field(308f, 4f, 92f);
            _amountField.text = "10000";
            _amountField.eventTextChanged += delegate { SyncForm(); };   // live-update the explainer as you type
            _daysBtn = MakeButton(404f, 4f, 48f, "7 d");
            _daysBtn.eventClicked += delegate { _daysIdx = (_daysIdx + 1) % DaysPresets.Length; SyncForm(); };
            _investBtn = MakeButton(456f, 4f, 58f, "Invest");
            _investBtn.eventClicked += delegate { Submit(); };
            _investBtn.tooltip = "Send § to this leaguemate — it TRANSFERS to them (out of your treasury). They receive "
                + "the cash plus a temporary demand + attractiveness boost for the chosen number of days.";
            // Second row: which demand the investment boosts, bail-out, and the status line.
            _demandBtn = MakeButton(2f, 32f, 150f, "Demand: Residential");
            _demandBtn.eventClicked += delegate { _demandIdx = (_demandIdx + 1) % DemandKinds.Length; SyncForm(); };
            _demandBtn.tooltip = "Which demand the investment boosts: Residential, Commercial, or Industry & Office "
                + "(CS1 combines industrial and office into one demand channel).";
            _bailBtn = MakeButton(156f, 32f, 80f, "Bail out");
            _bailBtn.eventClicked += delegate { SubmitBailout(); };
            _bailBtn.tooltip = "Pay down this leaguemate's defaulted debt (oldest first) to help them escape austerity. "
                + "Capped at what they owe; the § comes out of your treasury.";
            _statusLabel = SmallLabel(244f, 36f, UiKit.Dim);

            // Plain-language explainer line (third row), updated live in SyncForm so the player sees exactly what the
            // current Invest / Bail-out will do and what it costs THEM before clicking.
            _explainLabel = _root.AddUIComponent<UILabel>();
            _explainLabel.relativePosition = new Vector3(2f, 60f);
            _explainLabel.autoSize = false;
            _explainLabel.wordWrap = true;
            _explainLabel.size = new Vector2(size.x - 6f, 28f);
            _explainLabel.textScale = 0.7f;
            _explainLabel.textColor = UiKit.Dim;
            _explainLabel.text = string.Empty;

            _grid = _root.AddUIComponent<UIScrollablePanel>();
            _grid.relativePosition = new Vector3(0f, FormH);
            _grid.size = new Vector2(size.x, size.y - FormH);
            _grid.autoLayout = true;
            _grid.autoLayoutDirection = LayoutDirection.Vertical;
            _grid.autoLayoutPadding = new RectOffset(0, 0, 0, 2);
            _grid.clipChildren = true;
            _grid.scrollWheelDirection = UIOrientation.Vertical;

            SyncForm();
        }

        public void SetVisible(bool on) { if (_root != null) _root.isVisible = on; }

        public void Refresh()
        {
            if (_grid == null) return;
            SyncForm();
            Rebuild(); // draw current cache (or a loading/empty note) immediately
            if (_loading) return;
            _loading = true;
            OmApi.GetMembers(delegate (bool ok, MembersDto dto)
            {
                _loading = false;
                _failed = !ok || dto == null;
                if (!_failed)
                {
                    _members = dto;
                    LeagueRoster.Update(dto); // share names with the Inbox/Contracts/Chirper
                }
                SyncForm();   // the roster may have changed → refresh the target picker
                Rebuild(); // callback is on the main thread (OmHttp contract)
            });
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

            RenderActiveInvestments();   // "Investments in your city" — who invested, how much, what it boosts

            UIPanel header = NewRow();
            UiKit.Cellate(header, NameCol, "Account", UiKit.Head, UIHorizontalAlignment.Left);
            UiKit.Cellate(header, RoleCol, "Role", UiKit.Head, UIHorizontalAlignment.Left);
            UiKit.Cellate(header, RelCol, "Reliab.", UiKit.Head, UIHorizontalAlignment.Right);
            UiKit.Cellate(header, PopCol, "Pop", UiKit.Head, UIHorizontalAlignment.Right);
            UiKit.Cellate(header, HappyCol, "Happy", UiKit.Head, UIHorizontalAlignment.Right);
            UiKit.Cellate(header, NumCol, "Net §", UiKit.Head, UIHorizontalAlignment.Right);
            UiKit.Cellate(header, NumCol, "Debt §", UiKit.Head, UIHorizontalAlignment.Right);

            MemberDto[] list = _members != null ? _members.members : null;
            if (list == null || list.Length == 0)
            {
                string note = _loading ? "Loading..."
                    : _failed ? "Couldn't reach the server — try Refresh."
                    : "No members (or not in a league).";
                UIPanel empty = NewRow();
                UiKit.Cellate(empty, Width, note, UiKit.Dim, UIHorizontalAlignment.Left);
                return;
            }

            string me = Settings.AccountIdValue;
            for (int i = 0; i < list.Length; i++)
            {
                MemberDto m = list[i];
                if (m == null) continue;
                bool isMe = m.accountId == me;

                string role = m.isOwner ? "owner" : "member";
                if (isMe) role += " (you)";

                // Prefer the friendly name; fall back to a trimmed id when unnamed. Mark a leaguemate in austerity.
                string label = !string.IsNullOrEmpty(m.displayName) ? m.displayName : OnlineSync.ShortId(m.accountId);
                if (m.austerity) label += " *";

                UIPanel row = NewRow();
                Color32 nameColor = m.austerity ? UiKit.Down : (isMe ? UiKit.Head : UiKit.Flat);
                // Name cell carries an online ●/○ prefix and the full city profile as a hover tooltip.
                UILabel nameCell = UiKit.Cellate(row, NameCol, label, nameColor, UIHorizontalAlignment.Left);
                string dot = m.online
                    ? "<color #6fd36f>●</color> "
                    : "<color #7a7a7a>○</color> ";
                nameCell.processMarkup = true;
                nameCell.text = dot + label;
                nameCell.tooltip = BuildProfileTooltip(m);

                UiKit.Cellate(row, RoleCol, role, m.isOwner ? UiKit.Up : UiKit.Dim, UIHorizontalAlignment.Left);
                Color32 relColor = m.reliability >= 80 ? UiKit.Up : (m.reliability < 50 ? UiKit.Down : UiKit.Flat);
                UiKit.Cellate(row, RelCol, m.reliability + "%", relColor, UIHorizontalAlignment.Right);

                bool hasProfile = HasProfile(m);
                UiKit.Cellate(row, PopCol, hasProfile ? m.population.ToString("N0") : "—",
                    hasProfile ? UiKit.Flat : UiKit.Dim, UIHorizontalAlignment.Right);
                Color32 happyColor = !hasProfile ? UiKit.Dim
                    : (m.happiness >= 70 ? UiKit.Up : (m.happiness < 45 ? UiKit.Down : UiKit.Flat));
                UiKit.Cellate(row, HappyCol, hasProfile ? m.happiness + "%" : "—", happyColor, UIHorizontalAlignment.Right);

                UiKit.Cellate(row, NumCol, Money(m.netCents), m.netCents >= 0 ? UiKit.Up : UiKit.Down, UIHorizontalAlignment.Right);
                UiKit.Cellate(row, NumCol, m.outstandingDebtCents > 0 ? Money(m.outstandingDebtCents) : "—",
                    m.outstandingDebtCents > 0 ? UiKit.Down : UiKit.Dim, UIHorizontalAlignment.Right);
            }
        }

        // Active co-op investments — both RECEIVED ("in your city", shows the issuer) and MADE (by you, shows the
        // grantee) — listed above the roster. Reads CoopBuff (kept fresh by the /citystate poll). MAIN THREAD.
        private void RenderActiveInvestments()
        {
            bool any = false;
            any |= RenderInvestSection("Investments in your city:", CoopBuff.ActiveEffects, true);
            any |= RenderTradeRewardSection("Trade rewards in your city:", CoopBuff.ActiveTradeRewards);
            any |= RenderInvestSection("Investments you've made:", CoopBuff.InvestmentsMade, false);
            if (any) NewRow(); // spacer before the roster header
        }

        // Render one labelled list of investments. showIssuer = attribute to the issuer (received) vs the grantee
        // (made). Returns whether it rendered anything (so the caller knows to add a trailing spacer).
        private bool RenderInvestSection(string title, CityEffectDto[] effects, bool showIssuer)
        {
            if (effects == null || effects.Length == 0) return false;
            UiKit.Cellate(NewRow(), Width, title, UiKit.Head, UIHorizontalAlignment.Left);
            for (int i = 0; i < effects.Length; i++)
            {
                CityEffectDto e = effects[i];
                if (e == null) continue;
                string who = LeagueRoster.Display(showIssuer ? e.issuerId : e.granteeId);
                string verb = showIssuer ? "from " : "to ";
                string text = "  " + verb + who + " — §" + (e.costCents / 100).ToString("N0")
                    + "  (+" + e.demandBoost + " " + CoopBuff.KindLabel(e.demandKind) + " demand, " + e.ticksRemaining + "d left)";
                UiKit.Cellate(NewRow(), Width, text, showIssuer ? UiKit.Up : UiKit.Flat, UIHorizontalAlignment.Left);
            }
            return true;
        }

        private bool RenderTradeRewardSection(string title, CityEffectDto[] effects)
        {
            if (effects == null || effects.Length == 0) return false;
            UiKit.Cellate(NewRow(), Width, title, UiKit.Head, UIHorizontalAlignment.Left);
            for (int i = 0; i < effects.Length; i++)
            {
                CityEffectDto e = effects[i];
                if (e == null) continue;
                string text = "  " + CoopBuff.TradeRewardText(e) + "  (" + e.ticksRemaining + "d left)";
                UiKit.Cellate(NewRow(), Width, text, UiKit.Up, UIHorizontalAlignment.Left);
            }
            return true;
        }

        // True once a member has reported a city profile (vs. just being seen online with no snapshot yet).
        private static bool HasProfile(MemberDto m)
        {
            return m.population > 0 || m.buildingCount > 0 || !string.IsNullOrEmpty(m.cityName);
        }

        // The full per-member profile, shown when hovering the name. Headline stats (online, pop, happy) are columns;
        // everything else (treasury, industry breakdown, etc.) lives here so the narrow roster stays readable.
        private static string BuildProfileTooltip(MemberDto m)
        {
            System.Text.StringBuilder sb = new System.Text.StringBuilder();
            if (!string.IsNullOrEmpty(m.cityName)) sb.Append(m.cityName).Append('\n');
            sb.Append(m.online ? "● online now" : "○ " + LastSeenText(m.lastSeenSec)).Append('\n');

            if (!HasProfile(m))
            {
                sb.Append("(no city profile reported yet)");
                return sb.ToString();
            }

            sb.Append("Pop ").Append(m.population.ToString("N0"))
              .Append("  ·  Happy ").Append(m.happiness).Append('%')
              .Append("  ·  Attract ").Append(m.attractiveness).Append('\n');
            sb.Append("Treasury §").Append(Money(m.cashCents))
              .Append("  (wk +§").Append(Money(m.weeklyIncomeCents))
              .Append(" / -§").Append(Money(m.weeklyExpensesCents)).Append(")\n");
            sb.Append("Zones: R ").Append(m.resBuildings)
              .Append("  C ").Append(m.comBuildings)
              .Append("  O ").Append(m.offBuildings)
              .Append("  I ").Append(m.indBuildings)
              .Append("  (ind workers ").Append(m.indWorkers.ToString("N0")).Append(")\n");
            if (m.farmWorkers + m.forestWorkers + m.oreWorkers + m.oilWorkers > 0)
                sb.Append("Industry workers: farm ").Append(m.farmWorkers.ToString("N0"))
                  .Append(" · forest ").Append(m.forestWorkers.ToString("N0"))
                  .Append(" · ore ").Append(m.oreWorkers.ToString("N0"))
                  .Append(" · oil ").Append(m.oilWorkers.ToString("N0")).Append('\n');
            sb.Append("Unemployment ").Append(m.unemployment).Append('%')
              .Append("  ·  Buildings ").Append(m.buildingCount.ToString("N0"))
              .Append("  ·  Land value ").Append(m.landValue)
              .Append("  ·  Tourism ").Append(m.tourists)
              .Append("  ·  Crime ").Append(m.crime).Append('%');
            return sb.ToString();
        }

        // "last seen 3m/2h/4d ago" from a Unix-seconds timestamp (0 → never). UTC throughout.
        private static string LastSeenText(long unixSec)
        {
            if (unixSec <= 0) return "offline";
            System.DateTime seen = new System.DateTime(1970, 1, 1, 0, 0, 0, System.DateTimeKind.Utc).AddSeconds(unixSec);
            System.TimeSpan ago = System.DateTime.UtcNow - seen;
            if (ago.TotalSeconds < 0) return "online recently";
            if (ago.TotalMinutes < 1) return "last seen just now";
            if (ago.TotalHours < 1) return "last seen " + (int)ago.TotalMinutes + "m ago";
            if (ago.TotalDays < 1) return "last seen " + (int)ago.TotalHours + "h ago";
            return "last seen " + (int)ago.TotalDays + "d ago";
        }

        // ---- investment form ----

        private void Submit()
        {
            if (_sending) return;
            if (string.IsNullOrEmpty(_targetId)) { SetStatus("No leaguemate to invest in."); return; }
            double amt;
            if (!TryParse(_amountField, out amt) || amt < 1000d) { SetStatus("Enter at least §1,000."); return; }
            long cents = (long)(amt * 100.0);
            int days = DaysPresets[_daysIdx];
            string kind = DemandKinds[_demandIdx];
            string kindLabel = DemandLabels[_demandIdx];
            string target = _targetId;
            _sending = true;
            SyncForm();
            SetStatus("Investing...");
            OmApi.Invest(target, cents, days, kind, delegate (bool ok, string err)
            {
                _sending = false;
                SyncForm();
                if (ok) SetStatus("Invested §" + ((long)amt).ToString("N0", CultureInfo.InvariantCulture)
                    + " in " + LeagueRoster.Display(target) + " (" + kindLabel + " demand) for " + days + " days.");
                else SetStatus("Couldn't invest: " + (string.IsNullOrEmpty(err) ? "check the server is reachable." : err));
            });
        }

        // Bail out a leaguemate's defaulted debt (helps them escape austerity). Reuses the target + § amount; the
        // server caps the payment at their outstanding default and reports how much actually applied.
        private void SubmitBailout()
        {
            if (_sending) return;
            if (string.IsNullOrEmpty(_targetId)) { SetStatus("No leaguemate to bail out."); return; }
            if (!TargetInAusterity()) { SetStatus(LeagueRoster.Display(_targetId) + " isn't in austerity."); return; }
            double amt;
            if (!TryParse(_amountField, out amt) || amt < 1d) { SetStatus("Enter a § amount to pay down."); return; }
            long cents = (long)(amt * 100.0);
            string target = _targetId;
            _sending = true;
            SyncForm();
            SetStatus("Bailing out...");
            OmApi.Bailout(target, cents, delegate (bool ok, long applied, string err)
            {
                _sending = false;
                SyncForm();
                if (ok) SetStatus("Bailed out " + LeagueRoster.Display(target) + " by §"
                    + (applied / 100).ToString("N0", CultureInfo.InvariantCulture) + ".");
                else SetStatus("Couldn't bail out: " + (string.IsNullOrEmpty(err) ? "check the server is reachable." : err));
            });
        }

        // Is the currently-picked target flagged in austerity (from the M7 per-member transparency)? Gates the
        // bail-out button so it only offers a rescue to a city that actually owes defaulted debt.
        private bool TargetInAusterity()
        {
            if (string.IsNullOrEmpty(_targetId) || _members == null || _members.members == null) return false;
            for (int i = 0; i < _members.members.Length; i++)
            {
                MemberDto m = _members.members[i];
                if (m != null && m.accountId == _targetId) return m.austerity;
            }
            return false;
        }

        private void SyncForm()
        {
            EnsureTargetValid();
            bool haveTarget = !string.IsNullOrEmpty(_targetId);
            if (_targetBtn != null)
                _targetBtn.text = "Invest in: " + (haveTarget ? LeagueRoster.Display(_targetId) : "(no other members)");
            if (_daysBtn != null) _daysBtn.text = DaysPresets[_daysIdx] + " d";
            if (_demandBtn != null) _demandBtn.text = "Demand: " + DemandLabels[_demandIdx];
            if (_investBtn != null) _investBtn.isEnabled = haveTarget && !_sending;
            if (_bailBtn != null) _bailBtn.isEnabled = haveTarget && !_sending && TargetInAusterity();
            UpdateExplainer(haveTarget);
        }

        // The live, plain-language explainer of the co-op actions + their cost to the player. Shown under the form so
        // the consequence is clear before clicking (the § is a real transfer out of YOUR treasury, not free).
        private void UpdateExplainer(bool haveTarget)
        {
            if (_explainLabel == null) return;
            if (!haveTarget)
            {
                _explainLabel.text = "Invest: give § to a leaguemate (transfers to them) → they get the cash + a boost "
                    + "to the demand type you pick (more § = bigger boost). Bail out: pay down a friend's defaulted debt. Both cost you §.";
                return;
            }
            string name = LeagueRoster.Display(_targetId);
            string kindLabel = DemandLabels[_demandIdx];
            double amt;
            bool valid = TryParse(_amountField, out amt);
            string amtText = valid ? "§" + ((long)amt).ToString("N0", CultureInfo.InvariantCulture) : "§—";
            // Live preview of the demand magnitude the § buys (mirrors the server formula): §2,500/pt, capped at 20.
            string boostText = valid ? "+" + DemandPointsFor((long)(amt * 100.0)) : "+?";
            int days = DaysPresets[_daysIdx];
            string invest = "Invest → " + name + " gets " + amtText + " + " + boostText + " " + kindLabel
                + " demand (& attractiveness) for " + days + " d — costs you " + amtText + ", transferred to them.";
            if (TargetInAusterity())
                _explainLabel.text = invest + " Bail out → pay down " + name + "'s defaulted debt to help them escape austerity.";
            else
                _explainLabel.text = invest + " (" + name + " isn't in austerity, so Bail out is off.)";
        }

        private void CycleTarget()
        {
            List<string> others = OtherMembers();
            if (others.Count == 0) { _targetId = string.Empty; return; }
            int idx = others.IndexOf(_targetId);
            _targetId = others[(idx + 1) % others.Count];
        }

        private void EnsureTargetValid()
        {
            List<string> others = OtherMembers();
            if (others.Count == 0) { _targetId = string.Empty; return; }
            if (!others.Contains(_targetId)) _targetId = others[0];
        }

        private static List<string> OtherMembers()
        {
            List<string> ids = LeagueRoster.MemberIds();
            ids.Remove(Settings.AccountIdValue);
            return ids;
        }

        private void SetStatus(string text) { if (_statusLabel != null) _statusLabel.text = text; }

        private static string Money(long cents) { return (cents / 100).ToString("N0"); }

        // Mirror of the server's cost→demand-points formula (effect.go) for the live preview: floor 1, cap 20.
        private static int DemandPointsFor(long cents)
        {
            long pts = cents / CentsPerDemandPt;
            if (pts < 1) return 1;
            if (pts > DemandPtCap) return DemandPtCap;
            return (int)pts;
        }

        // ---- ui builders (mirrors BondsTab's form widgets) ----

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
            b.disabledBgSprite = "ButtonMenuDisabled";
            b.size = new Vector2(w, 26f);
            b.relativePosition = new Vector3(x, y);
            return b;
        }

        private UITextField Field(float x, float y, float w)
        {
            UITextField tf = _root.AddUIComponent<UITextField>();
            tf.relativePosition = new Vector3(x, y);
            tf.size = new Vector2(w, 26f);
            tf.builtinKeyNavigation = true;
            tf.isInteractive = true;
            tf.readOnly = false;
            tf.canFocus = true;
            tf.numericalOnly = true;
            tf.allowFloats = true;
            tf.padding = new RectOffset(6, 6, 5, 0);
            UIView view = UIView.GetAView();
            if (view != null) tf.atlas = view.defaultAtlas;
            tf.normalBgSprite = "TextFieldPanel";
            tf.hoveredBgSprite = "TextFieldPanelHovered";
            tf.focusedBgSprite = "TextFieldPanel";
            tf.selectionSprite = "EmptySprite";
            tf.textScale = 0.8f;
            tf.textColor = UiKit.Flat;
            tf.color = Color.white;
            tf.horizontalAlignment = UIHorizontalAlignment.Left;
            return tf;
        }

        private void Caption(string text, float x, float y)
        {
            UILabel l = _root.AddUIComponent<UILabel>();
            l.autoSize = true;
            l.relativePosition = new Vector3(x, y + 4f);
            l.textScale = 0.78f;
            l.textColor = UiKit.Head;
            l.text = text;
        }

        private UILabel SmallLabel(float x, float y, Color32 color)
        {
            UILabel l = _root.AddUIComponent<UILabel>();
            l.autoSize = true;
            l.relativePosition = new Vector3(x, y);
            l.textScale = 0.74f;
            l.textColor = color;
            l.text = " ";
            return l;
        }

        private static bool TryParse(UITextField field, out double value)
        {
            value = 0d;
            if (field == null) return false;
            return double.TryParse(field.text, NumberStyles.Float, CultureInfo.InvariantCulture, out value);
        }
    }
}
