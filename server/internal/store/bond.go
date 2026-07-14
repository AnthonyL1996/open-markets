package store

import (
	"time"

	"openmarkets/server/internal/money"
)

// ── Bond / credit (Phase 2a domain model) ────────────────────────────────────
//
// A Bond is a debt instrument: either a negotiated manual loan, or the automatic backstop minted when a trade
// installment can't be met ("always bond the shortfall"). It reuses the money package for its schedule. The
// state machine is the credit half of TRADE-SCREEN.md §10.1; the key invariant (Codex #1) is that once a bond
// reaches DefaultedReceivable its balance is FROZEN — interest no longer accrues — so the debt can only shrink,
// guaranteeing austerity is escapable. Bonds never recurse: a missed bond installment does not mint another bond.

// Bond statuses — the credit half of the canonical state machine.
const (
	BondOffered             = "offered"             // manual loan awaiting the lender/borrower's accept
	BondActive              = "active"              // funded; repaying on schedule
	BondDelinquent          = "delinquent"          // missed an installment; still accruing, can be cured
	BondDefaultedReceivable = "defaultedReceivable" // terminal default; INTEREST FROZEN; worked down by garnishment
	BondCompleted           = "completed"           // fully repaid
	BondCleared             = "cleared"             // defaulted debt fully recovered via garnishment
	BondWrittenOff          = "writtenOff"          // austerity timebox reached; remainder forgiven
	BondDeclined            = "declined"            // manual offer refused
	BondCancelled           = "cancelled"           // manual offer withdrawn before funding
)

// BondOriginManual marks a negotiated loan; auto-bonds use "trade:<tradeID>".
const BondOriginManual = "manual"

// Bond is a debt from DebtorID to CreditorID. TotalDueCents is frozen at activation (principal + flat interest)
// and is the amount actually repaid across Installments; it never grows once DefaultedReceivable.
type Bond struct {
	ID             string    `json:"id"`
	LeagueID       string    `json:"leagueId"`
	CreditorID     string    `json:"creditorId"`
	DebtorID       string    `json:"debtorId"`
	PrincipalCents int64     `json:"principalCents"`
	InterestBps    int64     `json:"interestBps"`
	Installments   int       `json:"installments"`
	Settled        int       `json:"settled"`     // installments the debtor has repaid
	MissedCount    int       `json:"missedCount"` // consecutive missed installments while delinquent
	TotalDueCents  int64     `json:"totalDueCents"`
	Status         string    `json:"status"`
	Origin         string    `json:"origin"`               // "manual" | "trade:<id>"
	ProposedBy     string    `json:"proposedBy,omitempty"` // manual-loan negotiation: who set the current terms (the OTHER party acts)
	Created        time.Time `json:"created"`
	// Austerity bookkeeping — set when the bond goes terminal (defaultedReceivable). DefaultedRemainingCents is
	// the frozen unpaid balance the debtor still owes; GarnishedCents is what austerity garnishment has since
	// recovered; GarnishTicks counts austerity sweeps applied (for the timebox write-off).
	DefaultedRemainingCents int64 `json:"defaultedRemainingCents,omitempty"`
	GarnishedCents          int64 `json:"garnishedCents,omitempty"`
	GarnishTicks            int   `json:"garnishTicks,omitempty"`
}

// OutstandingDefaultCents is the unpaid garnishable balance of a terminally-defaulted bond.
func (b Bond) OutstandingDefaultCents() int64 {
	rem := b.DefaultedRemainingCents - b.GarnishedCents
	if rem < 0 {
		return 0
	}
	return rem
}

// Activate freezes the schedule total (principal + flat interest) and validates the terms. Call when a manual
// bond is funded, or immediately for an auto-bond. minPrincipalCents guards against dust debts.
func (b *Bond) Activate(minPrincipalCents int64) error {
	total, err := money.ValidateBondTerms(b.PrincipalCents, b.InterestBps, b.Installments, minPrincipalCents)
	if err != nil {
		return err
	}
	b.TotalDueCents = total
	b.Status = BondActive
	b.Settled = 0
	b.MissedCount = 0
	return nil
}

// Schedule returns the per-installment cents (sums exactly to TotalDueCents). Freeze with Activate first.
func (b Bond) Schedule() ([]int64, error) {
	return money.Amortize(b.TotalDueCents, b.Installments)
}

