package store

import (
	"log"
	"sort"
	"strconv"
	"time"

	"openmarkets/server/internal/id"
	"openmarkets/server/internal/money"
)

// EconParams are the league economy knobs governing the bond economy. Defaults are the "harsh" posture chosen
// for v1 (TRADE-SCREEN.md / PLAN-trade-bonds.md): a 20% minimum default rate, a 6-installment auto-bond term,
// and a 2-miss tolerance before a bond defaults terminally.
type EconParams struct {
	MinDefaultRateBps        int64 // floor on a trade's negotiated default rate (Codex #2 guardrail)
	AutoBondTermInstallments int   // schedule length of an auto-issued default bond
	MinPrincipalCents        int64 // dust floor: shortfalls below this are forgiven, not bonded
	BondMaxMisses            int   // consecutive missed bond installments before terminal default
	// Austerity (Phase 3): a city with a terminally-defaulted bond is garnished each due tick by the
	// income-independent GarnishMinWriteDownCents (guarantees the debt monotonically clears → escapable). After
	// AusterityMaxTicks garnish ticks any remainder is written off (the timebox backstop).
	GarnishMinWriteDownCents int64
	AusterityMaxTicks        int
}

// DefaultEconParams returns the harsh starting knobs (tunable per league later).
func DefaultEconParams() EconParams {
	return EconParams{
		MinDefaultRateBps:        2000, // 20%
		AutoBondTermInstallments: 6,
		MinPrincipalCents:        100, // §1.00
		BondMaxMisses:            2,
		GarnishMinWriteDownCents: 50000, // §500 / tick floor
		AusterityMaxTicks:        30,    // timebox write-off backstop
	}
}

// SetEconParams overrides the economy knobs. Startup-time; safe before serving.
func (m *Memory) SetEconParams(p EconParams) {
	// Guard the austerity escapability invariant: with a zero garnish floor or timebox, a defaulted bond would
	// never advance a garnish tick, so the timebox write-off would never fire and the city could never escape.
	if p.GarnishMinWriteDownCents < 1 {
		p.GarnishMinWriteDownCents = 1
	}
	if p.AusterityMaxTicks < 1 {
		p.AusterityMaxTicks = 1
	}
	m.mu.Lock()
	m.econ = p
	m.mu.Unlock()
}

// ── Trade lifecycle ──────────────────────────────────────────────────────────

// CreateTrade validates and stores a new offered trade. Both parties must be league members and differ; the
// basket must be two-sided with sane installments and a default rate at/above the league floor. Line values are
// NOT frozen here — they're frozen at accept (mark-to-accept, Codex #7).
func (m *Memory) CreateTrade(t Trade) (Trade, error) {
	if t.OfferedBy == "" || t.Counterparty == "" || t.OfferedBy == t.Counterparty {
		return Trade{}, ErrConflict
	}
	if err := t.ValidateForOffer(m.econ.MinDefaultRateBps); err != nil {
		return Trade{}, err
	}
	m.mu.Lock()
	if !m.isMemberLocked(t.OfferedBy, t.LeagueID) || !m.isMemberLocked(t.Counterparty, t.LeagueID) {
		m.mu.Unlock()
		return Trade{}, ErrNotFound
	}
	t.ID = id.New()
	t.Status = TradeOffered
	t.Settled = 0
	for i := range t.Items { // ensure no caller-supplied frozen values leak in
		t.Items[i].ValueCentsAtAccept = 0
		t.Items[i].UnitPriceCents = 0
	}
	t.Created = m.clock()
	m.trades[t.ID] = t
	m.mu.Unlock()
	return t, m.persist()
}

func (m *Memory) GetTrade(idStr string) (Trade, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	t, ok := m.trades[idStr]
	if !ok {
		return Trade{}, ErrNotFound
	}
	return t, nil
}

// TradesFor lists the league's trades that involve accountID (order unspecified; clients sort).
func (m *Memory) TradesFor(leagueID, accountID string) ([]Trade, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []Trade
	for _, t := range m.trades {
		if t.LeagueID == leagueID && (t.OfferedBy == accountID || t.Counterparty == accountID) {
			out = append(out, t)
		}
	}
	return out, nil
}

