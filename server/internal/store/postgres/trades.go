package postgres

import (
	"context"
	"encoding/json"
	"strconv"

	"github.com/jackc/pgx/v5"

	"openmarkets/server/internal/id"
	"openmarkets/server/internal/money"
	"openmarkets/server/internal/store"
)

const tradeCols = `id, league_id, offered_by, counterparty, items, default_rate_bps, installments,
	status, settled, created, accepted_day, shortfall_installments`

// scanTrade reads a trades row (column order == tradeCols).
func scanTrade(row interface{ Scan(...any) error }) (store.Trade, error) {
	var t store.Trade
	var items, shortfall []byte
	if err := row.Scan(&t.ID, &t.LeagueID, &t.OfferedBy, &t.Counterparty, &items, &t.DefaultRateBps,
		&t.Installments, &t.Status, &t.Settled, &t.Created, &t.AcceptedDay, &shortfall); err != nil {
		return store.Trade{}, err
	}
	if err := json.Unmarshal(items, &t.Items); err != nil {
		return store.Trade{}, err
	}
	if len(shortfall) > 0 {
		if err := json.Unmarshal(shortfall, &t.ShortfallInstallments); err != nil {
			return store.Trade{}, err
		}
	}
	return t, nil
}

// writeTradeTx inserts-or-updates a trade row inside tx (the booking paths rewrite the whole entity).
func writeTradeTx(ctx context.Context, tx pgx.Tx, t store.Trade) error {
	items, err := json.Marshal(t.Items)
	if err != nil {
		return err
	}
	shortfall, err := json.Marshal(emptyIfNil(t.ShortfallInstallments))
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx,
		`INSERT INTO trades(id, league_id, offered_by, counterparty, items, default_rate_bps, installments,
			status, settled, created, accepted_day, shortfall_installments)
		 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
		 ON CONFLICT (id) DO UPDATE SET
			items=EXCLUDED.items, status=EXCLUDED.status, settled=EXCLUDED.settled,
			accepted_day=EXCLUDED.accepted_day, shortfall_installments=EXCLUDED.shortfall_installments,
			default_rate_bps=EXCLUDED.default_rate_bps, installments=EXCLUDED.installments`,
		t.ID, t.LeagueID, t.OfferedBy, t.Counterparty, items, t.DefaultRateBps, t.Installments,
		t.Status, t.Settled, t.Created, t.AcceptedDay, shortfall)
	return err
}

func emptyIfNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// loadTradeForUpdate loads a trade row with a row lock for a booking transaction.
func loadTradeForUpdate(ctx context.Context, tx pgx.Tx, tradeID string) (store.Trade, error) {
	return scanTrade(tx.QueryRow(ctx, `SELECT `+tradeCols+` FROM trades WHERE id=$1 FOR UPDATE`, tradeID))
}

// ── Reads ─────────────────────────────────────────────────────────────────────

func (p *PG) GetTrade(idStr string) (store.Trade, error) {
	ctx, cancel := p.ctx()
	defer cancel()
	t, err := scanTrade(p.pool.QueryRow(ctx, `SELECT `+tradeCols+` FROM trades WHERE id=$1`, idStr))
	if err != nil {
		return store.Trade{}, mapErr(err)
	}
	return t, nil
}

