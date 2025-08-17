package main

import (
	"context"
	"flag"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/rs/zerolog"
	"github.com/stesla/iris/internal/telnet"
)

var (
	addr     = flag.String("addr", getEnvDefault("IRIS_ADDR", ":4001"), "address on which to listen")
	password = flag.String("password", os.Getenv("IRIS_PASSWORD"), "password for server access")
	level    = flag.String("level", getEnvDefault("IRIS_LOG_LEVEL", "info"), "log level for json logger")
	logdir   = flag.String("logdir", getEnvDefault("IRIS_LOG_DIR", "./logs"), "logs get saved in this directory")
)

func main() {
	flag.Parse()

	if l, err := zerolog.ParseLevel(*level); err != nil {
		logger.Fatal().Str("level", *level).Msg("invalid level")
	} else {
		logger = logger.Level(l)
	}

	if *password == "" {
		logger.Fatal().Msg("must provide -password or set ENVOY_PASSWORD")
	}

	signal.Ignore(os.Interrupt, syscall.SIGHUP, syscall.SIGTERM)

	chReopenSignal := make(chan os.Signal, 1)
	signal.Notify(chReopenSignal, syscall.SIGHUP)
	go func() {
		for range chReopenSignal {
			logger.Info().Msg("reopening histories")
			ReopenHistories()
		}
	}()

	chExit := make(chan struct{})
	chExitSignal := make(chan os.Signal, 1)
	signal.Notify(chExitSignal, os.Interrupt, syscall.SIGTERM)
	go func() {
		for sig := range chExitSignal {
			logger.Info().Str("signal", sig.String()).Msg("exiting")
			CloseAllSessions()
			close(chExit)
		}
	}()

	l, err := net.Listen("tcp", *addr)
	if err != nil {
		logger.Fatal().Err(err).Send()
	}
	defer l.Close()

	chAccept := make(chan net.Conn)
	go func() {
		for {
			if tcp, err := l.Accept(); err != nil {
				logger.Fatal().Err(err)
			} else {
				chAccept <- tcp
			}
		}
	}()

	logger.Info().Str("addr", *addr).Int("pid", os.Getpid()).Msg("listening")

loop:
	for {
		select {
		case <-chExit:
			break loop
		case tcp := <-chAccept:
			ctx := context.Background()
			conn := telnet.Wrap(ctx, tcp)
			go func() {
				defer conn.Close()
				session := newDownstreamSession(conn)
				session.runForever()
			}()
		}
	}
}

func getEnvDefault(name, defaultValue string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return defaultValue
}
