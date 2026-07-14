package storeconf

import (
	"strings"
	"testing"
	"time"

	"openmarkets/server/internal/market"
	"openmarkets/server/internal/money"
	"openmarkets/server/internal/store"
)

// confStore is the slice of behavior the conformance scenario drives: the full Store plus the small set of
// startup knobs every backend exposes (the conformance scenario needs a deterministic pricer/clock/econ). Both
// *store.Memory and *postgres.PG satisfy it; the suite runs against Memory ALWAYS (validating the suite here)
// and against Postgres ONLY when TEST_DB_URL is set (see postgres/conformance_test.go).
type Store interface {
	store.Store
	SetPricer(store.Pricer)
	SetClock(func() time.Time)
	SetEconParams(store.EconParams)
	// GarnishBond is a duecycle-driven method (not in store.Store) the scenario calls to drive austerity.
	GarnishBond(bondID string) (store.Bond, store.SettlementEvent, bool, error)
	// AdvanceEvents (duecycle-driven, not in store.Store) steps the global price-shock map one tick — the scenario
	// drives it to assert SetEvent's crisis decays while keeping its Name.
	AdvanceEvents()
}

// fixedPricer is the deterministic accept-time price source the scenario freezes against.
func fixedPricer(_ string, c string) (int64, bool) {
	v, ok := map[string]int64{"Oil": 320, "Coal": 250}[c]
	return v, ok
}

// confBasket mirrors the store package's own test basket: offerer A nets +62000 (Oil give 32000, Coal take
// -20000, gold take +50000) → B pays A. With 1 installment the single net transfer is 62000 cents.
func confBasket(a, b, lid string, installments int) store.Trade {
	return store.Trade{
		LeagueID: lid, OfferedBy: a, Counterparty: b, DefaultRateBps: 2000, Installments: installments,
		Items: []store.LineItem{
			{Kind: store.LineCommodity, Commodity: "Oil", QtyFixed: 100 * money.QtyScale, Dir: store.DirGive},
			{Kind: store.LineCommodity, Commodity: "Coal", QtyFixed: 80 * money.QtyScale, Dir: store.DirTake},
			{Kind: store.LineGold, GoldCents: 50000, Dir: store.DirTake},
		},
	}
}

