package telnet

import (
	"github.com/stesla/iris/internal/event"
	"golang.org/x/text/encoding"
)

const EventSend event.Name = "telnet.send-data"

const EventEndOfRecord event.Name = "telnet.end-of-record"
const EventGoAhead event.Name = "telnet.go-ahead"

const EventOption event.Name = "telnet.option"

type OptionData struct {
	OptionState
	ChangedThem bool
	ChangedUs   bool
}

const EventNegotation event.Name = "telnet.reader.negotiation"

type Negotiation struct {
	Opt byte
	Cmd byte
}

const EventSubnegotiation event.Name = "telnet.reader.subnegotiation"

type Subnegotiation struct {
	Opt  byte
	Data []byte
}

const EventCharsetAccepted event.Name = "telnet.charset.accepted"
const EventCharsetRejected event.Name = "telnet.charset.rejected"

type CharsetData struct {
	encoding.Encoding
}
