package main

import (
	"context"
	"flag"
	"net"
	"os"

	"github.com/stesla/iris/internal/telnet"
)

var (
	addr = flag.String("addr", getEnvDefault("IRIS_ADDR", ":4001"), "address on which to listen")
)

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
