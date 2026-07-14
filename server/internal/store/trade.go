package store

import (
	"errors"
	"time"

	"openmarkets/server/internal/money"
)

// ── Trade basket (Phase 2a domain model) ─────────────────────────────────────
//
// A Trade is the Civ5-style two-sided basket: the OfferedBy party proposes a set of line items flowing in both
// directions, and the Counterparty accepts/declines the WHOLE bundle atomically. It is cash-settled (no cargo):
// per-line values are frozen at accept (mark-to-accept, Codex #7) and each line is a directional cash transfer
// per §9.4a. v1 is financial-only (no goods non-delivery). Settlement (gross-evaluate/net-book) and the
// auto-bond-on-shortfall path are layered on top of these types in a later increment.

// Line kinds and directions (relative to OfferedBy).
const (
	LineCommodity = "commodity"
	LineGold      = "gold"

	DirGive = "give" // flows away from the offerer
	DirTake = "take" // flows to the offerer
)

// Trade statuses — the basket half of the canonical state machine (TRADE-SCREEN.md §10.1).
const (
	TradeOffered   = "offered"
	TradeActive    = "active"
	TradeCompleted = "completed"
	TradeDeclined  = "declined"
	TradeCancelled = "cancelled"
)

var (
	// ErrBadLine: a structurally invalid line item.
	ErrBadLine = errors.New("trade: invalid line item")
	// ErrNoPrice: a commodity line has no price available to freeze its value.
	ErrNoPrice = errors.New("trade: no price for commodity")
	// ErrEmptyTrade: a trade must have at least one line on each side.
	ErrEmptyTrade = errors.New("trade: must give and take at least one line")
	// ErrLineTooLarge: a single line's value exceeds the bookable ceiling. Capping each line keeps the netted
	// sum (<= MaxTradeItems lines) far below int64 max, so OffererNetCents can't overflow.
	ErrLineTooLarge = errors.New("trade: line value exceeds the bookable maximum")
)

// LineItem is one directional component of a basket. For a commodity line, value is QtyFixed (scaled by
// money.QtyScale) × UnitPriceCents/QtyScale, frozen into ValueCentsAtAccept. For a gold line the value is
// GoldCents directly. Append-only JSON (array-shaped; JsonUtility-friendly on the client).
type LineItem struct {
	Kind               string `json:"kind"`                     // commodity | gold
	Commodity          string `json:"commodity,omitempty"`      // commodity lines
	QtyFixed           int64  `json:"qtyFixed,omitempty"`       // commodity lines, scaled by money.QtyScale
	UnitPriceCents     int64  `json:"unitPriceCents,omitempty"` // frozen market price per WHOLE unit (commodity)
	GoldCents          int64  `json:"goldCents,omitempty"`      // gold lines
	Dir                string `json:"dir"`                      // give | take (relative to OfferedBy)
	ValueCentsAtAccept int64  `json:"valueCentsAtAccept"`       // frozen at accept; 0 until then
}

// Trade is a two-sided basket between two league members.
type Trade struct {
	ID             string     `json:"id"`
	LeagueID       string     `json:"leagueId"`
	OfferedBy      string     `json:"offeredBy"`
	Counterparty   string     `json:"counterparty"`
	Items          []LineItem `json:"items"`
	DefaultRateBps int64      `json:"defaultRateBps"` // governs auto-bonds; >= league floor (enforced at create)
	Installments   int        `json:"installments"`
	Status         string     `json:"status"`
	Settled        int        `json:"settled"` // net installments settled (one net transfer per installment)
	Created        time.Time  `json:"created"`
	AcceptedDay    int64      `json:"acceptedDay,omitempty"` // due-clock anchor, set at accept
	// Installments for which a give-side DELIVERY shortfall has already been reported (M6) — dedupes the
	// client's shortfall reports so a goods-debt bond is minted at most once per installment. Append-only.
	ShortfallInstallments []int `json:"shortfallInstallments,omitempty"`
}

// CommodityGiveValueCents sums the frozen value of the commodity goods `caller` committed to GIVE across the
// whole trade (the cap for a delivery-shortfall report — you can't owe more goods-value than you promised).
// Dir is relative to OfferedBy: the offerer gives on DirGive lines, the counterparty gives on DirTake lines.
// Gold lines are excluded (gold settles as cash, never a physical delivery). Call after FreezeValues.
func (t Trade) CommodityGiveValueCents(caller string) int64 {
	giveDir := DirGive
	if caller == t.Counterparty {
		giveDir = DirTake
	}
	var sum int64
	for _, li := range t.Items {
		if li.Kind == LineCommodity && li.Dir == giveDir {
			sum += li.ValueCentsAtAccept
		}
	}
	return sum
}

// NetPayerReceiver returns who pays whom across the whole (cash-netted) trade. Because every line nets to a
// single signed cash flow, each installment is one transfer: if the offerer nets positive the counterparty pays
// the offerer, else the offerer pays. Freeze first.
func (t Trade) NetPayerReceiver() (payer, receiver string) {
	if t.OffererNetCents() >= 0 {
		return t.Counterparty, t.OfferedBy
	}
	return t.OfferedBy, t.Counterparty
}

// InstallmentSchedule splits the absolute net value into per-installment cents (sums exactly). A perfectly
// balanced trade (net 0) yields all-zero installments (nothing to book — just the goods swap). Freeze first.
func (t Trade) InstallmentSchedule() ([]int64, error) {
	mag := t.OffererNetCents()
	if mag < 0 {
		mag = -mag
	}
	if mag == 0 {
		return make([]int64, t.Installments), nil
	}
	return money.Amortize(mag, t.Installments)
}

