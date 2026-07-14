package money

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// These explicit vectors are the SOURCE OF TRUTH for the money math. They are asserted here (Go) and emitted to
// testdata/vectors.json (TestEmitVectors) so the net35 C# port (OpenMarkets/Market/Money.cs) can be checked
// against identical numbers — closing Codex #6 across both implementations.

func TestAmortize_ExactSumAndShape(t *testing.T) {
	cases := []struct {
		name  string
		total int64
		n     int
		want  []int64
	}{
		{"even split", 1000, 4, []int64{250, 250, 250, 250}},
		{"remainder front-loaded", 1003, 4, []int64{251, 251, 251, 250}},
		{"one installment", 777, 1, []int64{777}},
		{"tiny bond, 1 cent each", 3, 3, []int64{1, 1, 1}},
		{"remainder one", 100, 3, []int64{34, 33, 33}},
		{"large", 1_000_000_001, 3, []int64{333_333_334, 333_333_334, 333_333_333}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Amortize(tc.total, tc.n)
			if err != nil {
				t.Fatalf("Amortize(%d,%d) error: %v", tc.total, tc.n, err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("len = %d, want %d", len(got), len(tc.want))
			}
			var sum int64
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("installment[%d] = %d, want %d", i, got[i], tc.want[i])
				}
				sum += got[i]
			}
			if sum != tc.total { // the invariant that matters: no drift
				t.Errorf("sum = %d, want %d (DRIFT)", sum, tc.total)
			}
		})
	}
}

// Property: across a wide range, installments always sum exactly to the total and differ by at most 1 cent.
// (total==0 is degenerate — n>total always — and is covered by the error test, so start at 1.)
func TestAmortize_Invariants(t *testing.T) {
	for total := int64(1); total <= 500; total++ {
		for n := 1; int64(n) <= total && n <= MaxInstallments; n++ {
			got, err := Amortize(total, n)
			if err != nil {
				t.Fatalf("Amortize(%d,%d): unexpected error %v", total, n, err)
			}
			var sum, min, max int64 = 0, 1 << 62, -1
			for _, v := range got {
				sum += v
				if v < min {
					min = v
				}
				if v > max {
					max = v
				}
			}
			if sum != total {
				t.Fatalf("Amortize(%d,%d) sum=%d (drift)", total, n, sum)
			}
			if len(got) > 0 && max-min > 1 {
				t.Fatalf("Amortize(%d,%d) spread max-min=%d > 1", total, n, max-min)
			}
		}
	}
}

func TestAmortize_Errors(t *testing.T) {
	if _, err := Amortize(100, 0); !errors.Is(err, ErrBadInstallments) {
		t.Errorf("n=0: got %v, want ErrBadInstallments", err)
	}
	if _, err := Amortize(2, 3); !errors.Is(err, ErrTooManyInstallments) {
		t.Errorf("n>total: got %v, want ErrTooManyInstallments", err)
	}
	if _, err := Amortize(-1, 1); !errors.Is(err, ErrNegative) {
		t.Errorf("negative total: got %v, want ErrNegative", err)
	}
	// allocation guard: n over the cap must error BEFORE attempting a slice (Codex review #3).
	if _, err := Amortize(1<<62, MaxInstallments+1); !errors.Is(err, ErrInstallmentCap) {
		t.Errorf("over cap: got %v, want ErrInstallmentCap", err)
	}
}

func TestTotalDueCents(t *testing.T) {
	cases := []struct {
		principal int64
		bps       int64
		want      int64
	}{
		{10000, 0, 10000},    // 0% → unchanged
		{10000, 2000, 12000}, // 20% (HARSH default min) → 12000
		{10000, 500, 10500},  // 5%
		{333, 2000, 400},     // 333 + round(66.6) = 333+67 = 400 (half-up)
		{1, 5000, 2},         // 1 + round(0.5) = 1+1 = 2 (half-up)
		{1, 4999, 1},         // 1 + round(0.4999) = 1+0 = 1
	}
	for _, tc := range cases {
		got, err := TotalDueCents(tc.principal, tc.bps)
		if err != nil {
			t.Fatalf("TotalDueCents(%d,%d): %v", tc.principal, tc.bps, err)
		}
		if got != tc.want {
			t.Errorf("TotalDueCents(%d,%d) = %d, want %d", tc.principal, tc.bps, got, tc.want)
		}
	}
}

