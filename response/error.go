package response

type Error struct {
	ID    string `json:"id"`
	Error *Err
}

type Err struct {
	Code   int    `json:"code"`
	Reason string `json:"reason"`
}

const DoneOrderCode = 20007
const CancelledOrder = "unable to cancel CANCELLED order"
const AmountTooSmallCode = 20006
const OrderNotFoundOrOutdated = 20001
