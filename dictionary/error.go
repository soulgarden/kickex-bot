package dictionary

import "errors"

var ErrInvalidPair = errors.New("pair is missing in pairs list")

var ErrWsReadChannelClosed = errors.New("ws read channel closed")