func TestLineValueCents(t *testing.T) {
	cases := []struct {
		qtyFixed       int64 // already scaled by QtyScale (1000)
		unitPriceCents int64
		want           int64
	}{
		{100 * QtyScale, 320, 32000},          // 100 units @ §3.20 → §320.00
		{QtyScale / 2, 320, 160},              // 0.5 units @ 320 → 160 (sub-unit survives, not truncated to 0)
		{QtyScale / 3, 100, 33},               // 0.333 units @ 100 → 33.3 → 33 (half-up)
		{QtyScale/2 + QtyScale/1000, 100, 50}, // 0.501 @100 = 50.1 → 50
		{0, 999, 0},
	}
	for _, tc := range cases {
		got, err := LineValueCents(tc.qtyFixed, tc.unitPriceCents)
		if err != nil {
			t.Fatalf("LineValueCents(%d,%d): %v", tc.qtyFixed, tc.unitPriceCents, err)
		}
		if got != tc.want {
			t.Errorf("LineValueCents(%d,%d) = %d, want %d", tc.qtyFixed, tc.unitPriceCents, got, tc.want)
		}
	}
}

func TestValidateBondTerms(t *testing.T) {
	// HARSH defaults: minPrincipal 100 cents, 20% (2000 bps).
	const minPrincipal = 100
	if _, err := ValidateBondTerms(50, 2000, 4, minPrincipal); !errors.Is(err, ErrBelowMinPrincipal) {
		t.Errorf("below floor: got %v, want ErrBelowMinPrincipal", err)
	}
	total, err := ValidateBondTerms(10000, 2000, 12, minPrincipal)
	if err != nil || total != 12000 {
		t.Errorf("valid: total=%d err=%v, want total=12000 err=nil", total, err)
	}
	// installments within the cap but exceeding total cents → ErrTooManyInstallments (total=100 at 0%, n=120>100)
	if _, err := ValidateBondTerms(100, 0, 120, minPrincipal); !errors.Is(err, ErrTooManyInstallments) {
		t.Errorf("more installments than cents: got %v, want ErrTooManyInstallments", err)
	}
	// installments over the schedule cap → ErrInstallmentCap (checked before the per-cent rule)
	if _, err := ValidateBondTerms(1000000, 0, MaxInstallments+1, minPrincipal); !errors.Is(err, ErrInstallmentCap) {
		t.Errorf("over cap: got %v, want ErrInstallmentCap", err)
	}
	// interest out of range (risk-scan: pathological usury / negative)
	if _, err := ValidateBondTerms(10000, MaxInterestBps+1, 4, minPrincipal); !errors.Is(err, ErrInterestCap) {
		t.Errorf("usury rate: got %v, want ErrInterestCap", err)
	}
	if _, err := ValidateBondTerms(10000, -1, 4, minPrincipal); !errors.Is(err, ErrInterestCap) {
		t.Errorf("negative rate: got %v, want ErrInterestCap", err)
	}
	// Principal above the single-event bookable ceiling → rejected, even when the per-installment amount would
	// fit (the manual-loan principal transfers as ONE settlement event). Regression for the pre-merge Codex HIGH.
	if _, err := ValidateBondTerms(MaxBookableCents+1, 0, MaxInstallments, minPrincipal); !errors.Is(err, ErrAboveMaxBookable) {
		t.Errorf("principal over bookable cap: got %v, want ErrAboveMaxBookable", err)
	}
	// A principal far above the cap whose PER-INSTALLMENT slice would otherwise fit must still be rejected.
	if _, err := ValidateBondTerms(100*MaxBookableCents, 0, MaxInstallments, minPrincipal); !errors.Is(err, ErrAboveMaxBookable) {
		t.Errorf("huge principal, small installments: got %v, want ErrAboveMaxBookable", err)
	}
	// Exactly at the ceiling is allowed.
	if _, err := ValidateBondTerms(MaxBookableCents, 0, 1, minPrincipal); err != nil {
		t.Errorf("principal at the cap: got %v, want nil", err)
	}
}

