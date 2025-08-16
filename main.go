package main

import (
	"context"
	"flag"
	"net"
	"os"

	"github.com/rs/zerolog"
	"github.com/stesla/iris/internal/event"
	"github.com/stesla/iris/internal/telnet"
)

var (
	addr = flag.String("addr", getEnvDefault("IRIS_ADDR", ":4001"), "address on which to listen")
)

type contextKey string

const (
	KeyLogger contextKey = "logger"
)

var logger = zerolog.New(os.Stdout)

func main() {
	l, err := net.Listen("tcp", *addr)
	if err != nil {
		logger.Fatal().Err(err).Send()
	}
	defer l.Close()

	logger.Info().Str("addr", *addr).Msg("started")

	for {
		tcp, err := l.Accept()
		if err != nil {
			logger.Error().Err(err).Msg("error accepting connection")
		}
		ctx := context.Background()

		conn := telnet.Wrap(ctx, tcp)
		go func() {
			defer conn.Close()
			session := newDownstreamSession(conn)
			session.runForever()
		}()
	}
}

func getEnvDefault(name, defaultValue string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return defaultValue
}

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