// TradeVolumeByLeague counts each member's COMPLETED trades in a league, keyed by accountID (a completed trade is
// counted for BOTH its parties). Computed in a single pass like MarketMoverByAccount so the global leaderboard can
// fetch it once per league instead of calling TradesFor per member (which re-scans every trade per member). A trade
// with the same party on both sides (shouldn't happen) is counted once. Read-only; takes the lock and returns a copy.
func (m *Memory) TradeVolumeByLeague(leagueID string) map[string]int64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := map[string]int64{}
	for _, t := range m.trades {
		if t.LeagueID != leagueID || t.Status != TradeCompleted {
			continue
		}
		out[t.OfferedBy]++
		if t.Counterparty != t.OfferedBy {
			out[t.Counterparty]++
		}
	}
	return out
}

// SetTradeStatus performs the atomic accept/decline/cancel transition. On accept it FREEZES line values using
// the injected Pricer (mark-to-accept) and anchors the due clock.
//
// The freeze runs OUTSIDE the store lock so the Pricer may safely read the store (e.g. league reports → index)
// without deadlocking; the commit then re-checks the trade is still in the expected pre-state (guards the rare
// concurrent transition at friend scale).
func (m *Memory) SetTradeStatus(accountID, tradeID, action string) (Trade, error) {
	m.mu.Lock()
	t, ok := m.trades[tradeID]
	if !ok || (accountID != t.OfferedBy && accountID != t.Counterparty) {
		m.mu.Unlock()
		return Trade{}, ErrNotFound
	}
	prior := t.Status
	next, err := t.NextTradeStatus(accountID, action)
	pricer := m.pricer
	m.mu.Unlock()
	if err != nil {
		return Trade{}, err
	}

	if next == TradeActive { // freeze values + anchor the due clock, lock-free
		if pricer == nil {
			return Trade{}, ErrConflict
		}
		lid := t.LeagueID
		if err := t.FreezeValues(func(c string) (int64, bool) { return pricer(lid, c) }); err != nil {
			return Trade{}, err
		}
		if err := t.PrepareActiveErr(); err != nil {
			return Trade{}, err
		}
		t.AcceptedDay = m.clock().Unix()
		t.Settled = 0
	}
	t.Status = next

	m.mu.Lock()
	cur, ok := m.trades[tradeID]
	if !ok || cur.Status != prior { // someone transitioned it while we were pricing
		m.mu.Unlock()
		return Trade{}, ErrConflict
	}
	m.trades[tradeID] = t
	m.mu.Unlock()
	return t, m.persist()
}

// ── Settle loop (gross-evaluate / net-book; financial-only v1) ────────────────

// SettleTradeInstallment books the current installment's NET cash transfer as a settlement event (the net payer
// confirms payment). Both clients later apply the event idempotently. Completes the trade on the last
// installment. Only the net payer may settle a nonzero installment; a zero-net installment may be advanced by
// either party.
func (m *Memory) SettleTradeInstallment(accountID, tradeID string) (Trade, SettlementEvent, error) {
	m.mu.Lock()
	defer m.persistAfter()
	t, ok := m.trades[tradeID]
	if !ok || (accountID != t.OfferedBy && accountID != t.Counterparty) {
		m.mu.Unlock()
		return Trade{}, SettlementEvent{}, ErrNotFound
	}
	if t.Status != TradeActive || t.Settled >= t.Installments {
		m.mu.Unlock()
		return Trade{}, SettlementEvent{}, ErrConflict
	}
	sched, err := t.InstallmentSchedule()
	if err != nil {
		m.mu.Unlock()
		return Trade{}, SettlementEvent{}, ErrConflict
	}
	payer, receiver := t.NetPayerReceiver()
	cents := sched[t.Settled]
	var ev SettlementEvent
	if cents > 0 {
		if accountID != payer {
			m.mu.Unlock()
			return Trade{}, SettlementEvent{}, ErrConflict
		}
		ev = m.appendEventLocked(t.LeagueID, payer, receiver, cents, "trade:"+t.ID+":"+strconv.Itoa(t.Settled))
	}
	t.Settled++
	if t.Settled >= t.Installments {
		t.Status = TradeCompleted
	}
	if cents > 0 {
		m.bumpReliabilityLocked(payer, true) // a real (non-zero) installment met on time
	}
	m.trades[tradeID] = t
	m.mu.Unlock()
	return t, ev, nil
}

