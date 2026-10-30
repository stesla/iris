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
	"github.com/stesla/iris/internal/auth"
	"github.com/stesla/iris/internal/event"
	"github.com/stesla/iris/internal/secret"
	"github.com/stesla/iris/internal/telnet"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/text/encoding/unicode"
)

const readBufSize = 4096

// maxLoginBytes limits how much a client can send before its login is read.
const maxLoginBytes = 64 << 10

// loginTimeout is how long a client has to send its login. It is a variable
// so tests can shorten it.
var loginTimeout = 30 * time.Second

// loginErrorTimeout limits how long Iris waits to tell a client why its login
// failed, in case the client isn't reading.
const loginErrorTimeout = 5 * time.Second

var (
	errLoginTooLong           = errors.New("login too long")
	errUpstreamPasswordNotSet = errors.New("upstream password not set")
	errUpstreamClosed         = errors.New("upstream was closed while connecting")
)

type SessionPool struct {
	sync.RWMutex
	streams map[int64]*upstream // by upstream id, which survives renames
	db      *sql.DB
	box     *secret.Box
	logger  zerolog.Logger
}

func NewSessionPool(db *sql.DB, box *secret.Box, logger zerolog.Logger) *SessionPool {
	return &SessionPool{
		streams: make(map[int64]*upstream),
		db:      db,
		box:     box,
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
		session.state.Lock()
		history, name := session.history, session.name
		session.state.Unlock()
		// upstreams that are still connecting have no history yet
		if history == nil {
			continue
		}
		// reopening also picks up a new name if the upstream was renamed
		if err := history.Reopen(name); err != nil {
			session.Close()
			p.logger.Error().Err(err).Str("upstream", name).Msg("error reloading history")
		}
	}
}

// upstreamFor returns the upstream with the given id, adding an unconnected
// one to the pool if there isn't one.
func (p *SessionPool) upstreamFor(id int64, name string) *upstream {
	p.Lock()
	defer p.Unlock()
	if _, found := p.streams[id]; !found {
		p.streams[id] = &upstream{
			pool:       p,
			id:         id,
			name:       name,
			dispatcher: event.NewDispatcher(),
			logger:     p.logger,
		}
	}
	return p.streams[id]
}

// renameUpstream updates the name of the live upstream with the given id, if
// there is one. Its log file follows the new name the next time histories are
// reopened.
func (p *SessionPool) renameUpstream(id int64, name string) {
	p.RLock()
	u, found := p.streams[id]
	p.RUnlock()
	if found {
		u.state.Lock()
		u.name = name
		u.state.Unlock()
	}
}

// removeUnconnected removes u from the pool if it is still there and never
// finished connecting, so a failed connection attempt doesn't leave it behind.
func (p *SessionPool) removeUnconnected(u *upstream) {
	p.Lock()
	defer p.Unlock()
	if p.streams[u.id] == u && !u.IsConnected() {
		delete(p.streams, u.id)
		u.Close()
	}
}

// removeUpstream removes u from the pool if it is still there, so it can't
// remove a newer upstream that has since taken its id.
func (p *SessionPool) removeUpstream(u *upstream) {
	p.Lock()
	defer p.Unlock()
	if p.streams[u.id] == u {
		delete(p.streams, u.id)
	}
}

// closeUpstream disconnects the upstream with the given id, along with every
// client attached to it.
func (p *SessionPool) closeUpstream(id int64) {
	p.Lock()
	u, found := p.streams[id]
	delete(p.streams, id)
	p.Unlock()
	if found {
		u.Close()
	}
}

type telnetSession struct {
	conn           telnet.Conn
	logger         zerolog.Logger
	charset        telnet.CharsetHandler
	transmitBinary telnet.TransmitBinaryHandler
	dispatcher     event.Dispatcher
	startedAt      time.Time
}