func TestLargestInstallment_AndBookableCap(t *testing.T) {
	cases := []struct {
		total int64
		n     int
		want  int64
	}{
		{1000, 4, 250}, {1003, 4, 251}, {100, 3, 34}, {777, 1, 777}, {0, 4, 0},
	}
	for _, c := range cases {
		if got := LargestInstallment(c.total, c.n); got != c.want {
			t.Errorf("LargestInstallment(%d,%d) = %d, want %d", c.total, c.n, got, c.want)
		}
	}
	// A bond whose single installment would exceed the bookable ceiling is rejected...
	if _, err := ValidateBondTerms(MaxBookableCents*2, 0, 1, 100); !errors.Is(err, ErrAboveMaxBookable) {
		t.Errorf("over bookable cap: got %v, want ErrAboveMaxBookable", err)
	}
	// ...but the same principal split so each installment fits is accepted.
	if total, err := ValidateBondTerms(MaxBookableCents, 0, 2, 100); err != nil || total != MaxBookableCents {
		t.Errorf("split to fit: total=%d err=%v, want %d/nil", total, err, MaxBookableCents)
	}
}

// TestEmitVectors writes the canonical vector set to testdata/vectors.json for the C# port cross-check.
// It is part of the suite so the file never drifts from the implementation.
func TestEmitVectors(t *testing.T) {
	type amortVec struct {
		Total int64   `json:"total"`
		N     int     `json:"n"`
		Want  []int64 `json:"want"`
	}
	type totalVec struct {
		Principal int64 `json:"principalCents"`
		Bps       int64 `json:"interestBps"`
		Want      int64 `json:"want"`
	}
	type lineVec struct {
		QtyFixed       int64 `json:"qtyFixed"`
		UnitPriceCents int64 `json:"unitPriceCents"`
		Want           int64 `json:"want"`
	}
	vectors := struct {
		QtyScale  int64      `json:"qtyScale"`
		Amortize  []amortVec `json:"amortize"`
		TotalDue  []totalVec `json:"totalDue"`
		LineValue []lineVec  `json:"lineValue"`
	}{
		QtyScale: QtyScale,
		Amortize: []amortVec{
			{1000, 4, mustAmort(t, 1000, 4)},
			{1003, 4, mustAmort(t, 1003, 4)},
			{777, 1, mustAmort(t, 777, 1)},
			{3, 3, mustAmort(t, 3, 3)},
			{100, 3, mustAmort(t, 100, 3)},
			{1000000001, 3, mustAmort(t, 1000000001, 3)},
		},
		TotalDue: []totalVec{
			{10000, 0, mustTotal(t, 10000, 0)},
			{10000, 2000, mustTotal(t, 10000, 2000)},
			{10000, 500, mustTotal(t, 10000, 500)},
			{333, 2000, mustTotal(t, 333, 2000)},
			{1, 5000, mustTotal(t, 1, 5000)},
			{1, 4999, mustTotal(t, 1, 4999)},
		},
		LineValue: []lineVec{
			{100 * QtyScale, 320, mustLine(t, 100*QtyScale, 320)},
			{QtyScale / 2, 320, mustLine(t, QtyScale/2, 320)},
			{QtyScale / 3, 100, mustLine(t, QtyScale/3, 100)},
			{QtyScale/2 + QtyScale/1000, 100, mustLine(t, QtyScale/2+QtyScale/1000, 100)}, // 0.501 → 50
			{0, 999, mustLine(t, 0, 999)},
		},
	}
	want, err := json.MarshalIndent(vectors, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	want = append(want, '\n')
	path := filepath.Join("testdata", "vectors.json")

	if os.Getenv("UPDATE_VECTORS") == "1" {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, want, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	// Default: assert the committed golden matches the implementation, never silently overwrite (Codex review #2).
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v (run `UPDATE_VECTORS=1 go test` to regenerate)", path, err)
	}
	if string(got) != string(want) {
		t.Errorf("%s is stale vs the implementation — run `UPDATE_VECTORS=1 go test ./internal/money` to refresh", path)
	}
}

func mustAmort(t *testing.T, total int64, n int) []int64 {
	t.Helper()
	v, err := Amortize(total, n)
	if err != nil {
		t.Fatal(err)
	}
	return v
}
func mustTotal(t *testing.T, p, bps int64) int64 {
	t.Helper()
	v, err := TotalDueCents(p, bps)
	if err != nil {
		t.Fatal(err)
	}
	return v
}
func mustLine(t *testing.T, q, u int64) int64 {
	t.Helper()
	v, err := LineValueCents(q, u)
	if err != nil {
		t.Fatal(err)
	}
	return v
}
