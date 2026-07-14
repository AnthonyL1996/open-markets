package postgres

import (
	"context"
	"sort"
	"strconv"

	"github.com/jackc/pgx/v5"

	"openmarkets/server/internal/id"
	"openmarkets/server/internal/money"
	"openmarkets/server/internal/store"
)

const bondCols = `id, league_id, creditor_id, debtor_id, principal_cents, interest_bps, installments,
	settled, missed_count, total_due_cents, status, origin, proposed_by, created,
	defaulted_remaining_cents, garnished_cents, garnish_ticks`

func scanBond(row interface{ Scan(...any) error }) (store.Bond, error) {
	var b store.Bond
	err := row.Scan(&b.ID, &b.LeagueID, &b.CreditorID, &b.DebtorID, &b.PrincipalCents, &b.InterestBps,
		&b.Installments, &b.Settled, &b.MissedCount, &b.TotalDueCents, &b.Status, &b.Origin, &b.ProposedBy,
		&b.Created, &b.DefaultedRemainingCents, &b.GarnishedCents, &b.GarnishTicks)
	return b, err
}

func writeBondTx(ctx context.Context, tx pgx.Tx, b store.Bond) error {
	_, err := tx.Exec(ctx,
		`INSERT INTO bonds(id, league_id, creditor_id, debtor_id, principal_cents, interest_bps, installments,
			settled, missed_count, total_due_cents, status, origin, proposed_by, created,
			defaulted_remaining_cents, garnished_cents, garnish_ticks)
		 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)
		 ON CONFLICT (id) DO UPDATE SET
			principal_cents=EXCLUDED.principal_cents, interest_bps=EXCLUDED.interest_bps,
			installments=EXCLUDED.installments, settled=EXCLUDED.settled, missed_count=EXCLUDED.missed_count,
			total_due_cents=EXCLUDED.total_due_cents, status=EXCLUDED.status, proposed_by=EXCLUDED.proposed_by,
			defaulted_remaining_cents=EXCLUDED.defaulted_remaining_cents,
			garnished_cents=EXCLUDED.garnished_cents, garnish_ticks=EXCLUDED.garnish_ticks`,
		b.ID, b.LeagueID, b.CreditorID, b.DebtorID, b.PrincipalCents, b.InterestBps, b.Installments,
		b.Settled, b.MissedCount, b.TotalDueCents, b.Status, b.Origin, b.ProposedBy, b.Created,
		b.DefaultedRemainingCents, b.GarnishedCents, b.GarnishTicks)
	return err
}

func loadBondForUpdate(ctx context.Context, tx pgx.Tx, bondID string) (store.Bond, error) {
	return scanBond(tx.QueryRow(ctx, `SELECT `+bondCols+` FROM bonds WHERE id=$1 FOR UPDATE`, bondID))
}

// ── Reads ─────────────────────────────────────────────────────────────────────

func (p *PG) GetBond(idStr string) (store.Bond, error) {
	ctx, cancel := p.ctx()
	defer cancel()
	b, err := scanBond(p.pool.QueryRow(ctx, `SELECT `+bondCols+` FROM bonds WHERE id=$1`, idStr))
	if err != nil {
		return store.Bond{}, mapErr(err)
	}
	return b, nil
}

