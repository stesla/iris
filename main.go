package main

import (
	"context"
	"flag"
	"net"
	"os"

	"github.com/stesla/iris/internal/telnet"
)

var (
	addr     = flag.String("addr", getEnvDefault("IRIS_ADDR", ":4001"), "address on which to listen")
	password = flag.String("password", os.Getenv("IRIS_PASSWORD"), "password for server access")
	logdir   = flag.String("logdir", getEnvDefault("IRIS_LOG_DIR", "./logs"), "logs get saved in this directory")
)

func main() {
	flag.Parse()

	if *password == "" {
		logger.Fatal().Msg("must provide -password or set ENVOY_PASSWORD")
	}

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