func (p *PG) TradesFor(leagueID, accountID string) ([]store.Trade, error) {
	ctx, cancel := p.ctx()
	defer cancel()
	rows, err := p.pool.Query(ctx,
		`SELECT `+tradeCols+` FROM trades WHERE league_id=$1 AND (offered_by=$2 OR counterparty=$2)`,
		leagueID, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.Trade
	for rows.Next() {
		t, err := scanTrade(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (p *PG) TradeVolumeByLeague(leagueID string) map[string]int64 {
	ctx, cancel := p.ctx()
	defer cancel()
	out := map[string]int64{}
	// A completed trade credits BOTH parties; same-party-both-sides counted once (UNION semantics).
	rows, err := p.pool.Query(ctx,
		`SELECT acct, COUNT(*) FROM (
			SELECT id, offered_by AS acct FROM trades WHERE league_id=$1 AND status=$2
			UNION
			SELECT id, counterparty AS acct FROM trades WHERE league_id=$1 AND status=$2
		 ) u GROUP BY acct`,
		leagueID, store.TradeCompleted)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var acct string
		var n int64
		if err := rows.Scan(&acct, &n); err != nil {
			return out
		}
		out[acct] = n
	}
	return out
}

func (p *PG) ListActiveTrades() []store.Trade {
	ctx, cancel := p.ctx()
	defer cancel()
	rows, err := p.pool.Query(ctx,
		`SELECT `+tradeCols+` FROM trades WHERE status=$1 AND settled < installments`, store.TradeActive)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []store.Trade
	for rows.Next() {
		t, err := scanTrade(rows)
		if err != nil {
			return out
		}
		out = append(out, t)
	}
	return out
}

// ── CreateTrade (txn: member checks + insert) ─────────────────────────────────

func (p *PG) CreateTrade(t store.Trade) (store.Trade, error) {
	if t.OfferedBy == "" || t.Counterparty == "" || t.OfferedBy == t.Counterparty {
		return store.Trade{}, store.ErrConflict
	}
	p.mu.RLock()
	minRate := p.econ.MinDefaultRateBps
	p.mu.RUnlock()
	if err := t.ValidateForOffer(minRate); err != nil {
		return store.Trade{}, err
	}
	ctx, cancel := p.ctx()
	defer cancel()
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return store.Trade{}, err
	}
	defer tx.Rollback(ctx)

	okA, err := isMemberTx(ctx, tx, t.OfferedBy, t.LeagueID)
	if err != nil {
		return store.Trade{}, err
	}
	okB, err := isMemberTx(ctx, tx, t.Counterparty, t.LeagueID)
	if err != nil {
		return store.Trade{}, err
	}
	if !okA || !okB {
		return store.Trade{}, store.ErrNotFound
	}
	t.ID = id.New()
	t.Status = store.TradeOffered
	t.Settled = 0
	for i := range t.Items { // ensure no caller-supplied frozen values leak in
		t.Items[i].ValueCentsAtAccept = 0
		t.Items[i].UnitPriceCents = 0
	}
	t.Created = p.clock()
	if err := writeTradeTx(ctx, tx, t); err != nil {
		return store.Trade{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return store.Trade{}, err
	}
	return t, nil
}

// ── SetTradeStatus (accept/decline/cancel) ────────────────────────────────────
//
// On accept it FREEZES line values via the injected Pricer (mark-to-accept). The freeze runs OUTSIDE the txn
// (the Pricer reads the store), then the commit re-checks the trade is still in the expected pre-state
// (re-loading FOR UPDATE), mirroring Memory's lock-free freeze + re-check.

func (p *PG) SetTradeStatus(accountID, tradeID, action string) (store.Trade, error) {
	ctx, cancel := p.ctx()
	defer cancel()

	// Phase 1: load the current trade + validate the transition (no row lock held across the pricer call).
	t, err := scanTrade(p.pool.QueryRow(ctx, `SELECT `+tradeCols+` FROM trades WHERE id=$1`, tradeID))
	if err != nil {
		return store.Trade{}, mapErr(err)
	}
	if accountID != t.OfferedBy && accountID != t.Counterparty {
		return store.Trade{}, store.ErrNotFound
	}
	prior := t.Status
	next, err := t.NextTradeStatus(accountID, action)
	if err != nil {
		return store.Trade{}, err
	}
	p.mu.RLock()
	pricer := p.pricer
	p.mu.RUnlock()

	if next == store.TradeActive { // freeze values + anchor the due clock, lock-free
		if pricer == nil {
			return store.Trade{}, store.ErrConflict
		}
		lid := t.LeagueID
		if err := t.FreezeValues(func(c string) (int64, bool) { return pricer(lid, c) }); err != nil {
			return store.Trade{}, err
		}
		if err := t.PrepareActiveErr(); err != nil {
			return store.Trade{}, err
		}
		t.AcceptedDay = p.clock().Unix()
		t.Settled = 0
	}
	t.Status = next

	// Phase 2: commit under a row lock, re-checking the trade is still in the expected pre-state.
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return store.Trade{}, err
	}
	defer tx.Rollback(ctx)
	cur, err := loadTradeForUpdate(ctx, tx, tradeID)
	if err != nil {
		return store.Trade{}, mapErr(err)
	}
	if cur.Status != prior {
		return store.Trade{}, store.ErrConflict
	}
	if err := writeTradeTx(ctx, tx, t); err != nil {
		return store.Trade{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return store.Trade{}, err
	}
	return t, nil
}

// ── SettleTradeInstallment (txn) ──────────────────────────────────────────────

func (p *PG) SettleTradeInstallment(accountID, tradeID string) (store.Trade, store.SettlementEvent, error) {
	ctx, cancel := p.ctx()
	defer cancel()
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return store.Trade{}, store.SettlementEvent{}, err
	}
	defer tx.Rollback(ctx)

	t, err := loadTradeForUpdate(ctx, tx, tradeID)
	if err != nil {
		return store.Trade{}, store.SettlementEvent{}, mapErr(err)
	}
	if accountID != t.OfferedBy && accountID != t.Counterparty {
		return store.Trade{}, store.SettlementEvent{}, store.ErrNotFound
	}
	if t.Status != store.TradeActive || t.Settled >= t.Installments {
		return store.Trade{}, store.SettlementEvent{}, store.ErrConflict
	}
	sched, err := t.InstallmentSchedule()
	if err != nil {
		return store.Trade{}, store.SettlementEvent{}, store.ErrConflict
	}
	payer, receiver := t.NetPayerReceiver()
	cents := sched[t.Settled]
	var ev store.SettlementEvent
	if cents > 0 {
		if accountID != payer {
			return store.Trade{}, store.SettlementEvent{}, store.ErrConflict
		}
		ev, err = p.appendEventTx(ctx, tx, t.LeagueID, payer, receiver, cents, "trade:"+t.ID+":"+strconv.Itoa(t.Settled))
		if err != nil {
			return store.Trade{}, store.SettlementEvent{}, err
		}
	}
	t.Settled++
	if t.Settled >= t.Installments {
		t.Status = store.TradeCompleted
	}
	if cents > 0 {
		if err := bumpReliabilityTx(ctx, tx, payer, true); err != nil {
			return store.Trade{}, store.SettlementEvent{}, err
		}
	}
	if err := writeTradeTx(ctx, tx, t); err != nil {
		return store.Trade{}, store.SettlementEvent{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return store.Trade{}, store.SettlementEvent{}, err
	}
	return t, ev, nil
}

// AutoSettleTradeInstallment is the due-clock's server-side settle (no caller). Mirrors SettleTradeInstallment.
func (p *PG) AutoSettleTradeInstallment(tradeID string) (store.Trade, store.SettlementEvent, error) {
	ctx, cancel := p.ctx()
	defer cancel()
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return store.Trade{}, store.SettlementEvent{}, err
	}
	defer tx.Rollback(ctx)

	t, err := loadTradeForUpdate(ctx, tx, tradeID)
	if err != nil {
		return store.Trade{}, store.SettlementEvent{}, mapErr(err)
	}
	if t.Status != store.TradeActive || t.Settled >= t.Installments {
		return store.Trade{}, store.SettlementEvent{}, store.ErrConflict
	}
	sched, err := t.InstallmentSchedule()
	if err != nil {
		return store.Trade{}, store.SettlementEvent{}, store.ErrConflict
	}
	payer, receiver := t.NetPayerReceiver()
	cents := sched[t.Settled]
	var ev store.SettlementEvent
	if cents > 0 {
		ev, err = p.appendEventTx(ctx, tx, t.LeagueID, payer, receiver, cents, "trade:"+t.ID+":"+strconv.Itoa(t.Settled))
		if err != nil {
			return store.Trade{}, store.SettlementEvent{}, err
		}
		if err := bumpReliabilityTx(ctx, tx, payer, true); err != nil {
			return store.Trade{}, store.SettlementEvent{}, err
		}
	}
	t.Settled++
	if t.Settled >= t.Installments {
		t.Status = store.TradeCompleted
	}
	if err := writeTradeTx(ctx, tx, t); err != nil {
		return store.Trade{}, store.SettlementEvent{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return store.Trade{}, store.SettlementEvent{}, err
	}
	return t, ev, nil
}

// ── MissTradeInstallment (txn: auto-bond the shortfall) ───────────────────────

func (p *PG) MissTradeInstallment(tradeID string) (store.Trade, store.Bond, error) {
	ctx, cancel := p.ctx()
	defer cancel()
	p.mu.RLock()
	econ := p.econ
	p.mu.RUnlock()

	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return store.Trade{}, store.Bond{}, err
	}
	defer tx.Rollback(ctx)

	t, err := loadTradeForUpdate(ctx, tx, tradeID)
	if err != nil {
		return store.Trade{}, store.Bond{}, mapErr(err)
	}
	if t.Status != store.TradeActive || t.Settled >= t.Installments {
		return store.Trade{}, store.Bond{}, store.ErrConflict
	}
	sched, err := t.InstallmentSchedule()
	if err != nil {
		return store.Trade{}, store.Bond{}, store.ErrConflict
	}
	payer, receiver := t.NetPayerReceiver()
	cents := sched[t.Settled]
	var b store.Bond
	if cents >= econ.MinPrincipalCents {
		b, err = store.NewAutoBond(id.New(), t.LeagueID, receiver, payer, t.ID, cents,
			t.DefaultRateBps, econ.AutoBondTermInstallments, econ.MinPrincipalCents, p.clock())
		if err != nil {
			return store.Trade{}, store.Bond{}, err
		}
		if err := writeBondTx(ctx, tx, b); err != nil {
			return store.Trade{}, store.Bond{}, err
		}
		if err := bumpReliabilityTx(ctx, tx, payer, false); err != nil {
			return store.Trade{}, store.Bond{}, err
		}
	}
	t.Settled++
	if t.Settled >= t.Installments {
		t.Status = store.TradeCompleted
	}
	if err := writeTradeTx(ctx, tx, t); err != nil {
		return store.Trade{}, store.Bond{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return store.Trade{}, store.Bond{}, err
	}
	return t, b, nil
}

// ── ReportTradeShortfall (txn: goods-debt bond) ───────────────────────────────

func (p *PG) ReportTradeShortfall(accountID, tradeID string, installment int, reportedCents int64) (store.Trade, store.Bond, error) {
	ctx, cancel := p.ctx()
	defer cancel()
	p.mu.RLock()
	econ := p.econ
	p.mu.RUnlock()

	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return store.Trade{}, store.Bond{}, err
	}
	defer tx.Rollback(ctx)

	t, err := loadTradeForUpdate(ctx, tx, tradeID)
	if err != nil {
		return store.Trade{}, store.Bond{}, mapErr(err)
	}
	if accountID != t.OfferedBy && accountID != t.Counterparty {
		return store.Trade{}, store.Bond{}, store.ErrNotFound
	}
	if t.Status != store.TradeActive || installment < 0 || installment >= t.Installments {
		return store.Trade{}, store.Bond{}, store.ErrConflict
	}
	for _, s := range t.ShortfallInstallments { // already reported → idempotent ErrConflict
		if s == installment {
			return store.Trade{}, store.Bond{}, store.ErrConflict
		}
	}

	cents := reportedCents
	if cents < 0 {
		cents = 0
	}
	total := t.CommodityGiveValueCents(accountID)
	if total > 0 {
		if csched, err := money.Amortize(total, t.Installments); err == nil && installment < len(csched) {
			if cents > csched[installment] {
				cents = csched[installment]
			}
		}
	} else {
		cents = 0
	}

	var b store.Bond
	if cents >= econ.MinPrincipalCents {
		creditor := t.OfferedBy
		if accountID == t.OfferedBy {
			creditor = t.Counterparty
		}
		b = store.Bond{
			ID:             id.New(),
			LeagueID:       t.LeagueID,
			CreditorID:     creditor,
			DebtorID:       accountID,
			PrincipalCents: cents,
			InterestBps:    t.DefaultRateBps,
			Installments:   1,
			Origin:         "trade-shortfall:" + t.ID,
			Created:        p.clock(),
		}
		if err := b.Activate(econ.MinPrincipalCents); err != nil {
			return store.Trade{}, store.Bond{}, err
		}
		if err := writeBondTx(ctx, tx, b); err != nil {
			return store.Trade{}, store.Bond{}, err
		}
		if err := bumpReliabilityTx(ctx, tx, accountID, false); err != nil {
			return store.Trade{}, store.Bond{}, err
		}
	}

	t.ShortfallInstallments = append(t.ShortfallInstallments, installment)
	if err := writeTradeTx(ctx, tx, t); err != nil {
		return store.Trade{}, store.Bond{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return store.Trade{}, store.Bond{}, err
	}
	return t, b, nil
}
