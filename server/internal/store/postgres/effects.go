package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"

	"openmarkets/server/internal/id"
	"openmarkets/server/internal/store"
)

const effectCols = `id, league_id, issuer_id, grantee_id, kind, cost_cents, demand_boost, demand_kind,
	attract_rate, commodity, trade_pct_bips, ticks_remaining, created`

func scanEffect(row interface{ Scan(...any) error }) (store.Effect, error) {
	var e store.Effect
	err := row.Scan(&e.ID, &e.LeagueID, &e.IssuerID, &e.GranteeID, &e.Kind, &e.CostCents, &e.DemandBoost,
		&e.DemandKind, &e.AttractRate, &e.Commodity, &e.TradePctBips, &e.TicksRemaining, &e.Created)
	return e, err
}

func writeEffectTx(ctx context.Context, tx pgx.Tx, e store.Effect) error {
	_, err := tx.Exec(ctx,
		`INSERT INTO effects(id, league_id, issuer_id, grantee_id, kind, cost_cents, demand_boost, demand_kind,
			attract_rate, commodity, trade_pct_bips, ticks_remaining, created)
		 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
		e.ID, e.LeagueID, e.IssuerID, e.GranteeID, e.Kind, e.CostCents, e.DemandBoost, e.DemandKind,
		e.AttractRate, e.Commodity, e.TradePctBips, e.TicksRemaining, e.Created)
	return err
}

// GrantInvestment books the symmetric issuer→grantee transfer (conserving) and creates the buff, in ONE txn.
// The buff-magnitude derivation + duration clamp live in store.InvestBuffMagnitude/clamp helpers exported for
// parity; here we replicate the same clamps Memory applies (cooldown, self-grant, member checks).
func (p *PG) GrantInvestment(leagueID, issuerID, granteeID string, costCents int64, days int, demandKind string) (store.Effect, store.SettlementEvent, error) {
	if issuerID == granteeID {
		return store.Effect{}, store.SettlementEvent{}, store.ErrConflict
	}
	if !store.ValidDemandKind(demandKind) {
		demandKind = store.DemandResidential
	}
	if days < store.InvestMinDays {
		days = store.InvestMinDays
	} else if days > store.InvestMaxDays {
		days = store.InvestMaxDays
	}

	ctx, cancel := p.ctx()
	defer cancel()
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return store.Effect{}, store.SettlementEvent{}, err
	}
	defer tx.Rollback(ctx)

	okI, err := isMemberTx(ctx, tx, issuerID, leagueID)
	if err != nil {
		return store.Effect{}, store.SettlementEvent{}, err
	}
	okG, err := isMemberTx(ctx, tx, granteeID, leagueID)
	if err != nil {
		return store.Effect{}, store.SettlementEvent{}, err
	}
	if !okI || !okG {
		return store.Effect{}, store.SettlementEvent{}, store.ErrNotFound
	}
	// Cooldown: refuse a second active grant from the same issuer→grantee pair.
	var dup bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM effects WHERE kind=$1 AND league_id=$2 AND issuer_id=$3 AND grantee_id=$4 AND ticks_remaining>0)`,
		store.EffectInvestmentOffice, leagueID, issuerID, granteeID).Scan(&dup); err != nil {
		return store.Effect{}, store.SettlementEvent{}, err
	}
	if dup {
		return store.Effect{}, store.SettlementEvent{}, store.ErrConflict
	}

	demand, attract := store.InvestBuffMagnitude(costCents)
	e := store.Effect{
		ID: id.New(), LeagueID: leagueID, IssuerID: issuerID, GranteeID: granteeID,
		Kind: store.EffectInvestmentOffice, CostCents: costCents, DemandBoost: demand, DemandKind: demandKind,
		AttractRate: attract, TicksRemaining: days, Created: p.clock(),
	}
	if err := writeEffectTx(ctx, tx, e); err != nil {
		return store.Effect{}, store.SettlementEvent{}, err
	}
	ev, err := p.appendEventTx(ctx, tx, leagueID, issuerID, granteeID, costCents, "invest:"+e.ID)
	if err != nil {
		return store.Effect{}, store.SettlementEvent{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return store.Effect{}, store.SettlementEvent{}, err
	}
	return e, ev, nil
}

func (p *PG) effectsWhere(where string, args ...any) []store.Effect {
	ctx, cancel := p.ctx()
	defer cancel()
	rows, err := p.pool.Query(ctx,
		`SELECT `+effectCols+` FROM effects WHERE `+where+` AND ticks_remaining>0 ORDER BY created ASC`, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []store.Effect
	for rows.Next() {
		e, err := scanEffect(rows)
		if err != nil {
			return out
		}
		out = append(out, e)
	}
	return out
}

func (p *PG) CityEffects(leagueID, accountID string) []store.Effect {
	return p.effectsWhere(`league_id=$1 AND grantee_id=$2`, leagueID, accountID)
}

func (p *PG) CityEffectsIssued(leagueID, accountID string) []store.Effect {
	return p.effectsWhere(`league_id=$1 AND issuer_id=$2`, leagueID, accountID)
}

func (p *PG) LeagueEffects(leagueID string) []store.Effect {
	return p.effectsWhere(`league_id=$1`, leagueID)
}

// InvestmentHistory returns every "invest:" settlement event in a league, newest first.
func (p *PG) InvestmentHistory(leagueID string) []store.SettlementEvent {
	ctx, cancel := p.ctx()
	defer cancel()
	rows, err := p.pool.Query(ctx,
		`SELECT `+eventCols+` FROM settlement_events
		  WHERE league_id=$1 AND ref LIKE 'invest:%' ORDER BY seq DESC`, leagueID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []store.SettlementEvent
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return out
		}
		out = append(out, e)
	}
	return out
}

// ExpireEffectsTick ages every active effect by one tick, deleting any that reach zero. Returns the count
// deleted. Emits no settlement event (cash already moved at grant). Matches Memory's semantics: decrement all
// active effects, delete those at <=1 remaining.
func (p *PG) ExpireEffectsTick() int {
	ctx, cancel := p.ctx()
	defer cancel()
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return 0
	}
	defer tx.Rollback(ctx)

	// Delete the ones that would hit zero this tick; count them.
	var expired int
	if err := tx.QueryRow(ctx,
		`WITH del AS (DELETE FROM effects WHERE ticks_remaining<=1 RETURNING 1)
		 SELECT count(*) FROM del`).Scan(&expired); err != nil {
		return 0
	}
	if _, err := tx.Exec(ctx, `UPDATE effects SET ticks_remaining = ticks_remaining - 1 WHERE ticks_remaining>1`); err != nil {
		return 0
	}
	if err := tx.Commit(ctx); err != nil {
		return 0
	}
	return expired
}