// AutoSettleTradeInstallment books the current installment automatically, server-side, when it comes due on the
// due-clock — no party has to initiate it. This is the default settlement path once a trade is AGREED: each
// installment's net cash transfer (payer → receiver) is booked when due, and both clients apply it idempotently
// off the settlement feed, so payment is automatic, not a manual action (and an offline payer still settles —
// the event waits in the feed and reconciles on return, closing the dodge hole without an auto-bond). Mirrors
// SettleTradeInstallment but takes no caller: the server acts on the net payer's behalf. Completes the trade on
// the last installment. A zero-net installment just advances the counter.
func (m *Memory) AutoSettleTradeInstallment(tradeID string) (Trade, SettlementEvent, error) {
	m.mu.Lock()
	defer m.persistAfter()
	t, ok := m.trades[tradeID]
	if !ok {
		m.mu.Unlock()
		return Trade{}, SettlementEvent{}, ErrNotFound
	}
	if t.Status != TradeActive || t.Settled >= t.Installments {
		m.mu.Unlock()
		return Trade{}, SettlementEvent{}, ErrConflict
	}
	sched, err := t.InstallmentSchedule()
	if err != nil {
		m.mu.Unlock()
		return Trade{}, SettlementEvent{}, ErrConflict
	}
	payer, receiver := t.NetPayerReceiver()
	cents := sched[t.Settled]
	var ev SettlementEvent
	if cents > 0 {
		ev = m.appendEventLocked(t.LeagueID, payer, receiver, cents, "trade:"+t.ID+":"+strconv.Itoa(t.Settled))
		m.bumpReliabilityLocked(payer, true) // settled on the due clock = a met installment
	}
	t.Settled++
	if t.Settled >= t.Installments {
		t.Status = TradeCompleted
	}
	m.trades[tradeID] = t
	m.mu.Unlock()
	return t, ev, nil
}

// MissTradeInstallment is the due-clock consequence of an unmet installment: the net payer's unpaid amount is
// auto-converted into a bond (debtor = the payer who failed, creditor = the receiver) at the trade's negotiated
// default rate ("always bond the shortfall"). Dust below the min principal is forgiven. Advances/completes the
// trade. Returns the created bond (zero-value if none).
func (m *Memory) MissTradeInstallment(tradeID string) (Trade, Bond, error) {
	m.mu.Lock()
	defer m.persistAfter()
	t, ok := m.trades[tradeID]
	if !ok {
		m.mu.Unlock()
		return Trade{}, Bond{}, ErrNotFound
	}
	if t.Status != TradeActive || t.Settled >= t.Installments {
		m.mu.Unlock()
		return Trade{}, Bond{}, ErrConflict
	}
	sched, err := t.InstallmentSchedule()
	if err != nil {
		m.mu.Unlock()
		return Trade{}, Bond{}, ErrConflict
	}
	payer, receiver := t.NetPayerReceiver()
	cents := sched[t.Settled]
	var b Bond
	if cents >= m.econ.MinPrincipalCents { // below the dust floor → forgiven, no bond
		b, err = NewAutoBond(id.New(), t.LeagueID, receiver, payer, t.ID, cents,
			t.DefaultRateBps, m.econ.AutoBondTermInstallments, m.econ.MinPrincipalCents, m.clock())
		if err != nil {
			m.mu.Unlock()
			return Trade{}, Bond{}, err
		}
		m.bonds[b.ID] = b
		m.bumpReliabilityLocked(payer, false) // missed → an auto-bond was minted (dust/zero misses don't count)
	}
	t.Settled++
	if t.Settled >= t.Installments {
		t.Status = TradeCompleted
	}
	m.trades[tradeID] = t
	m.mu.Unlock()
	return t, b, nil
}

