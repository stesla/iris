package serve

import (
	"context"
	"database/sql"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"

	_ "github.com/mattn/go-sqlite3"
	"github.com/rs/zerolog"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"golang.org/x/crypto/bcrypt"
	"google.golang.org/grpc"
	emptypb "google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"

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

	sessions := NewSessionPool(db, logger)

	go runApiServer(db, sessions)
	runTelnetProxy(sessions)
}

func runApiServer(db *sql.DB, sessions *SessionPool) {
	l, err := net.Listen("tcp", viper.GetString("grpc.server.addr"))
	if err != nil {
		logger.Fatal().Err(err).Msg("error listening on grpc.server.addr")
	}
	s := grpc.NewServer()
	srv := &apiServer{
		db:       db,
		sessions: sessions,
	}
	api.RegisterSessionsServer(s, srv)
	api.RegisterUpstreamsServer(s, srv)
	if err := s.Serve(l); err != nil {
		logger.Fatal().Err(err).Msg("error serving grpc")
	}
}

type authrequest interface {
	GetName() string
	GetPassword() string
}

func authenticate(s *apiServer, r authrequest) error {
	var hash string
	row := s.db.QueryRow("SELECT bcrypt FROM upstreams WHERE name=?", r.GetName())
	if err := row.Scan(&hash); err != nil {
		return err
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(r.GetPassword()))
}

type apiServer struct {
	api.UnimplementedSessionsServer
	api.UnimplementedUpstreamsServer
	db       *sql.DB
	sessions *SessionPool
}

func (s *apiServer) AddUpstream(_ context.Context, r *api.AddUpstreamRequest) (*emptypb.Empty, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(*r.Password), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}

	_, err = s.db.Exec(
		"INSERT INTO upstreams (name, address, login, bcrypt, script) VALUES (?, ?, ?, ?, ?)",
		r.Upstream.Name, r.Upstream.Address, r.Upstream.Login, hash, r.Script,
	)

	return &emptypb.Empty{}, err
}

func (s *apiServer) DeleteUpstream(_ context.Context, r *api.DeleteUpstreamRequest) (*emptypb.Empty, error) {
	if err := authenticate(s, r); err != nil {
		return nil, err
	}

	_, err := s.db.Exec("DELETE FROM upstreams WHERE name=?", r.GetName())

	return &emptypb.Empty{}, err
}

func (s *apiServer) EditUpstream(_ context.Context, r *api.EditUpstreamRequest) (*emptypb.Empty, error) {
	if err := authenticate(s, r); err != nil {
		return nil, err
	}

	var sets []string
	var args []any
	set := func(column string, value any) {
		sets = append(sets, column+"=?")
		args = append(args, value)
	}

	if r.NewName != nil {
		set("name", r.GetNewName())
	}
	if r.NewPassword != nil {
		hash, err := bcrypt.GenerateFromPassword([]byte(r.GetNewPassword()), bcrypt.DefaultCost)
		if err != nil {
			return nil, err
		}
		set("bcrypt", hash)
	}
	if r.Address != nil {
		set("address", r.GetAddress())
	}
	if r.Login != nil {
		set("login", r.GetLogin())
	}
	if r.Script != nil {
		set("script", r.GetScript())
	}

	if len(sets) == 0 {
		return &emptypb.Empty{}, nil
	}

	query := "UPDATE upstreams SET " + strings.Join(sets, ", ") + " WHERE name=?"
	args = append(args, r.GetName())

	_, err := s.db.Exec(query, args...)
	return &emptypb.Empty{}, err
}

func (s *apiServer) ListSessions(context.Context, *emptypb.Empty) (*api.ListSessionsResponse, error) {
	s.sessions.RLock()
	defer s.sessions.RUnlock()
	result := &api.ListSessionsResponse{}
	for key, upstream := range s.sessions.streams {
		result.Sessions = append(result.Sessions, &api.Session{
			UpstreamName: &key,
			TimeStarted:  timestamppb.New(upstream.startedAt),
		})
	}
	return result, nil
}

func (s *apiServer) ListUpstreams(context.Context, *emptypb.Empty) (*api.ListUpstreamsResponse, error) {
	rows, err := s.db.Query("SELECT name, address, login FROM upstreams")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := &api.ListUpstreamsResponse{}
	for rows.Next() {
		upstream := &api.Upstream{}
		if err := rows.Scan(&upstream.Name, &upstream.Address, &upstream.Login); err != nil {
			return nil, err
		}
		result.Upstreams = append(result.Upstreams, upstream)
	}
	return result, rows.Err()
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
