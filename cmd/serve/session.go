package serve

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"github.com/spf13/viper"
	"github.com/stesla/iris/internal/event"
	"github.com/stesla/iris/internal/telnet"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/text/encoding/unicode"
)

const readBufSize = 4096

type SessionPool struct {
	sync.Mutex
	streams map[string]*upstream
	db      *sql.DB
	logger  zerolog.Logger
}

func NewSessionPool(db *sql.DB, logger zerolog.Logger) *SessionPool {
	return &SessionPool{
		streams: make(map[string]*upstream),
		db:      db,
		logger:  logger,
	}
}

func (p *SessionPool) CloseAll() {
	p.Lock()
	defer p.Unlock()
	for _, session := range p.streams {
		session.Close()
	}
}

func (p *SessionPool) NewDownstream(conn net.Conn) *downstream {
	result := &downstream{
		pool: p,
		telnetSession: newSession(conn, p.logger.With().
			Str("client", conn.RemoteAddr().String()).
			Logger()),
	}
	result.charset.IsServer = true
	return result
}

func (p *SessionPool) ReopenHistories() {
	p.Lock()
	defer p.Unlock()
	for _, session := range p.streams {
		if err := session.history.Reopen(); err != nil {
			session.Close()
			p.logger.Error().Err(err).Str("session-key", session.key).Msg("error reloading history")
		}
	}
}

func (p *SessionPool) upstreamForKey(key string) *upstream {
	p.Lock()
	defer p.Unlock()
	if _, found := p.streams[key]; !found {
		p.streams[key] = &upstream{
			pool:       p,
			key:        key,
			dispatcher: event.NewDispatcher(),
			logger:     p.logger,
		}
	}
	return p.streams[key]
}

func (p *SessionPool) deleteUpstreamWithKey(key string) {
	p.Lock()
	defer p.Unlock()
	delete(p.streams, key)
}

type telnetSession struct {
	conn           telnet.Conn
	logger         zerolog.Logger
	charset        telnet.CharsetHandler
	transmitBinary telnet.TransmitBinaryHandler
	dispatcher     event.Dispatcher
}

func newSession(conn net.Conn, logger zerolog.Logger) *telnetSession {
	s := &telnetSession{
		conn:       telnet.Wrap(context.Background(), conn),
		logger:     logger,
		dispatcher: event.NewDispatcher(),
	}
	s.conn.RegisterHandler(LogHandler{Logger: s.logger})
	s.conn.RegisterHandler(&s.transmitBinary)
	s.conn.RegisterHandler(&s.charset)
	s.conn.Listen(telnet.EventOption, s)
	s.conn.Listen(telnet.EventCharsetAccepted, s)
	s.conn.Listen(telnet.EventCharsetRejected, s)
	return s
}

func (s *telnetSession) Close() error {
	return s.conn.Close()
}

func (s *telnetSession) Context() context.Context {
	return s.conn.Context()
}

func (s *telnetSession) GetOption(opt byte) telnet.OptionState {
	return s.conn.GetOption(opt)
}

func (s *telnetSession) Read(p []byte) (n int, err error) {
	return s.conn.Read(p)
}

func (s *telnetSession) Write(p []byte) (n int, err error) {
	return s.conn.Write(p)
}

func (s *telnetSession) Listen(_ context.Context, ev event.Event) error {
	switch ev.Name {
	case telnet.EventOption:
		opt := ev.Data.(telnet.OptionData)
		switch opt.Option() {
		case telnet.Charset:
			if opt.ResolvedUs {
				if opt.EnabledForUs() {
					s.charset.RequestEncoding(unicode.UTF8)
				} else {
					s.dispatcher.Dispatch(context.Background(), event.Event{Name: EventCharsetResolved})
				}
			}
		}
	case telnet.EventCharsetAccepted:
		s.GetOption(telnet.TransmitBinary).Allow(true, true).EnableBoth(s.Context())
		fallthrough
	case telnet.EventCharsetRejected:
		s.dispatcher.Dispatch(context.Background(), event.Event{Name: EventCharsetResolved})
	}
	return nil
}