// ReportTradeShortfall records that `accountID` could not physically deliver some give-goods for `installment`
// (M6 give-side delivery). The undelivered value is converted into a cash-debt bond (debtor = the caller who
// failed, creditor = the other party) at the trade's negotiated default rate, over a single installment, with
// no principal transfer — exactly the "always bond the shortfall" rule, mirroring MissTradeInstallment. The
// reported cents are capped at the caller's frozen commodity GIVE value for that installment (so a client can't
// over-report), dust below the min principal is forgiven, and a reliability ding is recorded. Idempotent per
// (trade, installment): a repeat report returns ErrConflict. Cash settlement is unaffected — this is a separate,
// additive goods obligation. Returns the (unchanged-status) trade and the minted bond (zero-value if forgiven).
func (m *Memory) ReportTradeShortfall(accountID, tradeID string, installment int, reportedCents int64) (Trade, Bond, error) {
	m.mu.Lock()
	defer m.persistAfter()
	t, ok := m.trades[tradeID]
	if !ok || (accountID != t.OfferedBy && accountID != t.Counterparty) {
		m.mu.Unlock()
		return Trade{}, Bond{}, ErrNotFound
	}
	if t.Status != TradeActive || installment < 0 || installment >= t.Installments {
		m.mu.Unlock()
		return Trade{}, Bond{}, ErrConflict
	}
	for _, s := range t.ShortfallInstallments { // already reported → idempotent no-op
		if s == installment {
			m.mu.Unlock()
			return Trade{}, Bond{}, ErrConflict
		}
	}

	// Cap at the caller's frozen commodity give value for THIS installment (amortized share).
	cents := reportedCents
	if cents < 0 {
		cents = 0
	}
	total := t.CommodityGiveValueCents(accountID)
	if total > 0 {
		if sched, err := money.Amortize(total, t.Installments); err == nil && installment < len(sched) {
			if cents > sched[installment] {
				cents = sched[installment]
			}
		}
	} else {
		cents = 0 // gave no commodities → nothing to under-deliver
	}

	var b Bond
	if cents >= m.econ.MinPrincipalCents { // below the dust floor → forgiven, no bond
		creditor := t.OfferedBy
		if accountID == t.OfferedBy {
			creditor = t.Counterparty
		}
		b = Bond{
			ID:             id.New(),
			LeagueID:       t.LeagueID,
			CreditorID:     creditor,
			DebtorID:       accountID,
			PrincipalCents: cents,
			InterestBps:    t.DefaultRateBps,
			Installments:   1,
			Origin:         "trade-shortfall:" + t.ID,
			Created:        m.clock(),
		}
		if err := b.Activate(m.econ.MinPrincipalCents); err != nil {
			m.mu.Unlock()
			return Trade{}, Bond{}, err
		}
		m.bonds[b.ID] = b
		m.bumpReliabilityLocked(accountID, false) // under-delivered → reliability ding
	}

	t.ShortfallInstallments = append(t.ShortfallInstallments, installment)
	m.trades[tradeID] = t
	m.mu.Unlock()
	return t, b, nil
}

// ListActiveTrades returns every trade still settling (active, with installments left), across all leagues —
// the due-clock ticker's work list.
func (m *Memory) ListActiveTrades() []Trade {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []Trade
	for _, t := range m.trades {
		if t.Status == TradeActive && t.Settled < t.Installments {
			out = append(out, t)
		}
	}
	return out
}

// ListActiveBonds returns every bond still open (active or delinquent, with installments left) — the ticker's
// bond work list.
func (m *Memory) ListActiveBonds() []Bond {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []Bond
	for _, b := range m.bonds {
		if (b.Status == BondActive || b.Status == BondDelinquent) && b.Settled < b.Installments {
			out = append(out, b)
		}
	}
	return out
}

// ── Bonds ────────────────────────────────────────────────────────────────────