// RemainingCents is the unpaid balance: TotalDueCents minus the sum of settled installments.
func (b Bond) RemainingCents() (int64, error) {
	sched, err := b.Schedule()
	if err != nil {
		return 0, err
	}
	var paid int64
	for i := 0; i < b.Settled && i < len(sched); i++ {
		paid += sched[i]
	}
	return b.TotalDueCents - paid, nil
}

// RegisterMiss records a missed installment. After maxMisses consecutive misses the bond goes terminal
// DefaultedReceivable and its balance is frozen (interest stops — Codex #1). Returns true if it just defaulted.
func (b *Bond) RegisterMiss(maxMisses int) (defaulted bool) {
	if b.Status == BondActive {
		b.Status = BondDelinquent
	}
	if b.Status != BondDelinquent {
		return false
	}
	b.MissedCount++
	if b.MissedCount >= maxMisses {
		b.Status = BondDefaultedReceivable // balance now frozen; no further interest
		if rem, err := b.RemainingCents(); err == nil {
			b.DefaultedRemainingCents = rem // freeze the garnishable balance at default
		}
		return true
	}
	return false
}

// ApplyGarnish records that `cents` of the defaulted balance was recovered via austerity garnishment, and
// clears the bond once the balance reaches zero. Returns the actual cents applied (capped at the outstanding
// balance, so the final installment never over-collects).
func (b *Bond) ApplyGarnish(cents int64) int64 {
	if b.Status != BondDefaultedReceivable || cents <= 0 {
		return 0
	}
	out := b.OutstandingDefaultCents()
	if cents > out {
		cents = out
	}
	b.GarnishedCents += cents
	b.GarnishTicks++
	if b.OutstandingDefaultCents() == 0 {
		b.Status = BondCleared
	}
	return cents
}

// ApplyBailout reduces a defaulted bond's outstanding balance by `cents` (capped at the balance) from a THIRD-PARTY
// payment, clearing the bond at zero. Like ApplyGarnish but it does NOT advance GarnishTicks: a voluntary bail-out
// isn't an austerity sweep and must not push the timebox write-off (which would forgive debt the bailer is paying).
// Returns the cents actually applied.
func (b *Bond) ApplyBailout(cents int64) int64 {
	if b.Status != BondDefaultedReceivable || cents <= 0 {
		return 0
	}
	out := b.OutstandingDefaultCents()
	if cents > out {
		cents = out
	}
	b.GarnishedCents += cents
	if b.OutstandingDefaultCents() == 0 {
		b.Status = BondCleared
	}
	return cents
}

// WriteOff forgives any remaining defaulted balance (austerity timebox reached) — terminal.
func (b *Bond) WriteOff() {
	if b.Status == BondDefaultedReceivable {
		b.Status = BondWrittenOff
	}
}

// Cure clears a delinquency back to active (the debtor caught up). No-op unless delinquent.
func (b *Bond) Cure() {
	if b.Status == BondDelinquent {
		b.Status = BondActive
		b.MissedCount = 0
	}
}

// NewAutoBond builds the active backstop bond for an unmet trade obligation (the always-bond rule). The debtor
// is the party who failed; principal is the unmet cents; the rate is the trade's negotiated default rate.
func NewAutoBond(id, leagueID, creditorID, debtorID, tradeID string, unmetCents, defaultRateBps int64,
	installments int, minPrincipalCents int64, now time.Time) (Bond, error) {
	b := Bond{
		ID:             id,
		LeagueID:       leagueID,
		CreditorID:     creditorID,
		DebtorID:       debtorID,
		PrincipalCents: unmetCents,
		InterestBps:    defaultRateBps,
		Installments:   installments,
		Origin:         "trade:" + tradeID,
		Created:        now,
	}
	if err := b.Activate(minPrincipalCents); err != nil {
		return Bond{}, err
	}
	return b, nil
}

// SettlementEvent is a server-authored, immutable money movement that BOTH clients book idempotently (Codex #3:
// conservation-safe settlement). Seq is monotonic per league; Ref ties it to its trade/bond installment.
type SettlementEvent struct {
	Seq        int64     `json:"seq"`
	LeagueID   string    `json:"leagueId"`
	PayerID    string    `json:"payerId"`
	ReceiverID string    `json:"receiverId"`
	Cents      int64     `json:"cents"`
	Ref        string    `json:"ref"` // "trade:<id>:<installment>" | "bond:<id>:<installment>"
	Created    time.Time `json:"created"`
}
