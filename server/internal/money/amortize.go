// Package money is the canonical integer-cents money math for trades and bonds (GATE-A).
//
// Everything here is pure integer arithmetic — no floats, no time, no deps — so the same algorithm can be
// mirrored exactly in the net35 C# client (OpenMarkets/Market/Money.cs) and cross-checked against the shared
// vectors in testdata/vectors.json. This resolves Codex finding #6 (rounding can farm free/inflated debt):
//
//   - amounts are int64 CENTS; quantities are fixed-point integers scaled by QtyScale (no (long)qty truncation),
//   - a bond's TotalDueCents is computed ONCE from principal + flat interest (basis points, not %/float),
//   - Amortize splits a total into installments that sum EXACTLY to the total (quotient + remainder),
//   - preconditions (min principal, installments <= total) guarantee every installment pays >= 1 cent.
package money

import "errors"

// QtyScale is the fixed-point scale for quantities: a qty of 1.0 unit is stored as 1*QtyScale.
// Using milli-units lets sub-unit quantities (e.g. 0.5) survive without truncation.
const QtyScale int64 = 1000

// MaxInstallments caps a schedule length. It matches the contract model's existing installment ceiling and,
// crucially, bounds Amortize's allocation so an absurd n can't attempt an impossible slice (Codex review #3).
const MaxInstallments = 120

// MaxBookableCents is the largest cents a single settlement installment may carry: the client books into the
// game's int-cents treasury (EconomyManager), so anything above int32 max can't be applied exactly and would be
// silently clamped (Codex M5 review). Trades/bonds are validated so no installment exceeds this.
const MaxBookableCents int64 = 2147483647 // math.MaxInt32

// MaxInterestBps caps a bond/loan interest rate (10000 bps = 100%). Generous (the counterparty must still
// accept), but bounds pathological usury and keeps total-due overflow far away.
const MaxInterestBps int64 = 100000 // 1000%

var (
	// ErrBadInstallments: installments must be >= 1.
	ErrBadInstallments = errors.New("money: installments must be >= 1")
	// ErrTooManyInstallments: more installments than cents, so some installment would be 0 cents.
	ErrTooManyInstallments = errors.New("money: installments exceed total cents (an installment would be 0)")
	// ErrInstallmentCap: schedule longer than MaxInstallments (also an allocation guard).
	ErrInstallmentCap = errors.New("money: installments exceed the maximum schedule length")
	// ErrAboveMaxBookable: an installment exceeds what the client can book into the int-cents treasury.
	ErrAboveMaxBookable = errors.New("money: installment exceeds the maximum bookable amount")
	// ErrInterestCap: interest rate out of range (negative or above MaxInterestBps).
	ErrInterestCap = errors.New("money: interest rate out of range")
	// ErrBelowMinPrincipal: principal under the configured floor.
	ErrBelowMinPrincipal = errors.New("money: principal below minimum")
	// ErrNegative: a negative input where only non-negative is valid.
	ErrNegative = errors.New("money: negative amount")
	// ErrOverflow: an intermediate product would overflow int64.
	ErrOverflow = errors.New("money: arithmetic overflow")
)

// LineValueCents values qtyFixed (a quantity scaled by QtyScale) at unitPriceCents per WHOLE unit.
// Rounds half-up. Overflow-guarded. This is the frozen-at-accept per-line value (Codex #7).
//
//	value = round( qtyFixed * unitPriceCents / QtyScale )
func LineValueCents(qtyFixed, unitPriceCents int64) (int64, error) {
	if qtyFixed < 0 || unitPriceCents < 0 {
		return 0, ErrNegative
	}
	if qtyFixed != 0 && unitPriceCents > (maxInt64-QtyScale/2)/qtyFixed {
		return 0, ErrOverflow
	}
	// round half-up: (a + scale/2) / scale
	return (qtyFixed*unitPriceCents + QtyScale/2) / QtyScale, nil
}

// TotalDueCents is principal plus flat interest expressed in BASIS POINTS (1% = 100 bps), rounded half-up.
// Flat (not compounding) for v1: total = principal + round(principal * bps / 10000).
func TotalDueCents(principalCents, interestBps int64) (int64, error) {
	if principalCents < 0 || interestBps < 0 {
		return 0, ErrNegative
	}
	if principalCents != 0 && interestBps > (maxInt64-5000)/principalCents {
		return 0, ErrOverflow
	}
	interest := (principalCents*interestBps + 5000) / 10000 // round half-up
	if interest > maxInt64-principalCents {
		return 0, ErrOverflow
	}
	return principalCents + interest, nil
}

// Amortize splits totalCents into n installments that sum EXACTLY to totalCents.
// The first (totalCents % n) installments are one cent larger than the rest, so:
//   - sum(result) == totalCents (no drift),
//   - every installment is >= totalCents/n >= 1 (given n <= totalCents),
//   - the schedule is deterministic and front-loaded by exactly the remainder.
func Amortize(totalCents int64, n int) ([]int64, error) {
	if n < 1 {
		return nil, ErrBadInstallments
	}
	if n > MaxInstallments {
		return nil, ErrInstallmentCap
	}
	if totalCents < 0 {
		return nil, ErrNegative
	}
	if int64(n) > totalCents {
		return nil, ErrTooManyInstallments
	}
	base := totalCents / int64(n)
	rem := totalCents % int64(n)
	out := make([]int64, n)
	for i := 0; i < n; i++ {
		out[i] = base
		if int64(i) < rem {
			out[i]++
		}
	}
	return out, nil
}

// LargestInstallment returns the biggest single installment of an amortized total — the front-loaded one
// (base, +1 cent when there is a remainder). Used to bound a settlement against MaxBookableCents.
func LargestInstallment(totalCents int64, n int) int64 {
	if n < 1 || totalCents <= 0 {
		return 0
	}
	base := totalCents / int64(n)
	if totalCents%int64(n) != 0 {
		base++
	}
	return base
}

// ValidateBondTerms checks the preconditions for a schedulable bond: principal at/above the floor, a sane
// installment count, that the resulting total can pay >= 1 cent per installment, and that no installment
// exceeds the client's bookable ceiling. Returns the frozen total.
func ValidateBondTerms(principalCents, interestBps int64, installments int, minPrincipalCents int64) (totalDue int64, err error) {
	if principalCents < minPrincipalCents {
		return 0, ErrBelowMinPrincipal
	}
	// The principal must itself be bookable in a single settlement event: a manual loan transfers the whole
	// principal lender→borrower as ONE event at accept. Without this, a high-principal/many-installment loan
	// could satisfy the per-installment ceiling below yet emit an unbookable (>int32) principal transfer.
	if principalCents > MaxBookableCents {
		return 0, ErrAboveMaxBookable
	}
	if installments < 1 {
		return 0, ErrBadInstallments
	}
	if installments > MaxInstallments {
		return 0, ErrInstallmentCap
	}
	if interestBps < 0 || interestBps > MaxInterestBps {
		return 0, ErrInterestCap
	}
	total, err := TotalDueCents(principalCents, interestBps)
	if err != nil {
		return 0, err
	}
	if int64(installments) > total {
		return 0, ErrTooManyInstallments
	}
	if LargestInstallment(total, installments) > MaxBookableCents {
		return 0, ErrAboveMaxBookable
	}
	return total, nil
}

const maxInt64 = int64(^uint64(0) >> 1)