func newSession(conn net.Conn, logger zerolog.Logger) *telnetSession {
	s := &telnetSession{
		conn:       telnet.Wrap(context.Background(), conn),
		logger:     logger,
		dispatcher: event.NewDispatcher(),
		startedAt:  time.Now(),
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

type upstreamConfig struct {
	id       int64
	address  string
	login    string
	password []byte // sealed with the pool's box; NULL if not set
	script   sql.NullString
}

// loadUpstream returns sql.ErrNoRows if the upstream doesn't exist.
func loadUpstream(ctx context.Context, db *sql.DB, name string) (*upstreamConfig, error) {
	var config upstreamConfig
	row := db.QueryRowContext(ctx, "SELECT id, address, login, password, script FROM upstreams WHERE name=?", name)
	if err := row.Scan(&config.id, &config.address, &config.login, &config.password, &config.script); err != nil {
		return nil, err
	}
	return &config, nil
}

// openPassword decrypts the upstream's password.
func (c *upstreamConfig) openPassword(box *secret.Box) (string, error) {
	if len(c.password) == 0 {
		return "", errUpstreamPasswordNotSet
	}
	password, err := box.Open(c.id, c.password)
	if err != nil {
		return "", fmt.Errorf("decrypting upstream password: %w", err)
	}
	return string(password), nil
}

func (s *downstream) connectNewUpstream(config *upstreamConfig) error {
	for option, value := range s.Options {
		if err := s.upstream.setOption(option, value); err != nil {
			return err
		}
	}

	// decrypted before connecting, so a missing or unreadable password
	// doesn't leave a connection to the game that never logs in
	password, err := config.openPassword(s.pool.box)
	if err != nil {
		return err
	}

	address := config.address
	fmt.Fprintf(s, "connecting to %v...", address)
	if err := s.upstream.Connect(address); err != nil {
		return fmt.Errorf("error connecting (%v): %w", address, err)
	}

	var connectScript string
	if config.script.Valid {
		connectScript = config.script.String
	} else {
		connectScript = "connect %LOGIN% %PASSWORD%"
	}

	connectScript = strings.ReplaceAll(connectScript, "%LOGIN%", config.login)
	connectScript = strings.ReplaceAll(connectScript, "%PASSWORD%", password)
	if _, err := s.upstream.Write([]byte(connectScript + "\n")); err != nil {
		return fmt.Errorf("error writing to (%v): %w", address, err)
	}

	return nil
}

func (s *downstream) connectUpstream() (err error) {
	// deferred functions run last first, so the client is told why its
	// login failed before removing the upstream closes the client
	defer func() {
		if err != nil && s.upstream != nil {
			s.pool.removeUnconnected(s.upstream)
		}
	}()
	defer func() {
		if err != nil {
			s.writeLoginError(err)
		}
	}()

	if err := s.readLogin(); err != nil {
		return err
	}

	err = auth.CheckPassword(s.Context(), s.pool.db, s.Password)
	s.Password = ""
	if err != nil {
		return err
	}

	config, err := loadUpstream(s.Context(), s.pool.db, s.Name)
	if err != nil {
		return err
	}

	s.upstream = s.pool.upstreamFor(config.id, s.Name)
	s.upstream.AddDownstream(s)
	if s.upstream.IsConnected() {
		s.dispatcher.Listen(EventCharsetResolved, s)
		return nil
	}

	return s.connectNewUpstream(config)
}

// readLogin reads the client's JSON login, which must arrive within
// loginTimeout and maxLoginBytes.
func (s *downstream) readLogin() error {
	if err := s.conn.SetReadDeadline(time.Now().Add(loginTimeout)); err != nil {
		return err
	}
	decoder := json.NewDecoder(&limitReader{r: s, n: maxLoginBytes})
	if err := decoder.Decode(s); err != nil {
		return err
	}
	return s.conn.SetReadDeadline(time.Time{})
}

// writeLoginError tells the client why its login failed. The line ends in
// "\n" alone, which the Telnet writer sends as CR LF.
func (s *downstream) writeLoginError(err error) {
	s.conn.SetWriteDeadline(time.Now().Add(loginErrorTimeout))
	fmt.Fprintf(s, "login failed: %s\n", loginErrorMessage(err))
}

func loginErrorMessage(err error) string {
	var syntaxErr *json.SyntaxError
	var typeErr *json.UnmarshalTypeError
	switch {
	case errors.Is(err, os.ErrDeadlineExceeded):
		return "timed out waiting for login"
	case errors.Is(err, errLoginTooLong):
		return "login too long"
	case errors.As(err, &syntaxErr), errors.As(err, &typeErr):
		return "invalid login: " + err.Error()
	case errors.Is(err, bcrypt.ErrMismatchedHashAndPassword):
		return "incorrect password"
	case errors.Is(err, sql.ErrNoRows):
		return "upstream not found"
	case errors.Is(err, secret.ErrDecrypt), errors.Is(err, secret.ErrUnsupportedVersion):
		return "unable to decrypt upstream password"
	case errors.Is(err, errUpstreamClosed):
		return errUpstreamClosed.Error()
	default:
		return err.Error()
	}
}

// limitReader is like io.LimitedReader, but fails with errLoginTooLong rather
// than io.EOF, so a long login isn't mistaken for a disconnect.
type limitReader struct {
	r io.Reader
	n int64
}

func (l *limitReader) Read(p []byte) (int, error) {
	if l.n <= 0 {
		return 0, errLoginTooLong
	}
	if int64(len(p)) > l.n {
		p = p[:l.n]
	}
	n, err := l.r.Read(p)
	l.n -= int64(n)
	return n, err
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
	id int64

	mux        sync.Mutex // guards downstream
	downstream []io.WriteCloser

	// state guards the fields below, and setting telnetSession. It is
	// separate from mux, which is held while writing to clients, so a client
	// that isn't reading can't keep the upstream from closing.
	state   sync.Mutex
	name    string
	closed  bool
	history History

	dispatcher event.Dispatcher
	logger     zerolog.Logger
}

const EventConnectUpstream event.Name = "upstream.connect"

// Connect connects to the game at addr. It fails with errUpstreamClosed if
// the upstream is closed first, such as by being deleted while connecting.
func (s *upstream) Connect(addr string) error {
	s.state.Lock()
	name := s.name
	s.state.Unlock()

	tcp, err := net.Dial("tcp", addr)
	if err != nil {
		return err
	}
	history, err := newHistory(name)
	if err != nil {
		tcp.Close()
		return err
	}

	s.state.Lock()
	if s.closed {
		s.state.Unlock()
		tcp.Close()
		history.Close()
		return errUpstreamClosed
	}
	s.history = history
	s.telnetSession = newSession(tcp, s.logger.With().
		Str("server", tcp.RemoteAddr().String()).
		Logger())
	s.state.Unlock()

	s.AddDownstream(history)
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
	s.state.Lock()
	s.closed = true
	session := s.telnetSession
	s.state.Unlock()

	for _, wc := range s.downstream {
		wc.Close()
	}
	if session != nil {
		session.Close()
	}
	return nil
}

func (s *upstream) IsConnected() bool {
	s.state.Lock()
	defer s.state.Unlock()
	return s.telnetSession != nil
}

// info returns what GET /sessions reports about the upstream.
func (s *upstream) info() (name string, startedAt time.Time, connected bool) {
	s.state.Lock()
	defer s.state.Unlock()
	if s.telnetSession == nil {
		return s.name, time.Time{}, false
	}
	return s.name, s.startedAt, true
}

func (s *upstream) runForever() {
	defer func() {
		s.Close()
		s.pool.removeUpstream(s)
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
	// Reopen closes the log and opens it again under name, which changes if
	// the upstream was renamed.
	Reopen(name string) error
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

func (f *logFile) Reopen(name string) (err error) {
	if err = f.Close(); err != nil {
		return
	}
	f.key = name
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
