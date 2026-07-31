package main

import (
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/rs/zerolog"
	"github.com/spf13/viper"
)

var sessions = NewSessionPool()

func main() {
	viper.SetEnvPrefix("IRIS")
	viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))

	viper.SetDefault("addr", ":4001")
	viper.SetDefault("log.level", "info")
	viper.SetDefault("log.dir", "./logs")

	viper.AutomaticEnv()

	if config := viper.GetString("config"); config != "" {
		viper.SetConfigFile(config)
	} else {
		viper.SetConfigName("config")
		viper.AddConfigPath("$HOME/.config/iris")
		viper.AddConfigPath(".")
	}
	if err := viper.ReadInConfig(); err != nil {
		logger.Fatal().Err(err).Msg("error loading config")
	}

	if l, err := zerolog.ParseLevel(viper.GetString("log.level")); err != nil {
		logger.Fatal().Str("level", viper.GetString("log.level")).Msg("invalid level")
	} else {
		logger = logger.Level(l)
	}

	if viper.GetString("password") == "" {
		logger.Fatal().Msg("must set IRIS_PASSWORD")
	}

	signal.Ignore(os.Interrupt, syscall.SIGHUP, syscall.SIGTERM)

	chReopenSignal := make(chan os.Signal, 1)
	signal.Notify(chReopenSignal, syscall.SIGHUP)
	go func() {
		for range chReopenSignal {
			logger.Info().Msg("reopening histories")
			sessions.ReopenHistories()
		}
	}()

	chExit := make(chan struct{})
	chExitSignal := make(chan os.Signal, 1)
	signal.Notify(chExitSignal, os.Interrupt, syscall.SIGTERM)
	go func() {
		for sig := range chExitSignal {
			logger.Info().Str("signal", sig.String()).Msg("exiting")
			sessions.CloseAll()
			close(chExit)
		}
	}()

	l, err := net.Listen("tcp", viper.GetString("addr"))
	if err != nil {
		logger.Fatal().Err(err).Send()
	}
	defer l.Close()

	chAccept := make(chan net.Conn)
	go func() {
		for {
			if tcp, err := l.Accept(); err != nil {
				logger.Fatal().Err(err).Send()
			} else {
				chAccept <- tcp
			}
		}
	}()

	logger.Info().Str("addr", viper.GetString("addr")).Int("pid", os.Getpid()).Msg("listening")

loop:
	for {
		select {
		case <-chExit:
			break loop
		case tcp := <-chAccept:
			go func() {
				session := sessions.NewDownstream(tcp)
				defer session.Close()
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
