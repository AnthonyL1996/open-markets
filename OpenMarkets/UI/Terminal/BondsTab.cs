using System.Collections.Generic;
using System.Globalization;
using ColossalFramework.UI;
using OpenMarkets.Net;
using UnityEngine;

namespace OpenMarkets.UI.Terminal
{
    /// <summary>
    /// Bonds tab: the credit ledger AND the manual-loan negotiation surface (M5 Phase 3). The top strip is an
    /// "issue loan" form (lend or borrow; counterparty, principal, rate, installments) that also doubles as the
    /// COUNTER editor — clicking "counter" on an incoming offer loads its terms here to adjust and re-propose.
    /// Below it: a "Loan offers" section (accept / decline / counter when it's your turn, cancel when it's
    /// theirs) and then the active debt ledger — "You owe" / "Owed to you" with terms, status, repayment
    /// progress, and a "repay" button. An AUSTERITY banner shows when the city owes a defaulted bond.
    ///
    /// The form controls are built ONCE and mutated; the grid rebuilds from the fetched bonds on each change.
    /// MAIN THREAD. Repay/accept book cash via the contiguous /settlements poll (RequestSettlementPoll).
    /// </summary>
    internal sealed class BondsTab : ITabBody
    {
        private const float FormH = 92f;
        private const float DescCol = 300f;
        private const float StatusCol = 92f;
        private const float BarCol = 70f;
        private const float BtnCol = 70f;
        private const float Width = DescCol + StatusCol + BarCol + BtnCol;

        // Negotiable interest choices (bps) — loans have no floor (unlike a trade's default rate).
        private static readonly int[] RateBps = { 500, 1000, 1500, 2000, 3000, 5000 };

        private UIPanel _root;
        private UIScrollablePanel _grid;
        private UIButton _roleBtn;
        private UIButton _counterpartyBtn;
        private UITextField _principalField;
        private UIButton _rateBtn;
        private UIButton _installmentsBtn;
        private UIButton _offerBtn;
        private UILabel _modeLabel;

        private BondDto[] _bonds;
        private CityStateDto _city;
        private bool _loading;
        private bool _cityLoading;
        private string _status = string.Empty;

        // Form state.
        private bool _iLend = true;
        private string _counterpartyId = string.Empty;
        private int _rateIdx;
        private int _installments = 4;
        private string _counterTargetId = string.Empty; // empty = new offer; else countering this bond
        private bool _sending;

        public string TabLabel { get { return "Bonds"; } }
        public string Title { get { return "Open Markets — bonds & loans"; } }

        public void Build(UIComponent host, Vector2 size)
        {
            _root = host.AddUIComponent<UIPanel>();
            _root.relativePosition = Vector3.zero;
            _root.size = size;
            _root.autoLayout = false;

            // Issue-loan / counter form.
            _roleBtn = MakeButton(2f, 4f, 92f, "I lend");
            _roleBtn.eventClicked += delegate { _iLend = !_iLend; SyncForm(); };
            _counterpartyBtn = MakeButton(98f, 4f, 200f, "—");
            _counterpartyBtn.eventClicked += delegate { CycleCounterparty(); SyncForm(); };

            Caption("§", 2f, 38f);
            _principalField = Field(20f, 36f, 90f);
            _rateBtn = MakeButton(116f, 36f, 64f, "—");
            _rateBtn.eventClicked += delegate { _rateIdx = (_rateIdx + 1) % RateBps.Length; SyncForm(); };
            _installmentsBtn = MakeButton(184f, 36f, 64f, "x 4");
            _installmentsBtn.eventClicked += delegate { _installments = _installments >= 12 ? 1 : _installments + 1; SyncForm(); };
            _offerBtn = MakeButton(252f, 36f, 100f, "Offer loan");
            _offerBtn.eventClicked += delegate { Submit(); };
            UiKit.Primary(_offerBtn); // the call-to-action

            _modeLabel = SmallLabel(2f, 68f, UiKit.Dim);

            // Rule between the issue/counter form and the ledger below, so the two regions read as distinct.
            UiKit.Divider(_root, 0f, FormH - 4f, size.x);

            _grid = _root.AddUIComponent<UIScrollablePanel>();
            _grid.relativePosition = new Vector3(0f, FormH);
            _grid.size = new Vector2(size.x, size.y - FormH);
            _grid.autoLayout = true;
            _grid.autoLayoutDirection = LayoutDirection.Vertical;
            _grid.autoLayoutPadding = new RectOffset(0, 0, 0, 2);
            _grid.clipChildren = true;
            _grid.scrollWheelDirection = UIOrientation.Vertical;

            EnsureCounterpartyValid();
            SyncForm();
        }