func (p *PG) BondsFor(leagueID, accountID string) ([]store.Bond, error) {
	ctx, cancel := p.ctx()
	defer cancel()
	rows, err := p.pool.Query(ctx,
		`SELECT `+bondCols+` FROM bonds WHERE league_id=$1 AND (creditor_id=$2 OR debtor_id=$2)`,
		leagueID, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.Bond
	for rows.Next() {
		b, err := scanBond(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func (p *PG) ListActiveBonds() []store.Bond {
	ctx, cancel := p.ctx()
	defer cancel()
	rows, err := p.pool.Query(ctx,
		`SELECT `+bondCols+` FROM bonds WHERE status IN ($1,$2) AND settled < installments`,
		store.BondActive, store.BondDelinquent)
	if err != nil {
		return nil
	}
	defer rows.Close()
	return collectBonds(rows)
}

func (p *PG) ListDefaultedBonds() []store.Bond {
	ctx, cancel := p.ctx()
	defer cancel()
	rows, err := p.pool.Query(ctx, `SELECT `+bondCols+` FROM bonds WHERE status=$1`, store.BondDefaultedReceivable)
	if err != nil {
		return nil
	}
	defer rows.Close()
	return collectBonds(rows)
}

func collectBonds(rows pgx.Rows) []store.Bond {
	var out []store.Bond
	for rows.Next() {
		b, err := scanBond(rows)
		if err != nil {
			return out
		}
		out = append(out, b)
	}
	return out
}

// ── SettleBondInstallment (txn) ───────────────────────────────────────────────

func (p *PG) SettleBondInstallment(accountID, bondID string) (store.Bond, store.SettlementEvent, error) {
	ctx, cancel := p.ctx()
	defer cancel()
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return store.Bond{}, store.SettlementEvent{}, err
	}
	defer tx.Rollback(ctx)

	b, err := loadBondForUpdate(ctx, tx, bondID)
	if err != nil {
		return store.Bond{}, store.SettlementEvent{}, mapErr(err)
	}
	if accountID != b.DebtorID {
		return store.Bond{}, store.SettlementEvent{}, store.ErrNotFound
	}
	if b.Status != store.BondActive && b.Status != store.BondDelinquent {
		return store.Bond{}, store.SettlementEvent{}, store.ErrConflict
	}
	if b.Settled >= b.Installments {
		return store.Bond{}, store.SettlementEvent{}, store.ErrConflict
	}
	sched, err := b.Schedule()
	if err != nil {
		return store.Bond{}, store.SettlementEvent{}, store.ErrConflict
	}
	cents := sched[b.Settled]
	onTime := b.Status == store.BondActive
	ev, err := p.appendEventTx(ctx, tx, b.LeagueID, b.DebtorID, b.CreditorID, cents, "bond:"+b.ID+":"+strconv.Itoa(b.Settled))
	if err != nil {
		return store.Bond{}, store.SettlementEvent{}, err
	}
	b.Settled++
	b.Cure()
	if b.Settled >= b.Installments {
		b.Status = store.BondCompleted
	}
	if onTime {
		if err := bumpReliabilityTx(ctx, tx, accountID, true); err != nil {
			return store.Bond{}, store.SettlementEvent{}, err
		}
	}
	if err := writeBondTx(ctx, tx, b); err != nil {
		return store.Bond{}, store.SettlementEvent{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return store.Bond{}, store.SettlementEvent{}, err
	}
	return b, ev, nil
}

// ── MissBondInstallment (txn: delinquent → terminal default) ──────────────────

func (p *PG) MissBondInstallment(bondID string) (store.Bond, bool, error) {
	ctx, cancel := p.ctx()
	defer cancel()
	p.mu.RLock()
	maxMisses := p.econ.BondMaxMisses
	p.mu.RUnlock()

	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return store.Bond{}, false, err
	}
	defer tx.Rollback(ctx)

	b, err := loadBondForUpdate(ctx, tx, bondID)
	if err != nil {
		return store.Bond{}, false, mapErr(err)
	}
	if b.Status != store.BondActive && b.Status != store.BondDelinquent {
		return store.Bond{}, false, store.ErrConflict
	}
	defaulted := b.RegisterMiss(maxMisses)
	if err := bumpReliabilityTx(ctx, tx, b.DebtorID, false); err != nil {
		return store.Bond{}, false, err
	}
	if err := writeBondTx(ctx, tx, b); err != nil {
		return store.Bond{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return store.Bond{}, false, err
	}
	return b, defaulted, nil
}

// ── GarnishBond (txn: austerity write-down) ───────────────────────────────────

func (p *PG) GarnishBond(bondID string) (b store.Bond, ev store.SettlementEvent, emitted bool, err error) {
	ctx, cancel := p.ctx()
	defer cancel()
	p.mu.RLock()
	econ := p.econ
	p.mu.RUnlock()

	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return store.Bond{}, store.SettlementEvent{}, false, err
	}
	defer tx.Rollback(ctx)

	b, err = loadBondForUpdate(ctx, tx, bondID)
	if err != nil {
		return store.Bond{}, store.SettlementEvent{}, false, mapErr(err)
	}
	if b.Status != store.BondDefaultedReceivable {
		return store.Bond{}, store.SettlementEvent{}, false, store.ErrConflict
	}
	applied := b.ApplyGarnish(econ.GarnishMinWriteDownCents)
	if applied > 0 {
		ev, err = p.appendEventTx(ctx, tx, b.LeagueID, b.DebtorID, b.CreditorID, applied, "garnish:"+b.ID+":"+strconv.Itoa(b.GarnishTicks))
		if err != nil {
			return store.Bond{}, store.SettlementEvent{}, false, err
		}
		emitted = true
	}
	if b.Status == store.BondDefaultedReceivable && b.GarnishTicks >= econ.AusterityMaxTicks {
		b.WriteOff()
	}
	if err := writeBondTx(ctx, tx, b); err != nil {
		return store.Bond{}, store.SettlementEvent{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return store.Bond{}, store.SettlementEvent{}, false, err
	}
	return b, ev, emitted, nil
}

// ── BailoutCity (txn: third-party pay-down, oldest first) ─────────────────────

func (p *PG) BailoutCity(bailerID, leagueID, debtorID string, cents int64) (applied int64, events []store.SettlementEvent, err error) {
	if cents <= 0 || bailerID == "" || debtorID == "" || bailerID == debtorID {
		return 0, nil, store.ErrConflict
	}
	ctx, cancel := p.ctx()
	defer cancel()
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return 0, nil, err
	}
	defer tx.Rollback(ctx)

	okBailer, err := isMemberTx(ctx, tx, bailerID, leagueID)
	if err != nil {
		return 0, nil, err
	}
	okDebtor, err := isMemberTx(ctx, tx, debtorID, leagueID)
	if err != nil {
		return 0, nil, err
	}
	if !okBailer || !okDebtor {
		return 0, nil, store.ErrNotFound
	}
	// Gather the debtor's defaulted bonds in this league, oldest first, UNDER A ROW LOCK so concurrent
	// garnish/bailout can't double-spend the same balance.
	rows, err := tx.Query(ctx,
		`SELECT `+bondCols+` FROM bonds
		  WHERE league_id=$1 AND debtor_id=$2 AND status=$3
		  ORDER BY created ASC FOR UPDATE`,
		leagueID, debtorID, store.BondDefaultedReceivable)
	if err != nil {
		return 0, nil, err
	}
	defaulted := collectBonds(rows)
	rows.Close()
	// Defensive: ORDER BY created already matches Memory's sort.Slice(Created.Before); keep a stable re-sort.
	sort.SliceStable(defaulted, func(i, j int) bool { return defaulted[i].Created.Before(defaulted[j].Created) })

	remaining := cents
	for i := range defaulted {
		b := defaulted[i]
		if remaining <= 0 {
			break
		}
		if b.CreditorID == bailerID {
			continue
		}
		paid := b.ApplyBailout(remaining)
		if paid <= 0 {
			continue
		}
		ev, err := p.appendEventTx(ctx, tx, b.LeagueID, bailerID, b.CreditorID, paid, "bailout:"+b.ID)
		if err != nil {
			return 0, nil, err
		}
		events = append(events, ev)
		if err := writeBondTx(ctx, tx, b); err != nil {
			return 0, nil, err
		}
		applied += paid
		remaining -= paid
	}
	if applied == 0 {
		return 0, nil, store.ErrConflict
	}
	// Chronicle once per bailout (within the same txn — atomic with the money moves). Names frozen now.
	if _, err := p.appendChronicleTx(ctx, tx, store.ChronicleEntry{
		LeagueID: leagueID, Kind: "bailout", ActorID: bailerID, TargetID: debtorID, Cents: applied,
		Text: displayNameTx(ctx, tx, bailerID) + " bailed out " + displayNameTx(ctx, tx, debtorID) +
			" (§" + chronicleCentsPG(applied) + ").",
	}); err != nil {
		return 0, nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, nil, err
	}
	return applied, events, nil
}

// chronicleCentsPG renders cents as a whole-§ amount with thousands separators (mirrors store.chronicleCents).
func chronicleCentsPG(cents int64) string {
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

// ── CityState ─────────────────────────────────────────────────────────────────

func (p *PG) CityState(leagueID, accountID string) (austerity bool, outstandingCents int64, defaultedBonds int) {
	ctx, cancel := p.ctx()
	defer cancel()
	rows, err := p.pool.Query(ctx,
		`SELECT defaulted_remaining_cents, garnished_cents FROM bonds
		  WHERE league_id=$1 AND debtor_id=$2 AND status=$3`,
		leagueID, accountID, store.BondDefaultedReceivable)
	if err != nil {
		return false, 0, 0
	}
	defer rows.Close()
	for rows.Next() {
		var rem, garn int64
		if err := rows.Scan(&rem, &garn); err != nil {
			return defaultedBonds > 0, outstandingCents, defaultedBonds
		}
		out := rem - garn
		if out < 0 {
			out = 0
		}
		outstandingCents += out
		defaultedBonds++
	}
	return defaultedBonds > 0, outstandingCents, defaultedBonds
}

// ── Manual loan negotiation ───────────────────────────────────────────────────

func (p *PG) validLoanTerms(principalCents, interestBps int64, installments int) bool {
	p.mu.RLock()
	minP := p.econ.MinPrincipalCents
	p.mu.RUnlock()
	_, err := money.ValidateBondTerms(principalCents, interestBps, installments, minP)
	return err == nil
}

func (p *PG) OfferLoan(b store.Bond) (store.Bond, error) {
	if b.CreditorID == "" || b.DebtorID == "" || b.CreditorID == b.DebtorID {
		return store.Bond{}, store.ErrConflict
	}
	if b.ProposedBy != b.CreditorID && b.ProposedBy != b.DebtorID {
		return store.Bond{}, store.ErrConflict
	}
	if !p.validLoanTerms(b.PrincipalCents, b.InterestBps, b.Installments) {
		return store.Bond{}, store.ErrConflict
	}
	ctx, cancel := p.ctx()
	defer cancel()
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return store.Bond{}, err
	}
	defer tx.Rollback(ctx)

	okC, err := isMemberTx(ctx, tx, b.CreditorID, b.LeagueID)
	if err != nil {
		return store.Bond{}, err
	}
	okD, err := isMemberTx(ctx, tx, b.DebtorID, b.LeagueID)
	if err != nil {
		return store.Bond{}, err
	}
	if !okC || !okD {
		return store.Bond{}, store.ErrNotFound
	}
	b.ID = id.New()
	b.Origin = store.BondOriginManual
	b.Status = store.BondOffered
	b.Settled = 0
	b.MissedCount = 0
	b.TotalDueCents = 0
	b.Created = p.clock()
	if err := writeBondTx(ctx, tx, b); err != nil {
		return store.Bond{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return store.Bond{}, err
	}
	return b, nil
}

func (p *PG) CounterLoan(accountID, bondID string, principalCents, interestBps int64, installments int) (store.Bond, error) {
	if !p.validLoanTerms(principalCents, interestBps, installments) {
		return store.Bond{}, store.ErrConflict
	}
	ctx, cancel := p.ctx()
	defer cancel()
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return store.Bond{}, err
	}
	defer tx.Rollback(ctx)

	b, err := loadBondForUpdate(ctx, tx, bondID)
	if err != nil {
		return store.Bond{}, mapErr(err)
	}
	if b.Origin != store.BondOriginManual || !loanParty(b, accountID) {
		return store.Bond{}, store.ErrNotFound
	}
	if b.Status != store.BondOffered || accountID == b.ProposedBy {
		return store.Bond{}, store.ErrConflict
	}
	b.PrincipalCents = principalCents
	b.InterestBps = interestBps
	b.Installments = installments
	b.ProposedBy = accountID
	if err := writeBondTx(ctx, tx, b); err != nil {
		return store.Bond{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return store.Bond{}, err
	}
	return b, nil
}

func (p *PG) AcceptLoan(accountID, bondID string) (store.Bond, store.SettlementEvent, error) {
	ctx, cancel := p.ctx()
	defer cancel()
	p.mu.RLock()
	minP := p.econ.MinPrincipalCents
	p.mu.RUnlock()

	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return store.Bond{}, store.SettlementEvent{}, err
	}
	defer tx.Rollback(ctx)

	b, err := loadBondForUpdate(ctx, tx, bondID)
	if err != nil {
		return store.Bond{}, store.SettlementEvent{}, mapErr(err)
	}
	if b.Origin != store.BondOriginManual || !loanParty(b, accountID) {
		return store.Bond{}, store.SettlementEvent{}, store.ErrNotFound
	}
	if b.Status != store.BondOffered || accountID == b.ProposedBy {
		return store.Bond{}, store.SettlementEvent{}, store.ErrConflict
	}
	if err := b.Activate(minP); err != nil {
		return store.Bond{}, store.SettlementEvent{}, store.ErrConflict
	}
	ev, err := p.appendEventTx(ctx, tx, b.LeagueID, b.CreditorID, b.DebtorID, b.PrincipalCents, "loan:"+b.ID+":principal")
	if err != nil {
		return store.Bond{}, store.SettlementEvent{}, err
	}
	if err := writeBondTx(ctx, tx, b); err != nil {
		return store.Bond{}, store.SettlementEvent{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return store.Bond{}, store.SettlementEvent{}, err
	}
	return b, ev, nil
}

func (p *PG) SetLoanStatus(accountID, bondID, action string) (store.Bond, error) {
	ctx, cancel := p.ctx()
	defer cancel()
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return store.Bond{}, err
	}
	defer tx.Rollback(ctx)

	b, err := loadBondForUpdate(ctx, tx, bondID)
	if err != nil {
		return store.Bond{}, mapErr(err)
	}
	if b.Origin != store.BondOriginManual || !loanParty(b, accountID) {
		return store.Bond{}, store.ErrNotFound
	}
	if b.Status != store.BondOffered {
		return store.Bond{}, store.ErrConflict
	}
	switch action {
	case "decline":
		if accountID == b.ProposedBy {
			return store.Bond{}, store.ErrConflict
		}
		b.Status = store.BondDeclined
	case "cancel":
		if accountID != b.ProposedBy {
			return store.Bond{}, store.ErrConflict
		}
		b.Status = store.BondCancelled
	default:
		return store.Bond{}, store.ErrConflict
	}
	if err := writeBondTx(ctx, tx, b); err != nil {
		return store.Bond{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return store.Bond{}, err
	}
	return b, nil
}

func loanParty(b store.Bond, accountID string) bool {
	return accountID == b.CreditorID || accountID == b.DebtorID
}
