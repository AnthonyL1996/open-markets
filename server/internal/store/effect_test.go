package store

import "testing"

// A grant transfers § issuer→grantee (conserving cash) and creates a capped, time-boxed buff on the grantee.
func TestGrantInvestment_ConservesCashAndCreatesBuff(t *testing.T) {
	m, a, b, lid := tradeLeague(t)

	const cost int64 = 5_000_000 // §50,000 → both magnitudes at their caps
	e, ev, err := m.GrantInvestment(lid, a, b, cost, 7, DemandCommercial)
	if err != nil {
		t.Fatalf("grant: %v", err)
	}
	if e.IssuerID != a || e.GranteeID != b || e.Kind != EffectInvestmentOffice {
		t.Fatalf("effect fields wrong: %+v", e)
	}
	if e.DemandKind != DemandCommercial {
		t.Fatalf("demand kind should be stored, got %q", e.DemandKind)
	}
	if e.CostCents != cost {
		t.Fatalf("invested § should be stored on the effect, got %d want %d", e.CostCents, cost)
	}
	if e.DemandBoost != InvestDemandBoostCap || e.AttractRate != InvestAttractRateCap {
		t.Fatalf("§50k should cap magnitudes, got demand=%d attract=%d", e.DemandBoost, e.AttractRate)
	}
	if e.TicksRemaining != 7 {
		t.Fatalf("days=7 → TicksRemaining=7, got %d", e.TicksRemaining)
	}
	// The cash event is a real issuer→grantee transfer of the full cost.
	if ev.PayerID != a || ev.ReceiverID != b || ev.Cents != cost {
		t.Fatalf("event should be a→b for %d, got %+v", cost, ev)
	}
	// Active on the grantee, not on the issuer.
	if got := m.CityEffects(lid, b); len(got) != 1 || got[0].ID != e.ID {
		t.Fatalf("grantee should have 1 active effect, got %d", len(got))
	}
	if got := m.CityEffects(lid, a); len(got) != 0 {
		t.Fatalf("issuer should have 0 active effects, got %d", len(got))
	}
	// Cash conservation: every transfer is zero-sum.
	if _, total, err := m.AuditLeague(lid); err != nil || total != 0 {
		t.Fatalf("conservation broken: total=%d err=%v", total, err)
	}
}

func TestGrantInvestment_RejectsSelfNonMemberAndCooldown(t *testing.T) {
	m, a, b, lid := tradeLeague(t)

	if _, _, err := m.GrantInvestment(lid, a, a, 1_000_000, 5, DemandResidential); err != ErrConflict {
		t.Fatalf("self-grant should be ErrConflict, got %v", err)
	}
	stranger, _, _ := m.CreateAccount()
	if _, _, err := m.GrantInvestment(lid, a, stranger.ID, 1_000_000, 5, DemandResidential); err != ErrNotFound {
		t.Fatalf("non-member grantee should be ErrNotFound, got %v", err)
	}
	if _, _, err := m.GrantInvestment(lid, a, b, 1_000_000, 5, DemandResidential); err != nil {
		t.Fatalf("first grant: %v", err)
	}
	// Cooldown: a second active grant from the same issuer to the same grantee is refused.
	if _, _, err := m.GrantInvestment(lid, a, b, 1_000_000, 5, DemandResidential); err != ErrConflict {
		t.Fatalf("duplicate active grant should be ErrConflict, got %v", err)
	}
	// A DIFFERENT issuer can still invest in the same grantee (no global lock).
	c, _, _ := m.CreateAccount()
	if err := m.JoinLeague(c.ID, lid); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.GrantInvestment(lid, c.ID, b, 1_000_000, 5, DemandResidential); err != nil {
		t.Fatalf("different issuer should be allowed: %v", err)
	}
}

func TestGrantInvestment_ClampsDaysAndConservesOnHugeCost(t *testing.T) {
	m, a, b, lid := tradeLeague(t)
	// Over-long duration is clamped; a huge cost caps the buff (not the cash, which still moves in full).
	e, ev, err := m.GrantInvestment(lid, a, b, InvestMaxCostCents, 999, DemandWorkplace)
	if err != nil {
		t.Fatalf("grant: %v", err)
	}
	if e.TicksRemaining != InvestMaxDays {
		t.Fatalf("days clamp: want %d got %d", InvestMaxDays, e.TicksRemaining)
	}
	if e.DemandBoost != InvestDemandBoostCap || e.AttractRate != InvestAttractRateCap {
		t.Fatalf("magnitudes should cap: %+v", e)
	}
	if ev.Cents != InvestMaxCostCents {
		t.Fatalf("full cost should transfer: got %d", ev.Cents)
	}
	if _, total, _ := m.AuditLeague(lid); total != 0 {
		t.Fatalf("conservation broken: total=%d", total)
	}
}

