package store

import (
	"testing"
	"time"
)

// TestCityProfileHistory_DownsampleAndReliability drives 200 snapshots with monotonically increasing ReportedAt
// (via a settable clock) and verifies: the history caps at 150, the newest 30 are always retained, and the
// server-stamped Reliability reflects the account's on-time reputation.
func TestCityProfileHistory_DownsampleAndReliability(t *testing.T) {
	st := NewMemory("")
	a, _, err := st.CreateAccount()
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	// Settable clock: each Put advances by one minute so ReportedAt strictly increases.
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var tick int
	st.SetClock(func() time.Time { return base.Add(time.Duration(tick) * time.Minute) })

	const n = 200
	for i := 0; i < n; i++ {
		tick = i
		if err := st.PutCityProfile(CityProfile{AccountID: a.ID, Population: 1000 + i}); err != nil {
			t.Fatalf("PutCityProfile %d: %v", i, err)
		}
	}

	hist := st.CityProfileHistory(a.ID)
	if len(hist) != cityProfileHistCap {
		t.Fatalf("history len = %d, want %d", len(hist), cityProfileHistCap)
	}

	// Newest 30 must be the last 30 written (populations 1170..1199, ReportedAt strictly increasing).
	last30 := hist[len(hist)-cityProfileHistKeepRecent:]
	for i, p := range last30 {
		wantPop := 1000 + (n - cityProfileHistKeepRecent) + i // 1170 + i
		if p.Population != wantPop {
			t.Fatalf("last30[%d].Population = %d, want %d", i, p.Population, wantPop)
		}
	}
	// Oldest→newest ordering preserved across the whole retained series.
	for i := 1; i < len(hist); i++ {
		if !hist[i].ReportedAt.After(hist[i-1].ReportedAt) {
			t.Fatalf("history not strictly increasing at %d: %v !> %v", i, hist[i].ReportedAt, hist[i-1].ReportedAt)
		}
	}

	// Reliability is stamped server-side. With no reputation history it's 100.
	if hist[len(hist)-1].Reliability != 100 {
		t.Fatalf("reliability = %d, want 100 (no reputation history)", hist[len(hist)-1].Reliability)
	}

	// Record 1 missed + 1 on-time installment → Reliability 50; the NEXT snapshot must carry it.
	st.mu.Lock()
	st.bumpReliabilityLocked(a.ID, true)
	st.bumpReliabilityLocked(a.ID, false)
	st.mu.Unlock()
	tick = n
	if err := st.PutCityProfile(CityProfile{AccountID: a.ID}); err != nil {
		t.Fatalf("PutCityProfile (post-reputation): %v", err)
	}
	hist = st.CityProfileHistory(a.ID)
	if got := hist[len(hist)-1].Reliability; got != 50 {
		t.Fatalf("stamped reliability = %d, want 50", got)
	}
}

// TestCityProfilePlausibility verifies the anti-cheat posture: out-of-range percentages are CLAMPED in place, a
// normal growth sequence stays Suspect=false, and an absurd one-post population jump sets Suspect=true (flag, not
// reject — the snapshot is still stored).
func TestCityProfilePlausibility(t *testing.T) {
	st := NewMemory("")
	a, _, err := st.CreateAccount()
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	// Out-of-range happiness (and an over-cap population) are clamped, not rejected.
	if err := st.PutCityProfile(CityProfile{AccountID: a.ID, Population: maxPopulation + 5_000_000, Happiness: 250, Crime: -10}); err != nil {
		t.Fatalf("PutCityProfile (clamp): %v", err)
	}
	p, _ := st.CityProfileOf(a.ID)
	if p.Happiness != 100 {
		t.Fatalf("happiness clamped = %d, want 100", p.Happiness)
	}
	if p.Crime != 0 {
		t.Fatalf("crime clamped = %d, want 0", p.Crime)
	}
	if p.Population != maxPopulation {
		t.Fatalf("population clamped = %d, want %d", p.Population, maxPopulation)
	}
	// First post can't be delta-checked → never flagged from the delta path.
	if p.Suspect {
		t.Fatal("first post should not be Suspect from a delta")
	}

	// A normal sequence (modest day-over-day growth) stays Suspect=false.
	st2 := NewMemory("")
	b, _, _ := st2.CreateAccount()
	for _, pop := range []int{10_000, 12_000, 15_000, 18_000} {
		if err := st2.PutCityProfile(CityProfile{AccountID: b.ID, Population: pop}); err != nil {
			t.Fatalf("PutCityProfile normal: %v", err)
		}
		got, _ := st2.CityProfileOf(b.ID)
		if got.Suspect {
			t.Fatalf("normal growth to %d flagged Suspect", pop)
		}
	}

	// An absurd jump from the last value (18k → 5M, far beyond max(20k, 3×18k)) is flagged but still stored.
	if err := st2.PutCityProfile(CityProfile{AccountID: b.ID, Population: 5_000_000}); err != nil {
		t.Fatalf("PutCityProfile jump: %v", err)
	}
	got, ok := st2.CityProfileOf(b.ID)
	if !ok {
		t.Fatal("jump post should still be stored (flag, not reject)")
	}
	if !got.Suspect {
		t.Fatal("absurd population jump should set Suspect=true")
	}
	if got.Population != maxPopulation { // also clamped to the cap
		t.Fatalf("jump population = %d, want clamped to %d", got.Population, maxPopulation)
	}
}

// TestNetCentsSeries_CumulativeCurve verifies the running cumulative net curve across a couple of settlement
// events: +Cents when the account receives, −Cents when it pays.
func TestNetCentsSeries_CumulativeCurve(t *testing.T) {
	st := NewMemory("")
	a, _, _ := st.CreateAccount()
	b, _, _ := st.CreateAccount()
	lg, _ := st.CreateLeague(a.ID, "L")
	if err := st.JoinLeague(b.ID, lg.ID); err != nil {
		t.Fatalf("JoinLeague: %v", err)
	}

	t0 := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	// Append settlement events directly (the curve only reads m.events).
	st.mu.Lock()
	st.events = []SettlementEvent{
		{Seq: 1, LeagueID: lg.ID, PayerID: b.ID, ReceiverID: a.ID, Cents: 500, Created: t0},                 // a +500
		{Seq: 2, LeagueID: lg.ID, PayerID: a.ID, ReceiverID: b.ID, Cents: 200, Created: t0.Add(time.Minute)}, // a -200 → 300
		{Seq: 3, LeagueID: "other", PayerID: b.ID, ReceiverID: a.ID, Cents: 999, Created: t0.Add(2 * time.Minute)}, // other league, ignored
		{Seq: 4, LeagueID: lg.ID, PayerID: b.ID, ReceiverID: a.ID, Cents: 100, Created: t0.Add(3 * time.Minute)}, // a +100 → 400
	}
	st.mu.Unlock()

	series := st.NetCentsSeries(lg.ID, a.ID)
	wantCents := []int64{500, 300, 400}
	if len(series) != len(wantCents) {
		t.Fatalf("series len = %d, want %d (%+v)", len(series), len(wantCents), series)
	}
	for i, w := range wantCents {
		if series[i].Cents != w {
			t.Fatalf("series[%d].Cents = %d, want %d", i, series[i].Cents, w)
		}
	}
	if !series[0].TS.Equal(t0) {
		t.Fatalf("series[0].TS = %v, want %v", series[0].TS, t0)
	}
}
