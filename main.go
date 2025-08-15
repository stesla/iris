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

func main() {
	logger := zerolog.New(os.Stdout)

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

		logEvent := func(_ context.Context, ev event.Event) error {
			log := logger.Trace().Str("event", string(ev.Name))
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
		conn.ListenFunc(telnet.EventNegotation, logEvent)
		conn.ListenFunc(telnet.EventOption, logEvent)
		conn.ListenFunc(telnet.EventSubnegotiation, logEvent)
		conn.ListenFunc(telnet.EventSend, logEvent)
		conn.ListenFunc(telnet.EventCharsetAccepted, logEvent)
		conn.ListenFunc(telnet.EventCharsetRejected, logEvent)

		go func() {
			defer conn.Close()
			logger.Debug().Str("peer", conn.RemoteAddr().String()).Msg("connected")
			session := newSession(conn)
			session.runForever()
			logger.Debug().Str("peer", conn.RemoteAddr().String()).Msg("disconnected")
		}()
	}
}

func getEnvDefault(name, defaultValue string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return defaultValue
}