func (s *telnetSession) negotiateOptions() {
	opts := []byte{
		telnet.SuppressGoAhead,
		telnet.EndOfRecord,
		telnet.Charset,
	}
	for _, opt := range opts {
		s.GetOption(opt).Allow(true, true).EnableBoth(s.Context())
	}
}

type downstream struct {
	pool *SessionPool
	*telnetSession
	upstream *upstream

	Name     string            `json:"name"`
	Password string            `json:"password"`
	Options  map[string]string `json:"options"`
}

func (s *downstream) Listen(_ context.Context, ev event.Event) error {
	switch ev.Name {
	case EventCharsetResolved:
		go s.dispatcher.RemoveListener(EventCharsetResolved, s)
		_, err := s.upstream.history.WriteTo(s)
		if err != nil {
			s.logger.Error().AnErr("error", err).Msg("error writing history")
		}
	}
	return nil
}

func (s *downstream) connectNewUpstream() error {
	var address, login, hash string
	var script sql.NullString
	row := s.pool.db.QueryRow("SELECT address, login, bcrypt, script FROM upstreams WHERE name=?", s.Name)
	if err := row.Scan(&address, &login, &hash, &script); err != nil {
		return err
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(s.Password)); err != nil {
		return err
	}

	fmt.Fprintf(s, "connecting to %v...", address)
	if err := s.upstream.Connect(address); err != nil {
		return fmt.Errorf("error connecting (%v): %w", address, err)
	}

	var connectScript string
	if script.Valid {
		connectScript = script.String
	} else {
		connectScript = "connect %LOGIN% %PASSWORD%"
	}

	connectScript = strings.ReplaceAll(connectScript, "%LOGIN%", login)
	connectScript = strings.ReplaceAll(connectScript, "%PASSWORD%", s.Password)
	if _, err := s.upstream.Write([]byte(connectScript + "\n")); err != nil {
		return fmt.Errorf("error writing to (%v): %w", address, err)
	}

	return nil
}

func (s *downstream) connectUpstream() error {
	decoder := json.NewDecoder(s)
	if err := decoder.Decode(&s); err != nil {
		return err
	}
	s.upstream = s.pool.upstreamForKey(s.Name)
	s.upstream.AddDownstream(s)
	if s.upstream.IsConnected() {
		s.dispatcher.Listen(EventCharsetResolved, s)
	} else {
		for option, value := range s.Options {
			if err := s.upstream.setOption(option, value); err != nil {
				return err
			}
		}
		return s.connectNewUpstream()
	}
	return nil
}

const EventCharsetResolved = "charset.resolved"

func (s *downstream) runForever() {
	s.logger.Debug().Msg("connected")
	defer s.logger.Debug().Msg("disconnected")

	s.negotiateOptions()
	if err := s.connectUpstream(); err != nil {
		s.logger.Info().AnErr("error", err).Msg("error connecting upstream")
		return
	}
	io.Copy(s.upstream, s)
}

type upstream struct {
	pool *SessionPool
	*telnetSession
	key        string
	mux        sync.Mutex
	downstream []io.WriteCloser
	history    History
	dispatcher event.Dispatcher
	logger     zerolog.Logger
}

const EventConnectUpstream event.Name = "upstream.connect"

func (s *upstream) Connect(addr string) (err error) {
	if s == nil {
		return errors.New("you must select an upstream to connect")
	}
	s.history, err = newHistory(s.key)
	if err != nil {
		return
	}
	s.AddDownstream(s.history)

	tcp, err := net.Dial("tcp", addr)
	if err != nil {
		return err
	}
	s.telnetSession = newSession(tcp, s.logger.With().
		Str("server", tcp.RemoteAddr().String()).
		Logger())
	s.dispatcher.Dispatch(s.Context(), event.Event{
		Name: EventConnectUpstream,
		Data: s,
	})
	go s.runForever()
	return nil
}

func (s *upstream) AddDownstream(w io.WriteCloser) {
	s.mux.Lock()
	defer s.mux.Unlock()
	s.downstream = append(s.downstream, w)
}

func (s *upstream) Close() error {
	for _, wc := range s.downstream {
		wc.Close()
	}
	s.telnetSession.Close()
	return nil
}

func (s *upstream) IsConnected() bool {
	return s.telnetSession != nil
}

