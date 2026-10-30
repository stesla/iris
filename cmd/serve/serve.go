package serve

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/rs/zerolog"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/stesla/iris/internal/auth"
	"github.com/stesla/iris/internal/secret"
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

	if set, err := auth.PasswordIsSet(context.Background(), db); err != nil {
		cobra.CheckErr(fmt.Errorf("checking iris password (have the migrations been run?): %w", err))
	} else if !set {
		cobra.CheckErr(errors.New("no iris password is set; run `iris password` to set one"))
	}

	box, err := secret.LoadOrCreateKey(viper.GetString("secret.key_file"))
	cobra.CheckErr(err)

	sessions := NewSessionPool(db, box, logger)

	go runApiServer(db, sessions, box)
	runTelnetProxy(sessions)
}

func runApiServer(db *sql.DB, sessions *SessionPool, box *secret.Box) {
	srv := &apiServer{
		db:       db,
		sessions: sessions,
		box:      box,
	}
	addr := viper.GetString("api.server.addr")
	server := &http.Server{
		Addr:              addr,
		Handler:           srv.handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	logger.Info().Str("addr", addr).Msg("api listening")
	if err := server.ListenAndServe(); err != nil {
		logger.Fatal().Err(err).Msg("error serving api")
	}
}

func runTelnetProxy(sessions *SessionPool) {
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