        public void SetVisible(bool on) { if (_root != null) _root.isVisible = on; }

        public void Refresh()
        {
            if (_root == null) return;
            EnsureCounterpartyValid();
            SyncForm();
            Rebuild();
            if (!_loading)
            {
                _loading = true;
                OmApi.GetBonds(delegate (bool ok, BondListDto list)
                {
                    _loading = false;
                    if (ok && list != null) _bonds = list.bonds;
                    Rebuild();
                });
            }
            if (!_cityLoading)
            {
                _cityLoading = true;
                OmApi.GetCityState(delegate (bool ok, CityStateDto cs)
                {
                    _cityLoading = false;
                    if (ok) _city = cs;
                    Rebuild();
                });
            }
        }

        // ---- grid ----

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
            if (!string.IsNullOrEmpty(_status))
                UiKit.Cellate(NewRow(), Width, _status, UiKit.Down, UIHorizontalAlignment.Left);

            if (_city != null && _city.austerity)
            {
                // Merged: the new BannerCell styling (UI polish) + the M8 lever detail (taxes/demand/budget).
                string banner = "  AUSTERITY — income garnished daily until §" + Cash(_city.outstandingDebtCents)
                    + " of defaulted debt clears (" + _city.defaultedBonds + " bond(s)). Taxes forced to "
                    + TaxLock.AusterityRate + "%, demand cut " + DemandLever.DemandCutPct + "%, service budgets capped at "
                    + BudgetLock.BudgetCeilingPct + "% until it clears.";
                UiKit.BannerCell(NewRow(), Width, banner);
            }

            Header("Loan offers");
            int offers = RenderOffers(me);
            if (offers == 0) Note("No loan offers.");

            Header("You owe");
            int owe = RenderActive(me, true);
            if (owe == 0) Note("No debts.");

            Header("Owed to you");
            int owed = RenderActive(me, false);
            if (owed == 0) Note("Nobody owes you.");
        }

        // Render negotiating (status "offered", origin manual) loan offers involving me.
        private int RenderOffers(string me)
        {
            if (_bonds == null) return 0;
            int n = 0;
            for (int i = 0; i < _bonds.Length; i++)
            {
                BondDto b = _bonds[i];
                if (b == null || b.status != "offered" || b.origin != "manual") continue;
                if (b.creditorId != me && b.debtorId != me) continue;
                n++;
                bool iLend = b.creditorId == me;
                string other = iLend ? b.debtorId : b.creditorId;
                bool myTurn = b.proposedBy != me;
                string desc = (iLend ? "Lend " : "Borrow ") + "§" + Cash(b.principalCents) + " @ " + (b.interestBps / 100) + "% x"
                    + b.installments + "  w/ " + LeagueRoster.Display(other)
                    + (myTurn ? "  (your turn)" : "  (waiting on them)");
                UIPanel row = NewRow();
                // Accent the rows that need YOUR action; leave the ones waiting on them plain.
                UiKit.Cellate(row, DescCol, desc, myTurn ? UiKit.Accent : UiKit.Flat, UIHorizontalAlignment.Left);
                string id = b.id;
                if (myTurn)
                {
                    UiKit.Primary(AddButton(row, "accept", delegate { AcceptOffer(id); }));
                    AddButton(row, "counter", delegate { LoadForCounter(id); });
                    AddButton(row, "decline", delegate { LoanAction(id, "decline"); });
                }
                else
                {
                    AddButton(row, "cancel", delegate { LoanAction(id, "cancel"); });
                }
            }
            return n;
        }