// PrepareActiveErr validates, after value-freeze, that the net schedule is bookable (a nonzero net can't be
// split into more installments than it has cents). Returns ErrConflict if not. Exported so an alternate Store
// backend (e.g. Postgres) runs the IDENTICAL accept-time bookability check as Memory.
func (t Trade) PrepareActiveErr() error {
	mag := t.OffererNetCents()
	if mag < 0 {
		mag = -mag
	}
	if mag != 0 && int64(t.Installments) > mag {
		return ErrConflict
	}
	// No single net installment may exceed what the client can book into the int-cents treasury (Codex M5).
	if money.LargestInstallment(mag, t.Installments) > money.MaxBookableCents {
		return ErrConflict
	}
	return nil
}

// validLine reports whether a line is structurally sound (before value freeze).
func (li LineItem) validLine() bool {
	if li.Dir != DirGive && li.Dir != DirTake {
		return false
	}
	switch li.Kind {
	case LineCommodity:
		return li.Commodity != "" && li.QtyFixed > 0
	case LineGold:
		// Gold value is fixed at offer time (no price freeze), so cap it here: a single gold line above the
		// bookable ceiling is rejected at creation, and the cap bounds the netted sum against int64 overflow.
		return li.GoldCents > 0 && li.GoldCents <= money.MaxBookableCents
	default:
		return false
	}
}

// CashFlowToOfferer returns the signed cents this line moves toward the offerer at full value (§9.4a):
// commodity give = +value (sells), commodity take = −value (buys); gold give = −value (pays), gold take = +value.
// Uses ValueCentsAtAccept, so call after FreezeValues.
func (li LineItem) CashFlowToOfferer() int64 {
	v := li.ValueCentsAtAccept
	switch li.Kind {
	case LineCommodity:
		if li.Dir == DirGive {
			return v
		}
		return -v
	case LineGold:
		if li.Dir == DirGive {
			return -v
		}
		return v
	default:
		return 0
	}
}

// FreezeValues sets ValueCentsAtAccept on every line. For commodity lines it looks up the frozen market price
// per whole unit via price(commodity) and computes the line value with money.LineValueCents; gold lines take
// GoldCents directly. Returns ErrNoPrice if a commodity has no price, ErrBadLine for a malformed line.
func (t *Trade) FreezeValues(price func(commodity string) (unitPriceCents int64, ok bool)) error {
	for i := range t.Items {
		li := &t.Items[i]
		if !li.validLine() {
			return ErrBadLine
		}
		switch li.Kind {
		case LineCommodity:
			unit, ok := price(li.Commodity)
			if !ok || unit <= 0 {
				return ErrNoPrice
			}
			val, err := money.LineValueCents(li.QtyFixed, unit)
			if err != nil {
				return err
			}
			li.UnitPriceCents = unit
			li.ValueCentsAtAccept = val
		case LineGold:
			li.ValueCentsAtAccept = li.GoldCents
		}
		// Cap the frozen per-line value. Gold is already bounded by validLine; a commodity value (qty × the
		// accept-time market price) is only known here, so the ceiling is enforced at freeze. This keeps
		// OffererNetCents (a sum of <= MaxTradeItems such values) safely within int64.
		if li.ValueCentsAtAccept > money.MaxBookableCents {
			return ErrLineTooLarge
		}
	}
	return nil
}

// OffererNetCents sums CashFlowToOfferer across all lines (+ = offerer nets positive). Freeze first.
func (t Trade) OffererNetCents() int64 {
	var sum int64
	for _, li := range t.Items {
		sum += li.CashFlowToOfferer()
	}
	return sum
}

// hasBothSides reports whether the basket has at least one give and one take line — a real two-sided trade.
func (t Trade) hasBothSides() bool {
	var give, take bool
	for _, li := range t.Items {
		if li.Dir == DirGive {
			give = true
		} else if li.Dir == DirTake {
			take = true
		}
	}
	return give && take
}

// ValidateForOffer checks structural validity at creation time (before persistence): non-empty, every line
// sound, both sides present, sane installments, and a default rate at/above the league floor.
// MaxTradeItems bounds a basket: enough for a rich Civ5-style deal, but small enough that the net-value sum
// can't overflow int64 (20 × MaxBookableCents ≈ 4.3e10 ≪ 9.2e18) and a request can't amplify memory/snapshot.
const MaxTradeItems = 20

func (t Trade) ValidateForOffer(minDefaultRateBps int64) error {
	if len(t.Items) == 0 || len(t.Items) > MaxTradeItems || !t.hasBothSides() {
		return ErrEmptyTrade
	}
	for _, li := range t.Items {
		if !li.validLine() {
			return ErrBadLine
		}
	}
	if t.Installments < 1 || t.Installments > money.MaxInstallments {
		return ErrConflict
	}
	if t.DefaultRateBps < minDefaultRateBps {
		return ErrConflict
	}
	return nil
}

// NextTradeStatus validates an accept/decline/cancel transition and returns the resulting status.
// Mirrors the contract rules: only the counterparty may accept/decline an offered trade; only the offerer may
// cancel an offered or active trade. Anything else → ErrConflict.
func (t Trade) NextTradeStatus(accountID, action string) (string, error) {
	switch action {
	case "accept", "decline":
		if t.Status != TradeOffered || accountID != t.Counterparty {
			return "", ErrConflict
		}
		if action == "accept" {
			return TradeActive, nil
		}
		return TradeDeclined, nil
	case "cancel":
		if accountID != t.OfferedBy || (t.Status != TradeOffered && t.Status != TradeActive) {
			return "", ErrConflict
		}
		return TradeCancelled, nil
	default:
		return "", ErrConflict
	}
}