func (m *Memory) GetBond(idStr string) (Bond, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	b, ok := m.bonds[idStr]
	if !ok {
		return Bond{}, ErrNotFound
	}
	return b, nil
}

// BondsFor lists the league's bonds where accountID is creditor or debtor.
func (m *Memory) BondsFor(leagueID, accountID string) ([]Bond, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []Bond
	for _, b := range m.bonds {
		if b.LeagueID == leagueID && (b.CreditorID == accountID || b.DebtorID == accountID) {
			out = append(out, b)
		}
	}
	return out, nil
}

// SettleBondInstallment books one bond repayment (debtor → creditor) as a settlement event, curing any
// delinquency and completing the bond on the final installment. Only the debtor may settle.
func (m *Memory) SettleBondInstallment(accountID, bondID string) (Bond, SettlementEvent, error) {
	m.mu.Lock()
	defer m.persistAfter()
	b, ok := m.bonds[bondID]
	if !ok || accountID != b.DebtorID {
		m.mu.Unlock()
		return Bond{}, SettlementEvent{}, ErrNotFound
	}
	if b.Status != BondActive && b.Status != BondDelinquent {
		m.mu.Unlock()
		return Bond{}, SettlementEvent{}, ErrConflict
	}
	if b.Settled >= b.Installments {
		m.mu.Unlock()
		return Bond{}, SettlementEvent{}, ErrConflict
	}
	sched, err := b.Schedule()
	if err != nil {
		m.mu.Unlock()
		return Bond{}, SettlementEvent{}, ErrConflict
	}
	cents := sched[b.Settled]
	onTime := b.Status == BondActive // paying while already delinquent doesn't earn on-time credit (Codex)
	ev := m.appendEventLocked(b.LeagueID, b.DebtorID, b.CreditorID, cents, "bond:"+b.ID+":"+strconv.Itoa(b.Settled))
	b.Settled++
	b.Cure() // catching up clears delinquency
	if b.Settled >= b.Installments {
		b.Status = BondCompleted
	}
	if onTime {
		m.bumpReliabilityLocked(accountID, true) // debtor repaid on time (not a late catch-up)
	}
	m.bonds[bondID] = b
	m.mu.Unlock()
	return b, ev, nil
}

// MissBondInstallment is the due-clock consequence for a bond: it records a miss, going delinquent and then
// terminal (defaultedReceivable, balance frozen) after BondMaxMisses. Bonds never recurse into new bonds.
func (m *Memory) MissBondInstallment(bondID string) (Bond, bool, error) {
	m.mu.Lock()
	defer m.persistAfter()
	b, ok := m.bonds[bondID]
	if !ok {
		m.mu.Unlock()
		return Bond{}, false, ErrNotFound
	}
	if b.Status != BondActive && b.Status != BondDelinquent {
		m.mu.Unlock()
		return Bond{}, false, ErrConflict
	}
	defaulted := b.RegisterMiss(m.econ.BondMaxMisses)
	m.bumpReliabilityLocked(b.DebtorID, false) // debtor missed a repayment
	m.bonds[bondID] = b
	m.mu.Unlock()
	return b, defaulted, nil
}

// ── Austerity (Phase 3) ──────────────────────────────────────────────────────

// ListDefaultedBonds returns every terminally-defaulted bond (across leagues) — the austerity sweep's work list.
func (m *Memory) ListDefaultedBonds() []Bond {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []Bond
	for _, b := range m.bonds {
		if b.Status == BondDefaultedReceivable {
			out = append(out, b)
		}
	}
	return out
}

