package swap

import "github.com/djkazic/tylium/pkg/types"

// Direction indicates the swap direction.
type Direction int

const (
	Buy  Direction = 1 // BTC → tyBTC (user pays Lightning, receives tyBTC)
	Sell Direction = 2 // tyBTC → BTC (user sends tyBTC, receives Lightning)
)

// State tracks swap lifecycle.
type State int

const (
	StateCreated         State = 0 // swap request received
	StateHTLCLocked      State = 1 // tyBTC HTLC locked on Tylium
	StateInvoiceCreated  State = 2 // LN hold invoice created (buy) or user invoice received (sell)
	StatePaymentInFlight State = 3 // LN payment in-flight
	StateClaimed         State = 4 // Tylium HTLC claimed (preimage revealed)
	StateSettled         State = 5 // both sides complete
	StateRefunded        State = 6 // timed out and refunded
	StateFailed          State = 7 // unrecoverable error
	StateRefundSubmitted State = 8 // refund tx submitted, waiting for finalization
)

func (s State) String() string {
	switch s {
	case StateCreated:
		return "created"
	case StateHTLCLocked:
		return "htlc_locked"
	case StateInvoiceCreated:
		return "invoice_created"
	case StatePaymentInFlight:
		return "payment_in_flight"
	case StateClaimed:
		return "claimed"
	case StateSettled:
		return "settled"
	case StateRefunded:
		return "refunded"
	case StateFailed:
		return "failed"
	case StateRefundSubmitted:
		return "refund_submitted"
	default:
		return "unknown"
	}
}

// Swap tracks the full state of an atomic swap.
type Swap struct {
	ID          string        `json:"id"`
	Direction   Direction     `json:"direction"`
	State       State         `json:"state"`
	Hash        [32]byte      `json:"hash"`
	Preimage    [32]byte      `json:"preimage,omitempty"`
	AmountTyBTC uint64        `json:"amountTyBTC"`
	AmountSats  int64         `json:"amountSats"`
	HTLCID      uint64        `json:"htlcID"`
	Timelock    uint64        `json:"timelock"`
	Invoice     string        `json:"invoice,omitempty"`
	Recipient   types.Address `json:"recipient,omitempty"` // tyBTC recipient (buy) or daemon address (sell)
	Error       string        `json:"error,omitempty"`
	Verified    bool          `json:"verified,omitempty"` // true once on-chain HTLC ID confirmed
}
