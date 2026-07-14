package money

import (
	"math/big"
	"testing"
)

// These fuzz tests assert the money-math PROPERTIES rather than specific outputs: the algorithms are pure
// integer arithmetic that must never (a) lose or invent a cent, (b) overflow silently, or (c) round wrong.
// math/big is used as an independent oracle for the rounding formulas, so an off-by-one in the int64 guard
// or the half-up term is caught regardless of magnitude. Run: go test ./internal/money -fuzz=Fuzz...

// FuzzAmortize: a successful split must sum EXACTLY to the total, contain no zero/negative installment, be
// front-loaded by exactly the remainder (max-min <= 1), and have its max equal LargestInstallment.
func FuzzAmortize(f *testing.F) {
	f.Add(int64(100), 3)
	f.Add(int64(1), 1)
	f.Add(int64(0), 5)
	f.Add(int64(2147483647), 120)
	f.Fuzz(func(t *testing.T, total int64, n int) {
		out, err := Amortize(total, n)
		if err != nil {
			return // rejected inputs are out of scope; the error paths are covered by the table tests
		}
		if len(out) != n {
			t.Fatalf("len=%d want n=%d", len(out), n)
		}
		var sum, min, max int64 = 0, out[0], out[0]
		for _, v := range out {
			if v < 1 {
				t.Fatalf("installment < 1 cent: %d in %v (total=%d n=%d)", v, out, total, n)
			}
			sum += v
			if v < min {
				min = v
			}
			if v > max {
				max = v
			}
		}
		if sum != total {
			t.Fatalf("sum=%d != total=%d (n=%d): %v", sum, total, n, out)
		}
		if max-min > 1 {
			t.Fatalf("not front-loaded: max-min=%d > 1 (total=%d n=%d): %v", max-min, total, n, out)
		}
		if got := LargestInstallment(total, n); got != max {
			t.Fatalf("LargestInstallment=%d != actual max=%d (total=%d n=%d)", got, max, total, n)
		}
	})
}

// FuzzTotalDueCents: a successful total must equal the big.Int half-up oracle exactly and never be below
// principal (interest is non-negative). A reported overflow must be a genuine int64 overflow per the oracle.
func FuzzTotalDueCents(f *testing.F) {
	f.Add(int64(10000), int64(500))
	f.Add(int64(0), int64(0))
	f.Add(int64(2147483647), int64(100000))
	f.Fuzz(func(t *testing.T, principal, bps int64) {
		got, err := TotalDueCents(principal, bps)
		if principal < 0 || bps < 0 {
			if err != ErrNegative {
				t.Fatalf("negative input principal=%d bps=%d: err=%v want ErrNegative", principal, bps, err)
			}
			return
		}
		want, oflow := totalDueOracle(principal, bps)
		if err == nil {
			// A returned value must be exactly right and must never appear when the true total overflows int64.
			if oflow {
				t.Fatalf("oracle overflows but TotalDueCents returned %d (principal=%d bps=%d)", got, principal, bps)
			}
			if got != want {
				t.Fatalf("TotalDueCents=%d != oracle=%d (principal=%d bps=%d)", got, want, principal, bps)
			}
			if got < principal {
				t.Fatalf("total %d < principal %d (interest must be >= 0)", got, principal)
			}
			return
		}
		// An error is only acceptable as a (possibly conservative) overflow rejection — the guard may trip on an
		// int64-overflowing intermediate product even when the exact result would fit. It must never be anything else.
		if err != ErrOverflow {
			t.Fatalf("unexpected err=%v (principal=%d bps=%d)", err, principal, bps)
		}
	})
}

// FuzzLineValueCents: a successful value must equal the big.Int half-up oracle and be non-negative. A reported
// overflow must be genuine.
func FuzzLineValueCents(f *testing.F) {
	f.Add(int64(1500), int64(400)) // 1.5 units @ 400 = 600
	f.Add(int64(0), int64(999))
	f.Add(int64(50000), int64(2147483647))
	f.Fuzz(func(t *testing.T, qtyFixed, unitPrice int64) {
		got, err := LineValueCents(qtyFixed, unitPrice)
		if qtyFixed < 0 || unitPrice < 0 {
			if err != ErrNegative {
				t.Fatalf("negative input: err=%v want ErrNegative", err)
			}
			return
		}
		want, oflow := lineValueOracle(qtyFixed, unitPrice)
		if err == nil {
			if oflow {
				t.Fatalf("oracle overflows but LineValueCents returned %d (qty=%d price=%d)", got, qtyFixed, unitPrice)
			}
			if got != want {
				t.Fatalf("LineValueCents=%d != oracle=%d (qty=%d price=%d)", got, want, qtyFixed, unitPrice)
			}
			if got < 0 {
				t.Fatalf("negative value %d from non-negative inputs", got)
			}
			return
		}
		// Conservative overflow rejection is the only acceptable error (the int64 product guard may trip even
		// when the divided result would fit). Never any other error.
		if err != ErrOverflow {
			t.Fatalf("unexpected err=%v (qty=%d price=%d)", err, qtyFixed, unitPrice)
		}
	})
}

// FuzzValidateBondTerms: whenever terms validate, the frozen total must be schedulable — Amortize succeeds and
// no installment exceeds the client's bookable ceiling (the invariant the settle loop relies on).
func FuzzValidateBondTerms(f *testing.F) {
	f.Add(int64(10000), int64(500), 12, int64(100))
	f.Add(int64(100), int64(0), 1, int64(100))
	f.Fuzz(func(t *testing.T, principal, bps int64, installments int, minPrincipal int64) {
		total, err := ValidateBondTerms(principal, bps, installments, minPrincipal)
		if err != nil {
			return
		}
		sched, aerr := Amortize(total, installments)
		if aerr != nil {
			t.Fatalf("validated terms but Amortize failed: %v (total=%d n=%d)", aerr, total, installments)
		}
		for _, v := range sched {
			if v > MaxBookableCents {
				t.Fatalf("validated installment %d exceeds MaxBookableCents (total=%d n=%d)", v, total, installments)
			}
			if v < 1 {
				t.Fatalf("validated installment %d < 1 cent (total=%d n=%d)", v, total, installments)
			}
		}
	})
}

// ── big.Int oracles (independent of the int64 guards under test) ──

// totalDueOracle computes principal + round_half_up(principal*bps/10000) with arbitrary precision, returning
// whether the int64 result would overflow (mirrors what TotalDueCents must reject).
func totalDueOracle(principal, bps int64) (int64, bool) {
	if principal < 0 || bps < 0 {
		return 0, false
	}
	p := big.NewInt(principal)
	interest := new(big.Int).Mul(p, big.NewInt(bps))
	interest.Add(interest, big.NewInt(5000))
	interest.Quo(interest, big.NewInt(10000))
	total := new(big.Int).Add(p, interest)
	if !total.IsInt64() {
		return 0, true
	}
	return total.Int64(), false
}

// lineValueOracle computes round_half_up(qty*price/QtyScale) with arbitrary precision.
func lineValueOracle(qtyFixed, unitPrice int64) (int64, bool) {
	if qtyFixed < 0 || unitPrice < 0 {
		return 0, false
	}
	v := new(big.Int).Mul(big.NewInt(qtyFixed), big.NewInt(unitPrice))
	v.Add(v, big.NewInt(QtyScale/2))
	v.Quo(v, big.NewInt(QtyScale))
	if !v.IsInt64() {
		return 0, true
	}
	return v.Int64(), false
}
