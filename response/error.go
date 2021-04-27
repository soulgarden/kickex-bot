package response

type Error struct {
	ID    string `json:"id"`
	Error *Err
}

type Err struct {
	Code   int    `json:"code"`
	Reason string `json:"reason"`
}

const DoneOrder = "unable to cancel DONE order"
const CancelledOrder = "unable to cancel CANCELLED order"
const AmountTooSmallCode = 20006
const InsufficientFundsCode = 20005 // total volume must be >= 1
const OrderNotFoundOrOutdated = 20001
