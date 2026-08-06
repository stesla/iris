package serve

import (
	"context"
	"database/sql"
	"net"
	"os"
	"os/signal"
	"syscall"

	_ "github.com/mattn/go-sqlite3"
	"github.com/rs/zerolog"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"golang.org/x/crypto/bcrypt"
	"google.golang.org/grpc"
	emptypb "google.golang.org/protobuf/types/known/emptypb"

	"github.com/stesla/iris/api"
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

	go runApiServer(db)
	runTelnetProxy(db)
}

func runApiServer(db *sql.DB) {
	l, err := net.Listen("tcp", viper.GetString("grpc.server.addr"))
	if err != nil {
		logger.Fatal().Err(err).Msg("error listening on grpc.server.addr")
	}
	s := grpc.NewServer()
	api.RegisterUpstreamsServer(s, &apiServer{db: db})
	if err := s.Serve(l); err != nil {
		logger.Fatal().Err(err).Msg("error serving grpc")
	}
}

type apiServer struct {
	api.UnimplementedUpstreamsServer
	db *sql.DB
}

func (s *apiServer) AddUpstream(_ context.Context, r *api.AddUpstreamRequest) (*emptypb.Empty, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(*r.Password), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}

	_, err = s.db.Exec(
		"INSERT INTO upstreams (name, address, bcrypt, script) VALUES (?, ?, ?, ?)",
		r.Name, r.Address, hash, r.Script,
	)

	return &emptypb.Empty{}, err
}

func (s *apiServer) ListUpstreams(context.Context, *emptypb.Empty) (*api.ListUpstreamResponse, error) {
	rows, err := s.db.Query("SELECT name FROM upstreams")
	if err != nil {
		return nil, err
	}
	result := &api.ListUpstreamResponse{}
	var name string
	for rows.Next() {
		rows.Scan(&name)
		result.Upstreams = append(result.Upstreams, name)
	}
	return result, rows.Err()
}

func runTelnetProxy(db *sql.DB) {
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