// A third party bails out a defaulted debtor: § transfers bailer→creditor (conserving), the balance shrinks, and
// when the last defaulted bond clears the debtor leaves austerity.
func TestBailoutCity_ClearsAusterityAndConserves(t *testing.T) {
	m, a, b, lid := tradeLeague(t) // a = creditor, b = debtor
	c, _, _ := m.CreateAccount()   // c = bailer (a third member)
	if err := m.JoinLeague(c.ID, lid); err != nil {
		t.Fatal(err)
	}
	// Inject a terminally-defaulted bond: b owes a a frozen §1,000.
	m.mu.Lock()
	m.bonds["bond1"] = Bond{
		ID: "bond1", LeagueID: lid, DebtorID: b, CreditorID: a,
		Status: BondDefaultedReceivable, DefaultedRemainingCents: 100000,
	}
	m.mu.Unlock()
	if aust, _, _ := m.CityState(lid, b); !aust {
		t.Fatal("b should be in austerity with a defaulted bond")
	}

	// Partial bail-out of §600 → one event c→a, §400 still outstanding, still in austerity.
	applied, evs, err := m.BailoutCity(c.ID, lid, b, 60000)
	if err != nil || applied != 60000 {
		t.Fatalf("partial bailout: applied=%d err=%v", applied, err)
	}
	if len(evs) != 1 || evs[0].PayerID != c.ID || evs[0].ReceiverID != a || evs[0].Cents != 60000 {
		t.Fatalf("event should be c→a for 60000: %+v", evs)
	}
	if aust, out, _ := m.CityState(lid, b); !aust || out != 40000 {
		t.Fatalf("expected still-austerity with §400 left: aust=%v out=%d", aust, out)
	}

	// Over-pay the rest (§1,000 offered) → capped at the §400 remaining; austerity clears.
	applied2, _, err := m.BailoutCity(c.ID, lid, b, 100000)
	if err != nil || applied2 != 40000 {
		t.Fatalf("final bailout should cap at the §400 remaining: applied=%d err=%v", applied2, err)
	}
	if aust, out, _ := m.CityState(lid, b); aust || out != 0 {
		t.Fatalf("austerity should clear at zero balance: aust=%v out=%d", aust, out)
	}

	// Conservation: c paid §1,000, a received §1,000, total nets to 0.
	net, total, _ := m.AuditLeague(lid)
	if total != 0 || net[c.ID] != -100000 || net[a] != 100000 {
		t.Fatalf("conservation: total=%d c=%d a=%d", total, net[c.ID], net[a])
	}
	// Nothing left to bail → ErrConflict; a non-member debtor → ErrNotFound.
	if _, _, err := m.BailoutCity(c.ID, lid, b, 10000); err != ErrConflict {
		t.Fatalf("expected ErrConflict when nothing defaulted, got %v", err)
	}
	stranger, _, _ := m.CreateAccount()
	if _, _, err := m.BailoutCity(c.ID, lid, stranger.ID, 10000); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound for a non-member debtor, got %v", err)
	}
	// Self-bailout is rejected (bailout is a co-op rescue, not a self-escape path).
	if _, _, err := m.BailoutCity(b, lid, b, 10000); err != ErrConflict {
		t.Fatalf("expected ErrConflict for self-bailout, got %v", err)
	}
}

func TestExpireEffectsTick_ExpiresAfterItsDays(t *testing.T) {
	m, a, b, lid := tradeLeague(t)
	if _, _, err := m.GrantInvestment(lid, a, b, 1_000_000, 3, DemandResidential); err != nil {
		t.Fatalf("grant: %v", err)
	}
	// 2 ticks: still active (3 → 2 → 1).
	m.ExpireEffectsTick()
	m.ExpireEffectsTick()
	if got := m.CityEffects(lid, b); len(got) != 1 {
		t.Fatalf("after 2 ticks the buff should still be active, got %d", len(got))
	}
	// 3rd tick removes it (1 → expired).
	m.ExpireEffectsTick()
	if got := m.CityEffects(lid, b); len(got) != 0 {
		t.Fatalf("after 3 ticks the buff should be gone, got %d", len(got))
	}
	// Expiry emits no settlement event → conservation still holds.
	if _, total, _ := m.AuditLeague(lid); total != 0 {
		t.Fatalf("conservation broken after expiry: total=%d", total)
	}
}