// GarnishBond applies one austerity garnishment tick to a defaulted bond: it seizes the income-independent
// minimum write-down (capped at the outstanding balance) from debtor → creditor as a settlement event, which
// guarantees the debt monotonically shrinks. When the balance reaches zero the bond is `cleared`; once the
// timebox (AusterityMaxTicks) is reached any remainder is written off. `emitted` reports whether a settlement
// event was produced (so the caller can nudge a poll). No-op (ErrConflict) for non-defaulted bonds.
func (m *Memory) GarnishBond(bondID string) (b Bond, ev SettlementEvent, emitted bool, err error) {
	m.mu.Lock()
	defer m.persistAfter()
	b, ok := m.bonds[bondID]
	if !ok {
		m.mu.Unlock()
		return Bond{}, SettlementEvent{}, false, ErrNotFound
	}
	if b.Status != BondDefaultedReceivable {
		m.mu.Unlock()
		return Bond{}, SettlementEvent{}, false, ErrConflict
	}
	applied := b.ApplyGarnish(m.econ.GarnishMinWriteDownCents)
	if applied > 0 {
		ev = m.appendEventLocked(b.LeagueID, b.DebtorID, b.CreditorID, applied, "garnish:"+b.ID+":"+strconv.Itoa(b.GarnishTicks))
		emitted = true
	}
	// Timebox backstop: forgive any remainder once the max garnish ticks are reached.
	if b.Status == BondDefaultedReceivable && b.GarnishTicks >= m.econ.AusterityMaxTicks {
		b.WriteOff()
	}
	m.bonds[bondID] = b
	m.mu.Unlock()
	return b, ev, emitted, nil
}

// BailoutCity is the co-op counterpart of garnishment: a third-party `bailerID` voluntarily pays down `debtorID`'s
// terminally-defaulted bonds in a league, oldest first, up to `cents`. Each bond's pay-down books a settlement event
// bailer → creditor (so cash is conserved — the bailer pays the creditor on the debtor's behalf), and clears the
// bond at zero. When the debtor's LAST defaulted bond clears, their austerity lifts (the client's /citystate poll
// then unwinds the tax-lock / demand slump / budget cap). The bailer is never charged more than the debtor's total
// outstanding default balance. A bond the bailer is the CREDITOR of is skipped (paying yourself is a no-op).
// Returns the total cents actually applied + the events booked. ErrConflict if there is nothing to bail out.
func (m *Memory) BailoutCity(bailerID, leagueID, debtorID string, cents int64) (applied int64, events []SettlementEvent, err error) {
	if cents <= 0 || bailerID == "" || debtorID == "" || bailerID == debtorID {
		// Reject self-bailout: bailout is a CO-OP rescue (a friend helps). A debtor clearing their own defaulted
		// bond here would bypass the repay path's reliability accounting and the garnishment timebox. Mirrors
		// GrantInvestment's self-grant rejection.
		return 0, nil, ErrConflict
	}
	m.mu.Lock()
	defer m.persistAfter()
	set := m.members[leagueID]
	if set == nil || !set[bailerID] || !set[debtorID] {
		m.mu.Unlock()
		return 0, nil, ErrNotFound
	}
	// Gather the debtor's defaulted bonds in this league, oldest first (stable, deterministic distribution).
	var defaulted []Bond
	for _, b := range m.bonds {
		if b.LeagueID == leagueID && b.DebtorID == debtorID && b.Status == BondDefaultedReceivable {
			defaulted = append(defaulted, b)
		}
	}
	sort.Slice(defaulted, func(i, j int) bool { return defaulted[i].Created.Before(defaulted[j].Created) })

	remaining := cents
	for _, b := range defaulted {
		if remaining <= 0 {
			break
		}
		if b.CreditorID == bailerID {
			continue // can't bail out a bond you're owed — it would book a self→self no-op
		}
		paid := b.ApplyBailout(remaining) // caps at this bond's outstanding balance
		if paid <= 0 {
			continue
		}
		ev := m.appendEventLocked(b.LeagueID, bailerID, b.CreditorID, paid, "bailout:"+b.ID)
		events = append(events, ev)
		m.bonds[b.ID] = b
		applied += paid
		remaining -= paid
	}
	if applied > 0 {
		// Chronicle once per bailout (not per bond): bailer → debtor, total applied. Frozen narration.
		m.appendChronicleLocked(ChronicleEntry{
			LeagueID: leagueID, Kind: "bailout", ActorID: bailerID, TargetID: debtorID, Cents: applied,
			Text: m.displayNameLocked(bailerID) + " bailed out " + m.displayNameLocked(debtorID) +
				" (§" + chronicleCents(applied) + ").",
		})
	}
	m.mu.Unlock()
	if applied == 0 {
		return 0, nil, ErrConflict // nothing defaulted to bail out (or only self-owed bonds)
	}
	return applied, events, nil
}

