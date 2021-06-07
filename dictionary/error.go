package dictionary

import "errors"

var ErrInvalidPair = errors.New("pair is missing in pairs list")

var ErrWsReadChannelClosed = errors.New("ws read channel closed")

var ErrParseFloat = errors.New("parse string as float")

var ErrResponse = errors.New("received response contains error")

var ErrCantCancelDoneOrder = errors.New("unable to cancel done order")

var ErrOrderNotFoundOrOutdated = errors.New("order not found or outdated")

var ErrCantConvertInterfaceToBytes = errors.New("can't convert interface to bytes")

var ErrInsufficientFunds = errors.New("insufficient funds")

var ErrUpdateOrderStateTimeout = errors.New("update order state timeout")

var ErrEventChannelClosed = errors.New("event channel closed")

var ErrOrderCreationEventNotReceived = errors.New("order creation event not received")
