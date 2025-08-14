package telnet

import (
	"github.com/stesla/iris/internal/event"
	"golang.org/x/text/encoding"
)

const EventOption event.Name = "telnet.event.option"

type OptionData struct {
	OptionState
	ChangedThem bool
	ChangedUs   bool
}

const EventCharsetAccepted event.Name = "telnet.event.charset.accepted"

type CharsetData struct {
	encoding.Encoding
}

const EventCharsetRejected event.Name = "telnet.event.charset.rejected"

const eventEndOfRecord event.Name = "internal.end-of-record"
const eventGoAhead event.Name = "internal.go-ahead"
const eventSend event.Name = "internal.send-data"

const eventNegotation event.Name = "internal.option.negotiation"

type negotiation struct {
	cmd byte
	opt byte
}

const eventSubnegotiation event.Name = "internal.option.subnegotiation"

type subnegotiation struct {
	opt  byte
	data []byte
}
