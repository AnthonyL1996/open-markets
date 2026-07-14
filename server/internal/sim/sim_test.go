package sim

import "testing"

// TestRunDefaults is the headline property test: the default-sized random economy must hold every
// system-level invariant (cash conservation, austerity escapability, no stranded trades/bonds, no overflow,
// fully drained) without panicking.
func TestRunDefaults(t *testing.T) {
	assertInvariants(t, Run(Defaults()))
}

// TestRunManySeeds exercises a spread of seeds so a one-off random path can't hide a violation.
func TestRunManySeeds(t *testing.T) {
	for seed := int64(1); seed <= 25; seed++ {
		p := Defaults()
		p.Seed = seed
		res := Run(p)
		if !res.OK {
			t.Errorf("seed %d violated an invariant: %+v", seed, res)
		}
	}
}

// TestRunLarger raises the member/round counts to stress the settle/garnish loops harder.
func TestRunLarger(t *testing.T) {
	assertInvariants(t, Run(Params{Members: 9, Rounds: 1500, MaxDrainTicks: 20000, Seed: 7}))
}

// TestExercisesHardPaths guards against a vacuous pass: a clean final state proves nothing if the run never
// actually defaulted a bond or pushed a city into austerity. Across many seeds the harness must hit both.
func TestExercisesHardPaths(t *testing.T) {
	var sawDefault, sawAusterity bool
	for seed := int64(1); seed <= 25; seed++ {
		p := Defaults()
		p.Seed = seed
		res := Run(p)
		if res.PeakDefaultedBonds > 0 {
			sawDefault = true
		}
		if res.PeakAusterityCities > 0 {
			sawAusterity = true
		}
	}
	if !sawDefault {
		t.Error("no seed ever produced a defaulted bond — the default path is untested (vacuous pass)")
	}
	if !sawAusterity {
		t.Error("no seed ever pushed a city into austerity — the austerity path is untested (vacuous pass)")
	}
}

func assertInvariants(t *testing.T, r Result) {
	t.Helper()
	if r.ConservationTotal != 0 {
		t.Errorf("cash NOT conserved: settlement-log net total = %d (want 0)", r.ConservationTotal)
	}
	if r.VoidNetCents != 0 {
		t.Errorf("%d cents booked against a void counterparty — money entered/left the closed system", r.VoidNetCents)
	}
	if r.StrangerAccounts != 0 {
		t.Errorf("%d settlement counterpart(ies) outside the league — leaked counterparty", r.StrangerAccounts)
	}
	if r.StuckBondsDefaulted != 0 {
		t.Errorf("escapability failure: %d bond(s) stuck defaulted-receivable after drain", r.StuckBondsDefaulted)
	}
	if r.AusterityCities != 0 {
		t.Errorf("escapability failure: %d city(ies) still in austerity after drain", r.AusterityCities)
	}
	if r.StuckActiveTrades != 0 {
		t.Errorf("%d trade(s) stranded active after drain", r.StuckActiveTrades)
	}
	if r.SettledOverflow != 0 {
		t.Errorf("%d trade/bond(s) settled past their installment count", r.SettledOverflow)
	}
	if !r.FullyDrained {
		t.Errorf("did not reach steady state within %d drain ticks", r.DrainTicks)
	}
	if !r.OK {
		t.Errorf("Run reported not OK: %+v", r)
	}
}