        // Render active/terminal bonds I owe (asDebtor) or am owed; skips offered (those are in RenderOffers).
        private int RenderActive(string me, bool asDebtor)
        {
            if (_bonds == null) return 0;
            int n = 0;
            for (int i = 0; i < _bonds.Length; i++)
            {
                BondDto b = _bonds[i];
                if (b == null || b.status == "offered") continue;
                bool iAmDebtor = b.debtorId == me;
                if (iAmDebtor != asDebtor) continue;
                n++;
                string other = asDebtor ? b.creditorId : b.debtorId;
                string desc = (asDebtor ? "Owe " : "Owed by ") + LeagueRoster.Display(other)
                    + ": §" + Cash(b.totalDueCents) + " @ " + (b.interestBps / 100) + "%"
                    + (b.origin != null && b.origin.StartsWith("trade:") ? "  (from a trade)" : string.Empty);
                UIPanel row = NewRow();
                UiKit.Cellate(row, DescCol, desc, UiKit.Flat, UIHorizontalAlignment.Left);
                UiKit.Cellate(row, StatusCol, b.status, StatusColor(b.status), UIHorizontalAlignment.Left);

                UIPanel barCell = row.AddUIComponent<UIPanel>();
                barCell.size = new Vector2(BarCol, UiKit.RowH);
                barCell.autoLayout = false;
                UiKit.SegmentBar(barCell, BarCol - 6f, UiKit.RowH - 8f, b.settled, b.installments, UiKit.Up, UiKit.Dim)
                    .relativePosition = new Vector3(2f, 4f);

                if (asDebtor && b.settled < b.installments && (b.status == "active" || b.status == "delinquent"))
                {
                    string id = b.id;
                    AddButton(row, "repay", delegate { Repay(id); });
                }
                else
                {
                    UiKit.Cellate(row, BtnCol, string.Empty, UiKit.Dim, UIHorizontalAlignment.Left);
                }
            }
            return n;
        }

        // ---- form actions ----

        private void Submit()
        {
            if (_sending) return;
            EnsureCounterpartyValid();
            if (string.IsNullOrEmpty(_counterpartyId)) { SetStatus("Pick a counterparty (no other league members yet)."); return; }
            double principal;
            if (!TryParse(_principalField, out principal) || principal <= 0) { SetStatus("Enter a principal greater than 0."); return; }
            long principalCents = (long)(principal * 100.0 + 0.5);

            SetSending(true);
            if (string.IsNullOrEmpty(_counterTargetId))
            {
                LoanOfferDto offer = new LoanOfferDto
                {
                    leagueId = Settings.LeagueIdValue,
                    role = _iLend ? "lend" : "borrow",
                    counterparty = _counterpartyId,
                    principalCents = principalCents,
                    interestBps = RateBps[_rateIdx],
                    installments = _installments
                };
                OmApi.OfferLoan(offer, delegate (bool ok, BondDto created, string error)
                {
                    SetSending(false);
                    if (ok) { SetStatus("Loan offer sent."); ClearForm(); Refresh(); }
                    else SetStatus("Offer rejected: " + Reason(error));
                });
            }
            else
            {
                LoanTermsDto terms = new LoanTermsDto
                {
                    principalCents = principalCents,
                    interestBps = RateBps[_rateIdx],
                    installments = _installments
                };
                OmApi.CounterLoan(_counterTargetId, terms, delegate (bool ok, BondDto updated, string error)
                {
                    SetSending(false);
                    if (ok) { SetStatus("Counter sent."); ClearForm(); Refresh(); }
                    else SetStatus("Counter rejected: " + Reason(error));
                });
            }
        }

        // Load an incoming offer's terms into the form so the player can adjust and counter it.
        private void LoadForCounter(string bondId)
        {
            BondDto b = Find(bondId);
            if (b == null) return;
            _counterTargetId = bondId;
            _iLend = b.creditorId == Settings.AccountIdValue;
            _counterpartyId = _iLend ? b.debtorId : b.creditorId;
            // Keep the fractional § so an unchanged Counter re-submits the exact principal (no cent truncation).
            if (_principalField != null) _principalField.text = (b.principalCents / 100.0).ToString("0.##", CultureInfo.InvariantCulture);
            _rateIdx = NearestRate(b.interestBps);
            _installments = b.installments >= 1 && b.installments <= 12 ? b.installments : 4;
            SetStatus("Countering — adjust terms and press Counter.");
            SyncForm();
        }

        private void AcceptOffer(string bondId)
        {
            OmApi.AcceptLoan(bondId, delegate (bool ok, BondSettleResultDto res, string error)
            {
                // Principal books via the contiguous /settlements poll, not the isolated response event.
                if (ok) { _status = string.Empty; OnlineSync.RequestSettlementPoll(); Refresh(); }
                else { _status = "Accept failed: " + Reason(error); Rebuild(); }
            });
        }

        private void LoanAction(string bondId, string action)
        {
            OmApi.LoanTransition(bondId, action, delegate (bool ok, BondDto updated, string error)
            {
                if (ok) { _status = string.Empty; if (_counterTargetId == bondId) ClearForm(); Refresh(); }
                else { _status = action + " failed: " + Reason(error); Rebuild(); }
            });
        }