// RunStoreConformance drives a scripted money-path scenario and asserts the invariants + exact outcomes. It is
// exported (capitalized) only by convention within the test package; callers pass a constructor that returns a
// fresh, isolated store (a clean schema for Postgres).
func Run(t *testing.T, newStore func(t *testing.T) Store) {
	t.Helper()
	st := newStore(t)
	// Deterministic clock + pricer + the default (harsh) econ knobs so settlement amounts are exactly known.
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tick := int64(0)
	st.SetClock(func() time.Time { tick++; return base.Add(time.Duration(tick) * time.Second) })
	st.SetPricer(fixedPricer)
	st.SetEconParams(store.DefaultEconParams())

	// ── Accounts + league ─────────────────────────────────────────────────────
	accA, secretA, err := st.CreateAccount()
	if err != nil || secretA == "" {
		t.Fatalf("create A: %v secret=%q", err, secretA)
	}
	if got, err := st.GetAccount(accA.ID); err != nil || got.ID != accA.ID {
		t.Fatalf("get A: %v", err)
	}
	if _, err := st.GetAccount("nope"); err != store.ErrNotFound {
		t.Fatalf("get unknown: got %v want ErrNotFound", err)
	}
	accB, _, _ := st.CreateAccount()
	accC, _, _ := st.CreateAccount()

	lg, err := st.CreateLeague(accA.ID, "L")
	if err != nil {
		t.Fatalf("create league: %v", err)
	}
	if err := st.JoinLeague(accB.ID, lg.ID); err != nil {
		t.Fatalf("join B: %v", err)
	}
	if err := st.JoinLeague(accC.ID, lg.ID); err != nil {
		t.Fatalf("join C: %v", err)
	}
	if err := st.JoinLeague(accB.ID, lg.ID); err != store.ErrAlreadyMember {
		t.Fatalf("double join: got %v want ErrAlreadyMember", err)
	}
	if found, err := st.LeagueByJoinCode(lg.JoinCode); err != nil || found.ID != lg.ID {
		t.Fatalf("byJoinCode: %v", err)
	}
	mem, err := st.LeagueMembers(lg.ID)
	if err != nil || len(mem) != 3 {
		t.Fatalf("members: %v len=%d want 3", err, len(mem))
	}
	if ls, _ := st.LeaguesForAccount(accB.ID); len(ls) != 1 || ls[0].ID != lg.ID {
		t.Fatalf("leaguesForAccount(B) = %+v", ls)
	}

	A, B, C, L := accA.ID, accB.ID, accC.ID, lg.ID

	// ── Reports ────────────────────────────────────────────────────────────────
	if err := st.PutReport(store.Report{AccountID: A, LeagueID: L, Commodity: "Oil", NetSupply: 1000}); err != nil {
		t.Fatalf("putreport: %v", err)
	}
	if err := st.PutReport(store.Report{AccountID: B, LeagueID: L, Commodity: "Oil", NetSupply: -500}); err != nil {
		t.Fatalf("putreport B: %v", err)
	}
	if rs, err := st.LeagueReports(L); err != nil || len(rs) != 2 {
		t.Fatalf("leagueReports: %v len=%d want 2", err, len(rs))
	}
	if mover, err := st.MarketMoverByAccount(L); err != nil || mover[A] != 1000 || mover[B] != 500 {
		t.Fatalf("marketMover = %+v", mover)
	}

	// ── Trade: offer → accept (freeze) → settle ───────────────────────────────
	tr, err := st.CreateTrade(confBasket(A, B, L, 1))
	if err != nil {
		t.Fatalf("create trade: %v", err)
	}
	if tr.Status != store.TradeOffered {
		t.Fatalf("status %s want offered", tr.Status)
	}
	for _, li := range tr.Items {
		if li.ValueCentsAtAccept != 0 {
			t.Fatalf("value frozen at create: %+v", li)
		}
	}
	// Only the counterparty may accept.
	if _, err := st.SetTradeStatus(A, tr.ID, "accept"); err != store.ErrConflict {
		t.Fatalf("offerer accept: got %v want ErrConflict", err)
	}
	tr, err = st.SetTradeStatus(B, tr.ID, "accept")
	if err != nil || tr.Status != store.TradeActive {
		t.Fatalf("accept: %v status=%s", err, tr.Status)
	}
	if tr.OffererNetCents() != 62000 {
		t.Fatalf("frozen net = %d want 62000", tr.OffererNetCents())
	}
	// Receiver (A) can't settle a nonzero installment; net payer (B) can.
	if _, _, err := st.SettleTradeInstallment(A, tr.ID); err != store.ErrConflict {
		t.Fatalf("receiver settle: got %v want ErrConflict", err)
	}
	tr, ev, err := st.SettleTradeInstallment(B, tr.ID)
	if err != nil {
		t.Fatalf("settle: %v", err)
	}
	if ev.PayerID != B || ev.ReceiverID != A || ev.Cents != 62000 {
		t.Fatalf("settle event = %+v want B->A 62000", ev)
	}
	if tr.Status != store.TradeCompleted || tr.Settled != 1 {
		t.Fatalf("trade after settle = status %s settled %d", tr.Status, tr.Settled)
	}

	// ── Trade #2: miss → auto-bond, then settle the bond to completion ────────
	tr2, _ := st.CreateTrade(confBasket(A, B, L, 1))
	tr2, _ = st.SetTradeStatus(B, tr2.ID, "accept")
	tr2, bond, err := st.MissTradeInstallment(tr2.ID)
	if err != nil {
		t.Fatalf("miss: %v", err)
	}
	if tr2.Status != store.TradeCompleted {
		t.Fatalf("missed trade status %s want completed", tr2.Status)
	}
	if bond.DebtorID != B || bond.CreditorID != A || bond.PrincipalCents != 62000 ||
		bond.Status != store.BondActive || bond.TotalDueCents != 74400 || bond.Origin != "trade:"+tr2.ID {
		t.Fatalf("auto-bond = %+v", bond)
	}
	// Settle the auto-bond fully (6 installments → status completed).
	for i := 0; i < bond.Installments; i++ {
		bd, bev, err := st.SettleBondInstallment(B, bond.ID)
		if err != nil {
			t.Fatalf("bond settle %d: %v", i, err)
		}
		if bev.PayerID != B || bev.ReceiverID != A {
			t.Fatalf("bond event %d = %+v want B->A", i, bev)
		}
		if i == bond.Installments-1 && bd.Status != store.BondCompleted {
			t.Fatalf("final bond status %s want completed", bd.Status)
		}
	}

	// ── Manual loan: C lends to B, B repays one, then defaults via misses, garnished, cleared ──
	offered, err := st.OfferLoan(store.Bond{
		LeagueID: L, CreditorID: C, DebtorID: B, ProposedBy: C,
		PrincipalCents: 100000, InterestBps: 1000, Installments: 4,
	})
	if err != nil {
		t.Fatalf("offer loan: %v", err)
	}
	if offered.Status != store.BondOffered || offered.Origin != store.BondOriginManual {
		t.Fatalf("offered loan = %+v", offered)
	}
	// Proposer (C) can't accept own terms; borrower (B) accepts → principal C→B booked.
	if _, _, err := st.AcceptLoan(C, offered.ID); err != store.ErrConflict {
		t.Fatalf("proposer accept: got %v want ErrConflict", err)
	}
	loan, pev, err := st.AcceptLoan(B, offered.ID)
	if err != nil {
		t.Fatalf("accept loan: %v", err)
	}
	if pev.PayerID != C || pev.ReceiverID != B || pev.Cents != 100000 || pev.Ref != "loan:"+loan.ID+":principal" {
		t.Fatalf("principal event = %+v want C->B 100000", pev)
	}
	if loan.Status != store.BondActive || loan.TotalDueCents != 110000 { // 100000 + 10%
		t.Fatalf("active loan = %+v want total 110000", loan)
	}
	// B repays one installment, then misses until terminal default (BondMaxMisses=2).
	if _, _, err := st.SettleBondInstallment(B, loan.ID); err != nil {
		t.Fatalf("loan repay 1: %v", err)
	}
	var defaulted bool
	for i := 0; i < 5 && !defaulted; i++ {
		_, d, err := st.MissBondInstallment(loan.ID)
		if err != nil {
			t.Fatalf("loan miss %d: %v", i, err)
		}
		defaulted = d
	}
	if !defaulted {
		t.Fatalf("loan never defaulted")
	}
	df, _ := st.GetBond(loan.ID)
	if df.Status != store.BondDefaultedReceivable {
		t.Fatalf("defaulted loan status %s want defaultedReceivable", df.Status)
	}
	// City B is now in austerity.
	if aust, outstanding, n := st.CityState(L, B); !aust || n != 1 || outstanding <= 0 {
		t.Fatalf("cityState(B) = aust %v outstanding %d n %d, want austerity", aust, outstanding, n)
	}
	// Garnish until cleared.
	cleared := false
	for i := 0; i < 50 && !cleared; i++ {
		gb, _, _, err := st.GarnishBond(loan.ID)
		if err != nil {
			t.Fatalf("garnish %d: %v", i, err)
		}
		if gb.Status == store.BondCleared || gb.Status == store.BondWrittenOff {
			cleared = true
		}
	}
	if !cleared {
		t.Fatalf("loan never cleared via garnishment")
	}
	if aust, _, _ := st.CityState(L, B); aust {
		t.Fatalf("B still in austerity after clear")
	}

	// ── Investment grant + city profile + history + NetCentsSeries ────────────
	eff, iev, err := st.GrantInvestment(L, A, C, 5_000_000, 3, store.DemandResidential)
	if err != nil {
		t.Fatalf("grant: %v", err)
	}
	if iev.PayerID != A || iev.ReceiverID != C || iev.Cents != 5_000_000 || iev.Ref != "invest:"+eff.ID {
		t.Fatalf("invest event = %+v", iev)
	}
	if got := st.CityEffects(L, C); len(got) != 1 || got[0].ID != eff.ID {
		t.Fatalf("C effects = %+v", got)
	}
	if got := st.CityEffectsIssued(L, A); len(got) != 1 {
		t.Fatalf("A issued effects = %+v", got)
	}
	if got := st.LeagueEffects(L); len(got) != 1 {
		t.Fatalf("league effects = %+v", got)
	}
	if hist := st.InvestmentHistory(L); len(hist) != 1 || hist[0].Ref != "invest:"+eff.ID {
		t.Fatalf("invest history = %+v", hist)
	}
	// Self-grant + duplicate grant both rejected.
	if _, _, err := st.GrantInvestment(L, A, A, 5_000_000, 3, store.DemandResidential); err != store.ErrConflict {
		t.Fatalf("self-grant: got %v want ErrConflict", err)
	}
	if _, _, err := st.GrantInvestment(L, A, C, 5_000_000, 3, store.DemandResidential); err != store.ErrConflict {
		t.Fatalf("dup grant: got %v want ErrConflict", err)
	}

	// ── Bailout: A bails out a fresh default of B ─────────────────────────────
	loan2, _ := st.OfferLoan(store.Bond{
		LeagueID: L, CreditorID: C, DebtorID: B, ProposedBy: C,
		PrincipalCents: 80000, InterestBps: 0, Installments: 2,
	})
	st.AcceptLoan(B, loan2.ID)
	for i := 0; i < 5; i++ {
		if _, d, _ := st.MissBondInstallment(loan2.ID); d {
			break
		}
	}
	d2, _ := st.GetBond(loan2.ID)
	if d2.Status != store.BondDefaultedReceivable {
		t.Fatalf("loan2 status %s want defaulted", d2.Status)
	}
	applied, bevs, err := st.BailoutCity(A, L, B, 1_000_000)
	if err != nil {
		t.Fatalf("bailout: %v", err)
	}
	if applied <= 0 || len(bevs) == 0 {
		t.Fatalf("bailout applied %d events %d", applied, len(bevs))
	}
	for _, e := range bevs {
		if e.PayerID != A {
			t.Fatalf("bailout event payer = %s want A", e.PayerID)
		}
	}
	if aust, _, _ := st.CityState(L, B); aust {
		t.Fatalf("B still in austerity after bailout")
	}
	// Self-bailout rejected.
	if _, _, err := st.BailoutCity(B, L, B, 1000); err != store.ErrConflict {
		t.Fatalf("self-bailout: got %v want ErrConflict", err)
	}

	// ── Co-op MEGAPROJECT (Great Work): contribute goods + § across 2 members → completion grants buffs ──
	// A project with TWO commodity reqs + a § req. A contributes Oil + part of the §; B contributes Coal + the
	// rest of the §; the final contribution COMPLETES it, granting both builders an Effect — with NO settlement
	// event (so AuditLeague stays 0; the only project money is the conserving member→"project:" § transfer).
	proj, err := st.CreateProject(store.Project{
		LeagueID: L, Name: "The Test Foundry", Description: "A conformance Great Work.",
		Reqs:         []store.ProjectReq{{Commodity: "Oil", Qty: 10}, {Commodity: "Coal", Qty: 8}},
		GoldReqCents: 100000, BuffKind: store.DemandWorkplace, BuffMagnitudeCents: 5_000_000, BuffDays: 5,
	})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	if proj.ID == "" || proj.Status != store.ProjectOpen {
		t.Fatalf("created project = %+v want open with id", proj)
	}
	if got, err := st.GetProject(proj.ID); err != nil || got.ID != proj.ID {
		t.Fatalf("getProject: %v %+v", err, got)
	}
	if ps := st.ProjectsFor(L); len(ps) != 1 || ps[0].ID != proj.ID {
		t.Fatalf("projectsFor(L) = %+v want [the project]", ps)
	}
	// A contributes Oil (full req) — not yet complete. Credited == the full requested qty (nothing capped).
	proj, credited, err := st.ContributeProjectGoods(L, A, proj.ID, "Oil", 10)
	if err != nil {
		t.Fatalf("contribute Oil: %v", err)
	}
	if credited != 10 {
		t.Fatalf("Oil credited = %d want 10", credited)
	}
	if proj.Goods["Oil"] != 10 || proj.Status != store.ProjectOpen {
		t.Fatalf("after Oil = %+v want Oil 10, still open", proj)
	}
	// Over-contribution is CAPPED at the remaining requirement: B offers 100 Coal, only 8 are needed — and
	// credited reflects the CAPPED amount (8), not the requested 100, so the client can refund the surplus.
	proj, credited, err = st.ContributeProjectGoods(L, B, proj.ID, "Coal", 100)
	if err != nil {
		t.Fatalf("contribute Coal: %v", err)
	}
	if credited != 8 {
		t.Fatalf("Coal credited = %d want 8 (capped at remaining)", credited)
	}
	if proj.Goods["Coal"] != 8 {
		t.Fatalf("Coal not capped: %d want 8", proj.Goods["Coal"])
	}
	if proj.Status != store.ProjectOpen {
		t.Fatalf("project complete before § met: %+v", proj)
	}
	// A pays part of the §; B pays the rest → the final § contribution COMPLETES the project.
	proj, gev, err := st.ContributeProjectGold(L, A, proj.ID, 40000)
	if err != nil {
		t.Fatalf("contribute § (A): %v", err)
	}
	if gev.PayerID != A || gev.ReceiverID != "project:"+proj.ID || gev.Cents != 40000 || gev.Ref != "project:"+proj.ID {
		t.Fatalf("gold event = %+v want A->project:%s 40000", gev, proj.ID)
	}
	if proj.Status != store.ProjectOpen {
		t.Fatalf("project complete before § fully met: %+v", proj)
	}
	// Over-contribution of § is capped at the remaining 60000 (B offers 999999).
	proj, gev2, err := st.ContributeProjectGold(L, B, proj.ID, 999999)
	if err != nil {
		t.Fatalf("contribute § (B): %v", err)
	}
	if gev2.Cents != 60000 {
		t.Fatalf("§ not capped: %d want 60000", gev2.Cents)
	}
	if proj.Status != store.ProjectCompleted {
		t.Fatalf("project not completed after all reqs met: %+v", proj)
	}
	if proj.Gold != 100000 {
		t.Fatalf("project gold = %d want 100000", proj.Gold)
	}
	// Both contributors are builders and each received a project-buff Effect (rides /citystate).
	if proj.By[A] <= 0 || proj.By[B] <= 0 {
		t.Fatalf("builders not credited: by=%+v", proj.By)
	}
	var aHas, bHas bool
	for _, e := range st.CityEffects(L, A) {
		if e.Kind == store.EffectProjectBuff {
			aHas = true
		}
	}
	for _, e := range st.CityEffects(L, B) {
		if e.Kind == store.EffectProjectBuff {
			bHas = true
		}
	}
	if !aHas || !bHas {
		t.Fatalf("project buff not granted to both builders: A=%v B=%v", aHas, bHas)
	}
	// A further contribution to a completed project is rejected.
	if _, _, err := st.ContributeProjectGoods(L, A, proj.ID, "Oil", 1); err != store.ErrConflict {
		t.Fatalf("contribute to completed project: got %v want ErrConflict", err)
	}
	if _, _, err := st.ContributeProjectGold(L, A, proj.ID, 1); err != store.ErrConflict {
		t.Fatalf("contribute § to completed project: got %v want ErrConflict", err)
	}
	// A project-complete chronicle entry exists.
	var sawProjectComplete bool
	for _, e := range st.Chronicle(L, 0, 500) {
		if e.Kind == "project-complete" {
			if !strings.Contains(e.Text, "built by 2 cities") || !strings.Contains(e.Text, "led by") {
				t.Fatalf("project-complete chronicle text = %q, want builder count and leader", e.Text)
			}
			sawProjectComplete = true
		}
	}
	if !sawProjectComplete {
		t.Fatalf("no project-complete chronicle entry after completion")
	}
	// AuditLeague still balances (the § sits in the project counterparty; buffs emit no event).
	if _, total, err := st.AuditLeague(L); err != nil || total != 0 {
		t.Fatalf("CONSERVATION VIOLATED by project: total=%d err=%v", total, err)
	}

	// Completion buffs are contribution-scaled with shared integer math: top builder receives the full advertised
	// magnitude; a builder at one-third of the top score receives floor + one-third of the non-floor band.
	ratioProj, err := st.CreateProject(store.Project{
		LeagueID: L, Name: "The Ratio Works", Description: "A proportional reward conformance Great Work.",
		Reqs:     []store.ProjectReq{{Commodity: "Oil", Qty: 30}, {Commodity: "Coal", Qty: 10}},
		BuffKind: store.DemandCommercial, BuffMagnitudeCents: 5_000_000, BuffDays: 5,
	})
	if err != nil {
		t.Fatalf("create ratio project: %v", err)
	}
	if ratioProj, _, err = st.ContributeProjectGoods(L, A, ratioProj.ID, "Oil", 30); err != nil {
		t.Fatalf("ratio project A goods: %v", err)
	}
	if ratioProj, _, err = st.ContributeProjectGoods(L, B, ratioProj.ID, "Coal", 10); err != nil {
		t.Fatalf("ratio project B goods: %v", err)
	}
	if ratioProj.Status != store.ProjectCompleted {
		t.Fatalf("ratio project status = %s want completed", ratioProj.Status)
	}
	var aRatio, bRatio *store.Effect
	ratioIssuer := "project:" + ratioProj.ID
	for _, e := range st.CityEffects(L, A) {
		if e.Kind == store.EffectProjectBuff && e.IssuerID == ratioIssuer {
			copy := e
			aRatio = &copy
		}
	}
	for _, e := range st.CityEffects(L, B) {
		if e.Kind == store.EffectProjectBuff && e.IssuerID == ratioIssuer {
			copy := e
			bRatio = &copy
		}
	}
	if aRatio == nil || bRatio == nil {
		t.Fatalf("ratio project effects missing: A=%v B=%v", aRatio != nil, bRatio != nil)
	}
	wantB := store.ProjectScaledBuffMagnitude(5_000_000, 10, 30)
	floor := int64(5_000_000 * store.ProjectRewardFloorNum / store.ProjectRewardFloorDen)
	if aRatio.CostCents != 5_000_000 || aRatio.DemandBoost != 20 || aRatio.AttractRate != 500 {
		t.Fatalf("top builder effect = %+v want full 5M / demand 20 / attract 500", *aRatio)
	}
	if bRatio.CostCents != wantB || bRatio.CostCents <= floor || bRatio.CostCents >= aRatio.CostCents ||
		bRatio.DemandBoost != 13 || bRatio.AttractRate != 333 {
		t.Fatalf("smaller builder effect = %+v want cost %d between floor %d and full %d, demand 13 attract 333",
			*bRatio, wantB, floor, aRatio.CostCents)
	}
	if _, total, err := st.AuditLeague(L); err != nil || total != 0 {
		t.Fatalf("CONSERVATION VIOLATED by proportional project: total=%d err=%v", total, err)
	}
	if built := st.CompletedProjectCountsByBuilder(L); built[A] != 2 || built[B] != 2 || built[C] != 0 {
		t.Fatalf("completed project counts = %+v, want A=2 B=2 C=0", built)
	}

	shieldProj, err := st.CreateProject(store.Project{
		LeagueID: L, Name: "The Shield Works", Description: "A market-shield conformance Great Work.",
		Reqs:     []store.ProjectReq{{Commodity: "Oil", Qty: 5}},
		BuffKind: store.DemandCommercial, BuffMagnitudeCents: 1_000_000, BuffDays: 3,
		TradeRewardKind: store.EffectMarketShield, TradeRewardCommodity: "Oil", TradeRewardPctBips: 3000,
	})
	if err != nil {
		t.Fatalf("create shield project: %v", err)
	}
	if shieldProj, _, err = st.ContributeProjectGoods(L, A, shieldProj.ID, "Oil", 5); err != nil {
		t.Fatalf("complete shield project: %v", err)
	}
	var shieldEffect *store.Effect
	shieldIssuer := "project:" + shieldProj.ID
	for _, e := range st.CityEffects(L, A) {
		if e.Kind == store.EffectMarketShield && e.IssuerID == shieldIssuer {
			copy := e
			shieldEffect = &copy
		}
	}
	if shieldEffect == nil || shieldEffect.Commodity != "Oil" || shieldEffect.TradePctBips != 3000 {
		t.Fatalf("marketShield effect = %+v, want Oil/3000", shieldEffect)
	}
	baseIdx := market.CommodityIndices([]market.Report{{AccountID: A, Commodity: "Oil", NetSupply: 10000}}, market.Params{VolumeRef: 20000, Min: 0.5, Max: 2.0})
	shieldIdx := market.CommodityIndicesWithShields(
		[]market.Report{{AccountID: A, Commodity: "Oil", NetSupply: 10000}},
		market.Params{VolumeRef: 20000, Min: 0.5, Max: 2.0},
		store.MarketShieldsFromEffects(st.LeagueEffects(L)),
	)
	if shieldIdx["Oil"] <= baseIdx["Oil"] {
		t.Fatalf("marketShield did not dampen price movement: base=%v shielded=%v", baseIdx["Oil"], shieldIdx["Oil"])
	}
	if _, total, err := st.AuditLeague(L); err != nil || total != 0 {
		t.Fatalf("CONSERVATION VIOLATED by marketShield project: total=%d err=%v", total, err)
	}

	// priceEdge trade reward: a CLIENT-applied export bonus. The server must GRANT a priceEdge effect (kind +
	// commodity + bips) identically in both backends; the actual §/truck multiplier is booked on the client and
	// has no server index effect. Conservation must hold (the grant carries no settlement event).
	edgeProj, err := st.CreateProject(store.Project{
		LeagueID: L, Name: "The Edge Works", Description: "A price-edge conformance Great Work.",
		Reqs:     []store.ProjectReq{{Commodity: "Oil", Qty: 5}},
		BuffKind: store.DemandCommercial, BuffMagnitudeCents: 1_000_000, BuffDays: 3,
		TradeRewardKind: store.EffectPriceEdge, TradeRewardCommodity: "Oil", TradeRewardPctBips: 800,
	})
	if err != nil {
		t.Fatalf("create edge project: %v", err)
	}
	if edgeProj, _, err = st.ContributeProjectGoods(L, A, edgeProj.ID, "Oil", 5); err != nil {
		t.Fatalf("complete edge project: %v", err)
	}
	var edgeEffect *store.Effect
	edgeIssuer := "project:" + edgeProj.ID
	for _, e := range st.CityEffects(L, A) {
		if e.Kind == store.EffectPriceEdge && e.IssuerID == edgeIssuer {
			copy := e
			edgeEffect = &copy
		}
	}
	if edgeEffect == nil || edgeEffect.Commodity != "Oil" || edgeEffect.TradePctBips != 800 {
		t.Fatalf("priceEdge effect = %+v, want Oil/800", edgeEffect)
	}
	if _, total, err := st.AuditLeague(L); err != nil || total != 0 {
		t.Fatalf("CONSERVATION VIOLATED by priceEdge project: total=%d err=%v", total, err)
	}

	// ── City profile: clamp + suspect flag + history downsample ───────────────
	if err := st.PutCityProfile(store.CityProfile{AccountID: B, Population: 10_000, Happiness: 250, Crime: -5}); err != nil {
		t.Fatalf("putcity 1: %v", err)
	}
	cp, ok := st.CityProfileOf(B)
	if !ok || cp.Happiness != 100 || cp.Crime != 0 || cp.Population != 10_000 {
		t.Fatalf("clamp failed: %+v", cp)
	}
	if cp.Reliability < 0 || cp.Reliability > 100 {
		t.Fatalf("reliability out of range: %d", cp.Reliability)
	}
	// A plausible second post is NOT suspect.
	if err := st.PutCityProfile(store.CityProfile{AccountID: B, Population: 12_000}); err != nil {
		t.Fatalf("putcity 2: %v", err)
	}
	if cp, _ := st.CityProfileOf(B); cp.Suspect {
		t.Fatalf("plausible post flagged suspect")
	}
	// An implausible jump IS suspect (but still stored).
	if err := st.PutCityProfile(store.CityProfile{AccountID: B, Population: 2_000_000}); err != nil {
		t.Fatalf("putcity 3: %v", err)
	}
	cp, _ = st.CityProfileOf(B)
	if !cp.Suspect || cp.Population != 2_000_000 {
		t.Fatalf("implausible jump not flagged/stored: %+v", cp)
	}
	if h := st.CityProfileHistory(B); len(h) != 3 {
		t.Fatalf("history len = %d want 3", len(h))
	}

	// History downsample: push well past the cap; length must stay bounded and newest preserved.
	for i := 0; i < 200; i++ {
		pop := 1_200_000 + i // monotone, plausible single-step deltas
		if err := st.PutCityProfile(store.CityProfile{AccountID: B, Population: pop}); err != nil {
			t.Fatalf("putcity bulk %d: %v", i, err)
		}
	}
	h := st.CityProfileHistory(B)
	if len(h) > 150 {
		t.Fatalf("history not downsampled: len %d > 150", len(h))
	}
	if h[len(h)-1].Population != 1_200_000+199 {
		t.Fatalf("newest history point lost: %d want %d", h[len(h)-1].Population, 1_200_000+199)
	}

	// ── NetCentsSeries: B's curve has points; cumulative net is consistent ────
	series := st.NetCentsSeries(L, B)
	if len(series) == 0 {
		t.Fatalf("net series empty for B")
	}

	// ── SettlementsSince: league-wide feed reader, seq order + sinceSeq/limit ──
	// By now the scenario has booked a trade settle, a bond, and an investment — all into league L.
	feed := st.SettlementsSince(L, 0, 1000)
	if len(feed) == 0 {
		t.Fatalf("SettlementsSince empty after settle+bond+invest")
	}
	for i := 1; i < len(feed); i++ {
		if feed[i].Seq <= feed[i-1].Seq {
			t.Fatalf("SettlementsSince not ascending by seq: %d then %d", feed[i-1].Seq, feed[i].Seq)
		}
		if feed[i].LeagueID != L {
			t.Fatalf("SettlementsSince leaked a non-L event: %+v", feed[i])
		}
	}
	// At least one trade, one bond, and one investment ref appear.
	var sawTrade, sawBond, sawInvest bool
	for _, e := range feed {
		switch {
		case len(e.Ref) >= 6 && e.Ref[:6] == "trade:":
			sawTrade = true
		case len(e.Ref) >= 5 && e.Ref[:5] == "bond:":
			sawBond = true
		case len(e.Ref) >= 7 && e.Ref[:7] == "invest:":
			sawInvest = true
		}
	}
	if !sawTrade || !sawBond || !sawInvest {
		t.Fatalf("SettlementsSince missing expected refs: trade=%v bond=%v invest=%v", sawTrade, sawBond, sawInvest)
	}
	// sinceSeq filters: everything after the first event's seq excludes that first event.
	firstSeq := feed[0].Seq
	sinceFirst := st.SettlementsSince(L, firstSeq, 1000)
	for _, e := range sinceFirst {
		if e.Seq <= firstSeq {
			t.Fatalf("SettlementsSince(sinceSeq) returned seq %d <= %d", e.Seq, firstSeq)
		}
	}
	if len(sinceFirst) != len(feed)-1 {
		t.Fatalf("SettlementsSince sinceSeq filter: got %d want %d", len(sinceFirst), len(feed)-1)
	}
	// limit caps the result.
	if capped := st.SettlementsSince(L, 0, 1); len(capped) != 1 || capped[0].Seq != firstSeq {
		t.Fatalf("SettlementsSince limit=1 = %+v want first event only", capped)
	}
	// Unknown league → empty.
	if got := st.SettlementsSince("nope", 0, 100); len(got) != 0 {
		t.Fatalf("SettlementsSince(unknown league) = %+v want empty", got)
	}

	// ── Chronicle (social slice 2): direct appends + AppendChronicle + queries ──
	// The scenario has already minted "founded" (CreateLeague) + 2× "joined" (JoinLeague B, C) + a "bailout"
	// (the BailoutCity above) into league L. Confirm they're present, league-scoped, ascending by seq.
	chron := st.Chronicle(L, 0, 200)
	if len(chron) < 4 {
		t.Fatalf("chronicle(L) = %d entries, want >=4 (founded+joined×2+bailout): %+v", len(chron), chron)
	}
	for i := 1; i < len(chron); i++ {
		if chron[i].Seq <= chron[i-1].Seq {
			t.Fatalf("chronicle not ascending by seq: %d then %d", chron[i-1].Seq, chron[i].Seq)
		}
		if chron[i].LeagueID != L {
			t.Fatalf("chronicle leaked a non-L entry: %+v", chron[i])
		}
	}
	if chron[0].Kind != "founded" || chron[0].ActorID != A {
		t.Fatalf("first chronicle entry = %+v, want founded by A", chron[0])
	}
	var sawJoined, sawBailout bool
	for _, e := range chron {
		if e.Text == "" {
			t.Fatalf("chronicle entry has empty frozen text: %+v", e)
		}
		switch e.Kind {
		case "joined":
			sawJoined = true
		case "bailout":
			sawBailout = true
			if e.ActorID != A || e.TargetID != B || e.Cents <= 0 {
				t.Fatalf("bailout chronicle = %+v, want actor A target B cents>0", e)
			}
		}
	}
	if !sawJoined || !sawBailout {
		t.Fatalf("chronicle missing kinds: joined=%v bailout=%v", sawJoined, sawBailout)
	}
	// since/limit filters.
	firstChronSeq := chron[0].Seq
	sinceChron := st.Chronicle(L, firstChronSeq, 200)
	for _, e := range sinceChron {
		if e.Seq <= firstChronSeq {
			t.Fatalf("chronicle(sinceSeq) returned seq %d <= %d", e.Seq, firstChronSeq)
		}
	}
	if len(sinceChron) != len(chron)-1 {
		t.Fatalf("chronicle sinceSeq filter: got %d want %d", len(sinceChron), len(chron)-1)
	}
	if capped := st.Chronicle(L, 0, 1); len(capped) != 1 || capped[0].Seq != firstChronSeq {
		t.Fatalf("chronicle limit=1 = %+v want first entry only", capped)
	}
	// Unknown league → empty.
	if got := st.Chronicle("nope", 0, 100); len(got) != 0 {
		t.Fatalf("chronicle(unknown league) = %+v want empty", got)
	}
	// AppendChronicle assigns a strictly-increasing, league-SHARED seq (across leagues). Mint a 2nd league and
	// append into both, asserting the shared counter interleaves monotonically.
	lg2, err := st.CreateLeague(accA.ID, "L2") // CreateLeague itself appends a "founded" into L2
	if err != nil {
		t.Fatalf("create L2: %v", err)
	}
	e1, err := st.AppendChronicle(store.ChronicleEntry{LeagueID: L, Kind: "austerity", ActorID: B, Text: "B fell into austerity."})
	if err != nil {
		t.Fatalf("append chronicle L: %v", err)
	}
	e2, err := st.AppendChronicle(store.ChronicleEntry{LeagueID: lg2.ID, Kind: "record-trade", ActorID: A, Text: "record."})
	if err != nil {
		t.Fatalf("append chronicle L2: %v", err)
	}
	if e2.Seq <= e1.Seq {
		t.Fatalf("chronicle seq not strictly increasing across leagues: L=%d then L2=%d", e1.Seq, e2.Seq)
	}
	if e1.Created.IsZero() || e2.Created.IsZero() {
		t.Fatalf("AppendChronicle did not stamp Created: %+v %+v", e1, e2)
	}
	// Drop the throwaway 2nd league so the later admin-surface assertions still see exactly league L. (The
	// chronicle is intentionally NOT cascaded by DeleteLeague — the saga outlives the league, matching Memory.)
	if err := st.DeleteLeague(lg2.ID); err != nil {
		t.Fatalf("delete L2: %v", err)
	}

	// ── ChronicleOnThisDay: filters by (month, day) of a PRIOR day ─────────────
	// Freeze the clock to a fixed instant; append a "today" entry and a "one year ago, same M/D" entry, then
	// query as of today. Only the prior-year entry comes back.
	today := time.Date(2027, 3, 14, 12, 0, 0, 0, time.UTC)
	st.SetClock(func() time.Time { return today })
	if _, err := st.AppendChronicle(store.ChronicleEntry{LeagueID: L, Kind: "joined", ActorID: C, Text: "today entry."}); err != nil {
		t.Fatalf("append today: %v", err)
	}
	priorYear := time.Date(2026, 3, 14, 9, 0, 0, 0, time.UTC) // same month/day, earlier year
	st.SetClock(func() time.Time { return priorYear })
	if _, err := st.AppendChronicle(store.ChronicleEntry{LeagueID: L, Kind: "joined", ActorID: C, Text: "prior-year entry."}); err != nil {
		t.Fatalf("append prior year: %v", err)
	}
	otherDay := time.Date(2026, 5, 1, 9, 0, 0, 0, time.UTC) // different month/day → must NOT match
	st.SetClock(func() time.Time { return otherDay })
	if _, err := st.AppendChronicle(store.ChronicleEntry{LeagueID: L, Kind: "joined", ActorID: C, Text: "other-day entry."}); err != nil {
		t.Fatalf("append other day: %v", err)
	}
	otd := st.ChronicleOnThisDay(L, today)
	if len(otd) != 1 || otd[0].Text != "prior-year entry." {
		t.Fatalf("ChronicleOnThisDay = %+v, want exactly the prior-year 03-14 entry", otd)
	}
	// Restore the scenario clock advance for any later use (the suite ends shortly, but keep it well-behaved).
	tick2 := int64(1_000_000)
	st.SetClock(func() time.Time { tick2++; return base.Add(time.Duration(tick2) * time.Second) })

	// ── Conservation invariant: AuditLeague total == 0 ────────────────────────
	net, total, err := st.AuditLeague(L)
	if err != nil {
		t.Fatalf("audit: %v", err)
	}
	if total != 0 {
		t.Fatalf("CONSERVATION VIOLATED: AuditLeague total = %d, want 0 (net=%+v)", total, net)
	}
	if _, _, err := st.AuditLeague("nope"); err != store.ErrNotFound {
		t.Fatalf("audit unknown league: got %v want ErrNotFound", err)
	}

	// ── Admin surface: stats reflect the league, then DeleteLeague cascades it away ──
	stats := st.AdminStats()
	if stats.Accounts != 3 {
		t.Fatalf("stats.Accounts = %d want 3", stats.Accounts)
	}
	if stats.Leagues != 1 {
		t.Fatalf("stats.Leagues = %d want 1", stats.Leagues)
	}
	if stats.Members != 3 {
		t.Fatalf("stats.Members = %d want 3", stats.Members)
	}
	if stats.TradesByStatus[store.TradeCompleted] == 0 {
		t.Fatalf("stats.TradesByStatus[completed] = 0, want >0 (%+v)", stats.TradesByStatus)
	}
	if len(stats.BondsByStatus) == 0 {
		t.Fatalf("stats.BondsByStatus empty, want bonds from the scenario")
	}
	if stats.SettlementVolumeCents <= 0 || stats.SettlementEventCount <= 0 {
		t.Fatalf("stats settlement = vol %d count %d, want >0", stats.SettlementVolumeCents, stats.SettlementEventCount)
	}
	if ls := st.AllLeagues(); len(ls) != 1 || ls[0].ID != L {
		t.Fatalf("AllLeagues = %+v want [%s]", ls, L)
	}
	// RemoveMember drops a membership but leaves the league + the account.
	if err := st.RemoveMember(C, L); err != nil {
		t.Fatalf("removeMember(C): %v", err)
	}
	if st.IsMember(C, L) {
		t.Fatalf("C still a member after RemoveMember")
	}
	if mem, _ := st.LeagueMembers(L); len(mem) != 2 {
		t.Fatalf("members after RemoveMember = %d want 2", len(mem))
	}
	if err := st.RemoveMember(C, L); err != store.ErrNotFound {
		t.Fatalf("removeMember non-member: got %v want ErrNotFound", err)
	}
	if err := st.RemoveMember(A, "nope"); err != store.ErrNotFound {
		t.Fatalf("removeMember unknown league: got %v want ErrNotFound", err)
	}
	// A fresh open project in L so DeleteLeague's projects cascade is exercised on a live (not just completed) one.
	delProj, err := st.CreateProject(store.Project{
		LeagueID: L, Name: "Doomed Works", Reqs: []store.ProjectReq{{Commodity: "Oil", Qty: 5}},
		BuffKind: store.DemandWorkplace, BuffMagnitudeCents: 1_000_000, BuffDays: 3,
	})
	if err != nil {
		t.Fatalf("create project to be deleted: %v", err)
	}
	if ps := st.ProjectsFor(L); len(ps) == 0 {
		t.Fatalf("ProjectsFor(L) empty before delete, want the doomed project")
	}
	// DeleteLeague removes the league and all of its trades/bonds.
	if err := st.DeleteLeague(L); err != nil {
		t.Fatalf("deleteLeague: %v", err)
	}
	if err := st.DeleteLeague(L); err != store.ErrNotFound {
		t.Fatalf("deleteLeague again: got %v want ErrNotFound", err)
	}
	if ls := st.AllLeagues(); len(ls) != 0 {
		t.Fatalf("AllLeagues after delete = %+v want empty", ls)
	}
	if _, err := st.GetLeague(L); err != store.ErrNotFound {
		t.Fatalf("GetLeague after delete: got %v want ErrNotFound", err)
	}
	if _, err := st.GetTrade(tr.ID); err != store.ErrNotFound {
		t.Fatalf("trade survived DeleteLeague: got %v want ErrNotFound", err)
	}
	if _, err := st.GetBond(bond.ID); err != store.ErrNotFound {
		t.Fatalf("bond survived DeleteLeague: got %v want ErrNotFound", err)
	}
	// Projects cascade too: ProjectsFor is empty and the deleted league's project is gone.
	if ps := st.ProjectsFor(L); len(ps) != 0 {
		t.Fatalf("ProjectsFor after delete = %+v want empty", ps)
	}
	if _, err := st.GetProject(delProj.ID); err != store.ErrNotFound {
		t.Fatalf("project survived DeleteLeague: got %v want ErrNotFound", err)
	}
	after := st.AdminStats()
	if after.Leagues != 0 || after.Members != 0 {
		t.Fatalf("stats after delete = leagues %d members %d, want 0/0", after.Leagues, after.Members)
	}
	if after.Accounts != 3 {
		t.Fatalf("DeleteLeague removed accounts: %d want 3", after.Accounts)
	}

	// ── SetEvent: a named crisis rides the global event map; decay keeps the Name until it clears ──
	// SetEvent a 2-tick named crisis. EventStates reflects Mult + Name immediately.
	st.SetEvent("Oil", market.EventState{Mult: 1.6, TicksLeft: 2, Name: "The Oil Blight", Narrative: "n", Kind: "crisis"})
	es := st.EventStates()
	if e := es["Oil"]; e.Mult != 1.6 || e.Name != "The Oil Blight" {
		t.Fatalf("SetEvent not reflected by EventStates: %+v", e)
	}
	// AdvanceEvents decays it (2→1 ticks) but KEEPS the Name.
	st.AdvanceEvents()
	es = st.EventStates()
	if e, ok := es["Oil"]; !ok || e.TicksLeft != 1 || e.Name != "The Oil Blight" {
		t.Fatalf("AdvanceEvents dropped/garbled the crisis: ok=%v %+v", ok, e)
	}
	// One more tick clears it (1→0 → removed), so the name is gone.
	st.AdvanceEvents()
	if _, ok := st.EventStates()["Oil"]; ok {
		t.Fatalf("crisis should have cleared after its last tick")
	}

	// ── Epoch is stable + non-empty ───────────────────────────────────────────
	if st.Epoch() == "" {
		t.Fatalf("empty epoch")
	}
	if err := st.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
}