// CityState derives a city's austerity status in a league: it is in austerity while it has any terminally
// defaulted bond as debtor; outstandingCents is the total garnishable balance still owed.
func (m *Memory) CityState(leagueID, accountID string) (austerity bool, outstandingCents int64, defaultedBonds int) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, b := range m.bonds {
		if b.LeagueID != leagueID || b.DebtorID != accountID || b.Status != BondDefaultedReceivable {
			continue
		}
		outstandingCents += b.OutstandingDefaultCents()
		defaultedBonds++
	}
	return defaultedBonds > 0, outstandingCents, defaultedBonds
}

// AuditLeague re-derives each account's net online cash (received − paid) from the league's settlement-event
// log, and the grand total. Every event is a zero-sum payer→receiver transfer, so the total MUST be 0 — a
// non-zero total means a non-conserving event was emitted somewhere (a regression guard / live invariant check).
func (m *Memory) AuditLeague(leagueID string) (net map[string]int64, total int64, err error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if _, ok := m.leagues[leagueID]; !ok {
		return nil, 0, ErrNotFound
	}
	net = map[string]int64{}
	for _, e := range m.events {
		if e.LeagueID != leagueID {
			continue
		}
		net[e.ReceiverID] += e.Cents
		net[e.PayerID] -= e.Cents
	}
	for _, v := range net {
		total += v
	}
	return net, total, nil
}

// ── Settlement events ────────────────────────────────────────────────────────

// SettlementsSince returns the league's settlement events with Seq > since (ascending), for client
// reconciliation. The client books each exactly once, keyed by Seq.
// SettlementsForAccount returns the caller's own settlement events (caller is payer or receiver) in a league
// with Seq > since, plus latestSeq = the caller's highest event seq overall. Scoping to the caller (not the
// whole league) keeps another member's payment graph private and shrinks the response. latestSeq lets the
// client detect a server reset/rollback: if its persisted cursor exceeds latestSeq, the server's event log was
// wiped and the client must reset its cursor instead of silently skipping the new (lower-seq) events.
func (m *Memory) SettlementsForAccount(leagueID, accountID string, since int64) (events []SettlementEvent, latestSeq int64, err error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, e := range m.events {
		if e.LeagueID != leagueID || (e.PayerID != accountID && e.ReceiverID != accountID) {
			continue
		}
		if e.Seq > latestSeq {
			latestSeq = e.Seq
		}
		if e.Seq > since {
			events = append(events, e)
		}
	}
	return events, latestSeq, nil
}

// defaultFeedLimit caps SettlementsSince when the caller passes a non-positive limit.
const defaultFeedLimit = 200

// SettlementsSince returns the league's settlement events with Seq>sinceSeq, ascending by Seq, capped at limit
// (limit<=0 → defaultFeedLimit). League-wide (every member's transfers) — the activity-feed reader. Returns a copy.
func (m *Memory) SettlementsSince(leagueID string, sinceSeq int64, limit int) []SettlementEvent {
	if limit <= 0 {
		limit = defaultFeedLimit
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]SettlementEvent, 0, limit)
	// m.events is appended in seq order, so iterating it yields ascending Seq.
	for _, e := range m.events {
		if e.LeagueID != leagueID || e.Seq <= sinceSeq {
			continue
		}
		out = append(out, e)
		if len(out) >= limit {
			break
		}
	}
	return out
}

// appendEventLocked assigns the next sequence and appends an event. Caller must hold m.mu (write).
func (m *Memory) appendEventLocked(leagueID, payer, receiver string, cents int64, ref string) SettlementEvent {
	m.eventSeq++
	e := SettlementEvent{
		Seq: m.eventSeq, LeagueID: leagueID, PayerID: payer, ReceiverID: receiver,
		Cents: cents, Ref: ref, Created: m.clock(),
	}
	m.events = append(m.events, e)
	return e
}

