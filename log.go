package main

import (
	"context"
	"os"

	"github.com/rs/zerolog"
	"github.com/stesla/iris/internal/event"
	"github.com/stesla/iris/internal/telnet"
)

var logger = zerolog.New(os.Stdout)

type LogHandler struct {
	ctx context.Context
	zerolog.Logger
}

func (h LogHandler) Register(ctx context.Context) {
	h.ctx = ctx
	dispatcher := ctx.Value(telnet.KeyDispatcher).(event.Dispatcher)
	dispatcher.Listen(telnet.EventNegotation, h)
	dispatcher.Listen(telnet.EventOption, h)
	dispatcher.Listen(telnet.EventSubnegotiation, h)
	dispatcher.Listen(telnet.EventSend, h)
	dispatcher.Listen(telnet.EventCharsetAccepted, h)
	dispatcher.Listen(telnet.EventCharsetRejected, h)
}

func (h LogHandler) Unregister() {
	dispatcher := h.ctx.Value(telnet.KeyDispatcher).(event.Dispatcher)
	dispatcher.RemoveListener(telnet.EventNegotation, h)
	dispatcher.RemoveListener(telnet.EventOption, h)
	dispatcher.RemoveListener(telnet.EventSubnegotiation, h)
	dispatcher.RemoveListener(telnet.EventSend, h)
	dispatcher.RemoveListener(telnet.EventCharsetAccepted, h)
	dispatcher.RemoveListener(telnet.EventCharsetRejected, h)
}

func (h LogHandler) Listen(_ context.Context, ev event.Event) error {
	log := h.Trace().Str("event", string(ev.Name))
	switch t := ev.Data.(type) {
	case []byte:
		log.Bytes("data", t)
	case telnet.OptionData:
		log.Uint8("option", t.Option()).
			Bool("changedThem", t.ChangedThem).
			Bool("changedUs", t.ChangedUs).
			Bool("enabledThem", t.EnabledForThem()).
			Bool("enabledUs", t.EnabledForUs())
	case telnet.Subnegotiation:
		log.Uint8("option", t.Opt).Bytes("data", t.Data)
	default:
		log.Any("data", t)
	}
	log.Send()
	return nil
}
