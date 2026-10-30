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

	"github.com/rs/zerolog"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/stesla/iris/internal/auth"
	"github.com/stesla/iris/internal/database"
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
	cobra.CheckErr(setLogLevel(viper.GetString("log.level")))

	db, box, err := prepare(context.Background())
	cobra.CheckErr(err)

	sessions := NewSessionPool(db, box, logger)

	go runApiServer(db, sessions, box)
	runTelnetProxy(sessions)
}

// setLogLevel sets the level of logger, so call it before anything copies
// logger.
func setLogLevel(level string) error {
	l, err := zerolog.ParseLevel(level)
	if err != nil {
		return err
	}
	// ParseLevel accepts an empty string as NoLevel, which drops every line
	if l == zerolog.NoLevel {
		return fmt.Errorf("invalid log level %q", level)
	}
	logger = logger.Level(l)
	return nil
}

// prepare opens the database and loads the encryption key. It refuses to
// start without an Iris password.
func prepare(ctx context.Context) (*sql.DB, *secret.Box, error) {
	db, err := database.Open(ctx, viper.GetString("db"))
	if err != nil {
		return nil, nil, err
	}
	if set, err := auth.PasswordIsSet(ctx, db); err != nil {
		db.Close()
		return nil, nil, fmt.Errorf("checking iris password: %w", err)
	} else if !set {
		db.Close()
		return nil, nil, errors.New("no iris password is set; run `iris password` to set one")
	}
	box, err := secret.LoadOrCreateKey(viper.GetString("secret.key_file"))
	if err != nil {
		db.Close()
		return nil, nil, err
	}
	return db, box, nil
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
		// only the first signal matters, and a repeat must not close chExit twice
		sig := <-chExitSignal
		logger.Info().Str("signal", sig.String()).Msg("exiting")
		sessions.CloseAll()
		close(chExit)
	}()

	l, err := net.Listen("tcp", viper.GetString("addr"))
	if err != nil {
		logger.Fatal().Err(err).Send()
	}
	defer l.Close()

	chAccept := make(chan net.Conn)
	go func() {
		if err := acceptConns(l, chAccept); err != nil {
			logger.Fatal().Err(err).Send()
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

// acceptConns sends each connection l accepts to conns until l fails. It
// returns nil if that's because l was closed, which is how shutdown stops it.
func acceptConns(l net.Listener, conns chan<- net.Conn) error {
	for {
		tcp, err := l.Accept()
		if errors.Is(err, net.ErrClosed) {
			return nil
		} else if err != nil {
			return err
		}
		conns <- tcp
	}
}
