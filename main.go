package main

import (
	"context"
	"flag"
	"io"
	"net"
	"os"

	"github.com/rs/zerolog"
	"github.com/stesla/iris/internal/event"
	"github.com/stesla/iris/internal/telnet"
	"golang.org/x/text/encoding/unicode"
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

		go func() {
			defer conn.Close()
			logger.Debug().Str("peer", conn.RemoteAddr().String()).Msg("connected")
			session := newSession(conn, logger)
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

type session struct {
	telnet.Conn

	logger zerolog.Logger

	charset        telnet.CharsetHandler
	transmitBinary telnet.TransmitBinaryHandler
}

func newSession(conn telnet.Conn, logger zerolog.Logger) *session {
	result := &session{
		Conn:    conn,
		logger:  logger,
		charset: telnet.CharsetHandler{IsServer: true},
	}
	conn.RegisterHandler(&result.transmitBinary)
	conn.RegisterHandler(&result.charset)
	conn.ListenFunc(telnet.EventOption, func(ctx context.Context, ev event.Event) error {
		switch opt := ev.Data.(type) {
		case telnet.OptionData:
			switch opt.Option() {
			case telnet.Charset:
				if opt.ChangedUs && opt.EnabledForUs() {
					result.charset.RequestEncoding(unicode.UTF8)
				}
			}
		}
		return nil
	})
	return result
}

func (s *session) runForever() {
	s.GetOption(telnet.SuppressGoAhead).Allow(true, true).EnableBoth(s.Context())
	s.GetOption(telnet.EndOfRecord).Allow(true, true).EnableBoth(s.Context())
	s.GetOption(telnet.TransmitBinary).Allow(true, true).EnableBoth(s.Context())
	s.GetOption(telnet.Charset).Allow(true, true).EnableBoth(s.Context())
	io.Copy(s, s)
}
