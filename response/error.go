package response

type Error struct {
	ID    string `json:"id"`
	Error *Err
}

type Err struct {
	Code   int    `json:"code"`
	Reason string `json:"reason"`
}

const CancelledOrder = "unable to cancel CANCELLED order"
const AmountTooSmall = "total volume must be >= 1"
const InsufficientFundsCode = 20005 // total volume must be >= 1
