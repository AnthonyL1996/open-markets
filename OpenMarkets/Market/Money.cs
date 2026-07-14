using System;

namespace OpenMarkets.Market
{
    /// <summary>
    /// Canonical integer-cents money math for trades and bonds (GATE-A) — the net35 MIRROR of the Go
    /// <c>server/internal/money</c> package. The two MUST stay bit-identical: both are cross-checked against the
    /// shared vectors in <c>server/internal/money/testdata/vectors.json</c> (regenerate via
    /// <c>go test ./internal/money</c>). Resolves Codex finding #6 (rounding farming free/inflated debt):
    /// amounts are <see cref="long"/> CENTS; quantities are fixed-point integers scaled by <see cref="QtyScale"/>
    /// (no truncating <c>(long)qty</c> cast); a bond total is computed ONCE; installments sum EXACTLY.
    ///
    /// net35-safe: plain static methods, no records / tuples / LINQ-in-hot-path. Pure (no Unity, no threads), so
    /// it's callable from either the main or sim thread. Inputs are validated at the boundary; callers handle the
    /// thrown <see cref="ArgumentException"/> (amortization runs at bond creation/display, never per-tick).
    /// </summary>
    public static class Money
    {
        /// <summary>Fixed-point scale for quantities: 1.0 unit is stored as 1*QtyScale (milli-units), so
        /// sub-unit quantities survive without truncation. MUST equal the Go package's QtyScale.</summary>
        public const long QtyScale = 1000L;

        /// <summary>Caps a schedule length — matches the contract installment ceiling and bounds Amortize's
        /// array allocation so an absurd n can't attempt an impossible array (Codex review #3). MUST equal Go.</summary>
        public const int MaxInstallments = 120;

        /// <summary>Value of <paramref name="qtyFixed"/> (a quantity already scaled by <see cref="QtyScale"/>) at
        /// <paramref name="unitPriceCents"/> per WHOLE unit, rounded half-up. The frozen-at-accept per-line value
        /// (Codex #7). value = round(qtyFixed * unitPriceCents / QtyScale).</summary>
        public static long LineValueCents(long qtyFixed, long unitPriceCents)
        {
            if (qtyFixed < 0 || unitPriceCents < 0) throw new ArgumentException("money: negative amount");
            if (qtyFixed != 0 && unitPriceCents > (long.MaxValue - QtyScale / 2) / qtyFixed)
                throw new ArgumentException("money: arithmetic overflow");
            return (qtyFixed * unitPriceCents + QtyScale / 2) / QtyScale; // round half-up
        }

        /// <summary>Principal plus FLAT interest in basis points (1% = 100 bps), rounded half-up.
        /// total = principal + round(principal * bps / 10000). Flat (non-compounding) for v1.</summary>
        public static long TotalDueCents(long principalCents, long interestBps)
        {
            if (principalCents < 0 || interestBps < 0) throw new ArgumentException("money: negative amount");
            if (principalCents != 0 && interestBps > (long.MaxValue - 5000) / principalCents)
                throw new ArgumentException("money: arithmetic overflow");
            long interest = (principalCents * interestBps + 5000) / 10000; // round half-up
            if (interest > long.MaxValue - principalCents) throw new ArgumentException("money: arithmetic overflow");
            return principalCents + interest;
        }

        /// <summary>Split <paramref name="totalCents"/> into <paramref name="n"/> installments that sum EXACTLY to
        /// the total. The first (total % n) installments are one cent larger, so there is no rounding drift and
        /// every installment is &gt;= total/n &gt;= 1 (given n &lt;= total). Deterministic — matches the Go side.</summary>
        public static long[] Amortize(long totalCents, int n)
        {
            if (n < 1) throw new ArgumentException("money: installments must be >= 1");
            if (n > MaxInstallments) throw new ArgumentException("money: installments exceed the maximum schedule length");
            if (totalCents < 0) throw new ArgumentException("money: negative amount");
            if (n > totalCents) throw new ArgumentException("money: installments exceed total cents");
            long baseEach = totalCents / n;
            long rem = totalCents % n;
            long[] outv = new long[n];
            for (int i = 0; i < n; i++)
            {
                outv[i] = baseEach;
                if (i < rem) outv[i]++;
            }
            return outv;
        }

        /// <summary>Validate the preconditions for a schedulable bond and return the frozen total via
        /// <paramref name="totalDue"/>. Returns false (with a reason) instead of throwing, so UI/negotiation can
        /// surface it. Mirrors the Go <c>ValidateBondTerms</c>.</summary>
        public static bool TryValidateBondTerms(long principalCents, long interestBps, int installments,
            long minPrincipalCents, out long totalDue, out string error)
        {
            totalDue = 0;
            error = null;
            if (principalCents < minPrincipalCents) { error = "principal below minimum"; return false; }
            if (installments < 1) { error = "installments must be >= 1"; return false; }
            if (installments > MaxInstallments) { error = "installments exceed the maximum schedule length"; return false; }
            try { totalDue = TotalDueCents(principalCents, interestBps); }
            catch (ArgumentException e) { error = e.Message; return false; }
            if (installments > totalDue) { error = "installments exceed total cents"; return false; }
            return true;
        }
    }
}