func (s *upstream) runForever() {
	defer func() {
		s.Close()
		s.pool.deleteUpstreamWithKey(s.key)
	}()
	s.logger.Debug().Msg("connected")
	s.negotiateOptions()
	for {
		var buf = make([]byte, readBufSize)
		n, err := s.Read(buf)
		if err != nil {
			break
		}
		buf = buf[:n]
		s.sendDownstream(buf)
	}
	s.logger.Debug().Msg("disconnected")
}

func (s *upstream) sendDownstream(buf []byte) {
	s.mux.Lock()
	defer s.mux.Unlock()
	s.downstream = slices.DeleteFunc(s.downstream, func(wc io.WriteCloser) bool {
		_, err := wc.Write(buf)
		return err != nil
	})
}

func (s *upstream) setOption(optionName, optionValue string) error {
	if s == nil {
		return errors.New("you must select an upstream to set options")
	}
	switch optionName {
	case "always_allow_charset":
		value, err := strconv.ParseBool(optionValue)
		if err != nil {
			return err
		}
		s.dispatcher.ListenFunc(EventConnectUpstream, func(context.Context, event.Event) error {
			s.telnetSession.charset.AllowWithoutTransmitBinary = value
			return nil
		})
	case "force_suppress_go_ahead":
		value, err := strconv.ParseBool(optionValue)
		if err != nil {
			return err
		}
		s.dispatcher.ListenFunc(EventConnectUpstream, func(context.Context, event.Event) error {
			s.telnetSession.conn.SuppressGoAhead(value)
			return nil
		})

	}
	return nil
}

type History interface {
	io.WriteCloser
	io.WriterTo
	Reopen() error
}

const defaultHistorySize = 20 * 1024 // about 256 lines of text
const logTimeFormat = "2006-01-02 15:04:05 -0700 MST"
const logSeperator = "--------------- %s - %s ---------------\n"
const logSepOpened = "--------------- opened"

func newHistory(key string) (History, error) {
	log := &logFile{key: key, historySize: defaultHistorySize}
	if err := log.Open(); err != nil {
		return nil, fmt.Errorf("error opening log for key (%v): %w", key, err)
	}
	return log, nil
}

type logFile struct {
	*os.File
	key         string
	historySize int64
}

func (f *logFile) Open() (err error) {
	logFileName := path.Join(
		viper.GetString("log.dir"),
		fmt.Sprintf("%s-%s.log", time.Now().Format("2006-01-02"), f.key),
	)
	f.File, err = os.OpenFile(
		logFileName,
		os.O_APPEND|os.O_CREATE|os.O_WRONLY,
		0644,
	)
	if err == nil {
		t := time.Now()
		fmt.Fprintf(f, logSeperator, "opened", t.Format(logTimeFormat))
	}
	return
}

func (f *logFile) Close() (err error) {
	t := time.Now()
	fmt.Fprintf(f, logSeperator, "closed", t.Format(logTimeFormat))
	return f.File.Close()
}

func (f *logFile) Reopen() (err error) {
	if err = f.Close(); err != nil {
		return
	}
	return f.Open()
}

func (f *logFile) WriteTo(w io.Writer) (int64, error) {
	if f.File == nil {
		return 0, errors.New("log file not open")
	}
	file, err := os.Open(f.Name())
	if err != nil {
		return 0, err
	}
	defer file.Close()
	end, err := file.Seek(0, io.SeekEnd)
	if err != nil {
		return 0, err
	}
	_, err = file.Seek(0, io.SeekStart)
	if err != nil {
		return 0, err
	}
	if end > f.historySize {
		_, err = file.Seek(end-f.historySize, io.SeekCurrent)
		if err != nil {
			return 0, err
		}
	}
	buf := make([]byte, f.historySize)
	n, err := file.Read(buf)
	if err != nil && err != io.EOF {
		return 0, err
	}
	buf = buf[:n]
	if n = bytes.LastIndex(buf, []byte(logSepOpened)); n > 0 {
		buf = buf[n:]
		n = bytes.IndexByte(buf, '\n')
		buf = buf[n+1:]
	}
	n, err = w.Write(buf)
	return int64(n), err
}