// chronicleCents renders cents as a whole-§ amount with thousands separators (e.g. 123456 → "1,234"), for
// frozen chronicle narration. Mirrors the Discord poster's formatCents.
func chronicleCents(cents int64) string {
	whole := cents / 100
	neg := whole < 0
	if neg {
		whole = -whole
	}
	s := strconv.FormatInt(whole, 10)
	var out []byte
	for i := 0; i < len(s); i++ {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, s[i])
	}
	if neg {
		return "-" + string(out)
	}
	return string(out)
}

// ── League Chronicle (social slice 2) ─────────────────────────────────────────

// appendChronicleLocked assigns the next chronicle sequence and appends a frozen narration entry. Caller must
// hold m.mu (write). Created is stamped from the clock. Used both by the public AppendChronicle (chronicler
// job) and by the in-op direct appends (CreateLeague/JoinLeague/BailoutCity) so the entry lands atomically with
// the operation under the same lock.
func (m *Memory) appendChronicleLocked(e ChronicleEntry) ChronicleEntry {
	m.chronicleSeq++
	e.Seq = m.chronicleSeq
	e.Created = m.clock()
	m.chronicle = append(m.chronicle, e)
	return e
}

// AppendChronicle appends a frozen narration entry under the lock, then persists. The Seq is monotonic and
// league-shared (its own meta('chronicle_seq') counter); Created is stamped from the clock.
func (m *Memory) AppendChronicle(e ChronicleEntry) (ChronicleEntry, error) {
	m.mu.Lock()
	out := m.appendChronicleLocked(e)
	m.mu.Unlock()
	return out, m.persist()
}

// defaultChronicleLimit caps Chronicle when the caller passes a non-positive limit.
const defaultChronicleLimit = 200

// Chronicle returns the league's chronicle entries with Seq>sinceSeq, ascending by Seq, capped at limit
// (limit<=0 → defaultChronicleLimit). Returns a copy.
func (m *Memory) Chronicle(leagueID string, sinceSeq int64, limit int) []ChronicleEntry {
	if limit <= 0 {
		limit = defaultChronicleLimit
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]ChronicleEntry, 0, limit)
	// m.chronicle is appended in seq order, so iterating it yields ascending Seq.
	for _, e := range m.chronicle {
		if e.LeagueID != leagueID || e.Seq <= sinceSeq {
			continue
		}
		out = append(out, e)
		if len(out) >= limit {
			break
		}
	}
	return out
}

// ChronicleOnThisDay returns the league's chronicle entries from a PRIOR day whose (month, day) match now's,
// ascending by Seq. "Prior day" = Created strictly before now AND on a different calendar day (so a fresh entry
// minted moments ago doesn't echo back as "on this day"). The year is intentionally NOT matched — any earlier
// year with the same month/day qualifies (the saga recall).
func (m *Memory) ChronicleOnThisDay(leagueID string, now time.Time) []ChronicleEntry {
	now = now.UTC()
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []ChronicleEntry
	for _, e := range m.chronicle {
		if e.LeagueID != leagueID {
			continue
		}
		c := e.Created.UTC()
		if c.Month() != now.Month() || c.Day() != now.Day() {
			continue
		}
		// Same month/day but it must be a PRIOR day (different calendar date, before now).
		if c.Year() == now.Year() && c.YearDay() == now.YearDay() {
			continue
		}
		if !c.Before(now) {
			continue
		}
		out = append(out, e)
	}
	return out
}

// persistAfter is a deferred helper: the methods above unlock m.mu themselves, then this flushes to disk.
// (persist takes its own locks, so it must run AFTER the method's own Unlock.)
func (m *Memory) persistAfter() {
	// These callers must unlock m.mu before persisting, so the error can't reach the HTTP response — but a
	// silently-dropped write means a booked settlement isn't durable. Log it so disk-full/permission failures
	// are at least visible (Codex/risk-scan RISK-2).
	if err := m.persist(); err != nil {
		log.Printf("store: deferred persist failed (settlement may not be durable): %v", err)
	}
}
