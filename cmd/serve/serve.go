package serve

import (
	"database/sql"
	"net"
	"os"
	"os/signal"
	"syscall"

	_ "github.com/mattn/go-sqlite3"
	"github.com/rs/zerolog"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var logger = zerolog.New(os.Stdout)

func AddToCommand(cmd *cobra.Command) {
	cmd.AddCommand(&cobra.Command{
		Use:   "serve",
		Short: "run the server",
		Run:   Serve,
	})
}

func Serve(cmd *cobra.Command, args []string) {
	db, err := sql.Open("sqlite3", viper.GetString("db"))
	cobra.CheckErr(err)

	if l, err := zerolog.ParseLevel(viper.GetString("log.level")); err != nil {
		cobra.CheckErr(err)
	} else {
		logger.Level(l)
	}

	sessions := NewSessionPool(db, logger)

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