        private void Repay(string bondId)
        {
            OmApi.SettleBond(bondId, delegate (bool ok, BondSettleResultDto res, string error)
            {
                if (ok) { _status = string.Empty; OnlineSync.RequestSettlementPoll(); Refresh(); }
                else { _status = "Repay failed: " + Reason(error); Rebuild(); }
            });
        }

        // ---- form helpers ----

        private void SetSending(bool on)
        {
            _sending = on;
            if (_offerBtn != null) _offerBtn.isEnabled = !on;
        }

        private void ClearForm()
        {
            _counterTargetId = string.Empty;
            if (_principalField != null) _principalField.text = string.Empty;
            SyncForm();
        }

        private void SyncForm()
        {
            if (_roleBtn != null) _roleBtn.text = _iLend ? "I lend" : "I borrow";
            if (_counterpartyBtn != null)
                _counterpartyBtn.text = string.IsNullOrEmpty(_counterpartyId)
                    ? "(no other members)" : LeagueRoster.Display(_counterpartyId);
            if (_rateBtn != null) _rateBtn.text = (RateBps[_rateIdx] / 100) + "%";
            if (_installmentsBtn != null) _installmentsBtn.text = "x " + _installments;
            if (_offerBtn != null) _offerBtn.text = string.IsNullOrEmpty(_counterTargetId) ? "Offer loan" : "Counter";
            if (_modeLabel != null)
                _modeLabel.text = string.IsNullOrEmpty(_counterTargetId) ? "New loan offer" : "Countering an offer";
        }

        private void CycleCounterparty()
        {
            List<string> others = OtherMembers();
            if (others.Count == 0) { _counterpartyId = string.Empty; return; }
            int idx = others.IndexOf(_counterpartyId);
            _counterpartyId = others[(idx + 1) % others.Count];
        }

        private void EnsureCounterpartyValid()
        {
            List<string> others = OtherMembers();
            if (others.Count == 0) { _counterpartyId = string.Empty; return; }
            if (!others.Contains(_counterpartyId)) _counterpartyId = others[0];
        }

        private static List<string> OtherMembers()
        {
            List<string> ids = LeagueRoster.MemberIds();
            ids.Remove(Settings.AccountIdValue);
            return ids;
        }

        private BondDto Find(string id)
        {
            if (_bonds == null) return null;
            for (int i = 0; i < _bonds.Length; i++)
                if (_bonds[i] != null && _bonds[i].id == id) return _bonds[i];
            return null;
        }

        private static int NearestRate(long bps)
        {
            int best = 0;
            long bestDiff = long.MaxValue;
            for (int i = 0; i < RateBps.Length; i++)
            {
                long d = bps - RateBps[i];
                if (d < 0) d = -d;
                if (d < bestDiff) { bestDiff = d; best = i; }
            }
            return best;
        }

        private static string Reason(string error)
        {
            return string.IsNullOrEmpty(error) ? "check the server is reachable." : error;
        }

        private static Color32 StatusColor(string status)
        {
            switch (status)
            {
                case "completed":
                case "cleared":
                    return UiKit.Up;
                case "delinquent":
                case "defaultedReceivable":
                case "writtenOff":
                    return UiKit.Down;
                default:
                    return UiKit.Flat;
            }
        }

        // ---- ui builders ----

        private void Header(string text) { UiKit.Cellate(NewRow(), Width, text, UiKit.Head, UIHorizontalAlignment.Left); }
        private void Note(string text) { UiKit.Cellate(NewRow(), Width, text, UiKit.Dim, UIHorizontalAlignment.Left); }

        private UIButton AddButton(UIPanel row, string text, MouseEventHandler onClick)
        {
            UIButton b = row.AddUIComponent<UIButton>();
            b.text = text;
            b.textScale = 0.7f;
            b.normalBgSprite = "ButtonMenu";
            b.hoveredBgSprite = "ButtonMenuHovered";
            b.size = new Vector2(BtnCol - 4f, UiKit.RowH - 2f);
            b.eventClicked += onClick;
            return b;
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

        private void SetStatus(string text) { _status = text; if (_grid != null) Rebuild(); }

        private static string Cash(long cents)
        {
            return (cents / 100).ToString("N0", CultureInfo.InvariantCulture);
        }
    }
}
