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

// dial connects to games. It is a variable so tests can control it.
var dial = (&net.Dialer{}).DialContext

// dialTimeout limits how long connecting to a game can take. It is a
// variable so tests can shorten it.
var dialTimeout = 30 * time.Second

// loginErrorTimeout limits how long Iris waits to tell a client why its login
// failed, in case the client isn't reading.
const loginErrorTimeout = 5 * time.Second

// negotiationTimeout is how long a joining client's replay waits for Telnet
// negotiation to finish. It is a variable so tests can shorten it.
var negotiationTimeout = 5 * time.Second

var (
	errLoginTooLong           = errors.New("login too long")
	errUpstreamPasswordNotSet = errors.New("upstream password not set")
	errUpstreamClosed         = errors.New("upstream was closed while connecting")
	errUpstreamNotConnected   = errors.New("upstream failed to connect")
)

type SessionPool struct {
	sync.RWMutex
	streams map[int64]*upstream // by upstream id, which survives renames
	db      *sql.DB
	box     *secret.Box
	logger  zerolog.Logger

	// syncMu makes syncUpstream calls run one at a time, so that one which read
	// an older name can't overwrite a newer one. It isn't the pool lock, which
	// is held on paths that shouldn't wait on a database query.
	syncMu sync.Mutex
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

// syncUpstream updates the live upstream with the given id, if there is one, to
// the name in its database row. Its log file follows the new name the next time
// histories are reopened. If the row is gone it closes the upstream and returns
// sql.ErrNoRows.
//
// Call it after committing a change to the row. Every commit is followed by a
// sync and syncs run one at a time, so the last one sees the latest commit.
func (p *SessionPool) syncUpstream(ctx context.Context, id int64) error {
	p.syncMu.Lock()
	defer p.syncMu.Unlock()

	var name string
	err := p.db.QueryRowContext(ctx, "SELECT name FROM upstreams WHERE id=?", id).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		p.closeUpstream(id)
	}
	if err != nil {
		return err
	}

	p.RLock()
	u, found := p.streams[id]
	p.RUnlock()
	if found {
		u.state.Lock()
		u.name = name
		u.state.Unlock()
	}
	return nil
}

// removeUnconnected removes u from the pool if it is still there and isn't
// connected, so a failed connection attempt doesn't leave it behind.
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
	startedAt      time.Time

	charsetAccepted bool // only used on the goroutine reading from conn

	// negotiated is closed once the encoding is settled: charset was refused, or
	// it was accepted and Transmit Binary has resolved in both directions.
	negotiated     chan struct{}
	negotiatedOnce sync.Once
}

func newSession(conn net.Conn, logger zerolog.Logger) *telnetSession {
	s := &telnetSession{
		conn:       telnet.Wrap(context.Background(), conn),
		logger:     logger,
		startedAt:  time.Now(),
		negotiated: make(chan struct{}),
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
					s.endNegotiation()
				}
			}
		case telnet.TransmitBinary:
			// the handlers registered first, so the encoding is already set
			if s.charsetAccepted && !opt.Waiting() {
				s.endNegotiation()
			}
		}
	case telnet.EventCharsetAccepted:
		s.charsetAccepted = true
		// if it is already enabled there won't be an option event to wait for
		if !s.GetOption(telnet.TransmitBinary).Allow(true, true).EnableBoth(s.Context()).Waiting() {
			s.endNegotiation()
		}
	case telnet.EventCharsetRejected:
		s.endNegotiation()
	}
	return nil
}

func (s *telnetSession) endNegotiation() {
	s.negotiatedOnce.Do(func() { close(s.negotiated) })
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
	upstream   *upstream
	afterLogin []byte // input read along with the login, to forward once connected

	// replayMu guards the fields below. While pending, Write adds to the buffer
	// instead of writing to the client. Close doesn't take it, so closing still
	// unblocks a flush stuck on the client.
	replayMu      sync.Mutex
	replayPending bool
	replayBuf     bytes.Buffer
}

// Write writes to the client, or holds p back while a replay is pending.
func (s *downstream) Write(p []byte) (int, error) {
	s.replayMu.Lock()
	defer s.replayMu.Unlock()
	if s.replayPending {
		return s.replayBuf.Write(p)
	}
	return s.telnetSession.Write(p)
}

// joinUpstream attaches the client to a connected upstream and replays its
// history once the client's encoding is settled. Until then the history and any
// game output wait in the buffer, because the Telnet default of 7-bit ASCII would
// corrupt non-ASCII text.
func (s *downstream) joinUpstream(u *upstream) error {
	// held so that game output can't reach the client before the history
	s.replayMu.Lock()
	defer s.replayMu.Unlock()
	if err := u.AddDownstream(s); err != nil {
		return err
	}
	u.state.Lock()
	history := u.history
	u.state.Unlock()
	if history == nil {
		return nil
	}
	s.replayPending = true
	if _, err := history.WriteTo(&s.replayBuf); err != nil {
		s.logger.Error().AnErr("error", err).Msg("error reading history")
	}
	// this can't wait here, because the answers only arrive once the client's
	// connection is being read again
	go s.flushReplay(negotiationTimeout)
	return nil
}

// flushReplay writes out the buffer once negotiation finishes. After timeout it
// does so anyway: the client hasn't negotiated, so ASCII is what it expects, and
// that's better than no history at all.
func (s *downstream) flushReplay(timeout time.Duration) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-s.negotiated:
	case <-timer.C:
	}

	s.replayMu.Lock()
	defer s.replayMu.Unlock()
	s.replayPending = false
	if _, err := s.telnetSession.Write(s.replayBuf.Bytes()); err != nil {
		s.logger.Error().AnErr("error", err).Msg("error writing history")
	}
	s.replayBuf = bytes.Buffer{}
}

// loginRequest is the JSON a client sends to log in. It is decoded into its
// own type, rather than into downstream, so a client can only set these
// fields.
type loginRequest struct {
	Name     string            `json:"name"`
	Password string            `json:"password"`
	Options  map[string]string `json:"options"`
}

type upstreamConfig struct {
	id       int64
	address  string
	login    string
	password []byte // sealed with the pool's box; NULL if not set
	script   sql.NullString
}

// loadUpstream returns sql.ErrNoRows if the upstream doesn't exist. It is a
// variable so tests can change the database right after a login reads it.
var loadUpstream = func(ctx context.Context, db *sql.DB, name string) (*upstreamConfig, error) {
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

func (s *downstream) connectNewUpstream(config *upstreamConfig, options map[string]string) error {
	for option, value := range options {
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

	// attached only now, so closing the upstream while it connects can't close
	// the client before it is told why, and before the read loop starts, so the
	// client can't miss the game's first output
	if err := s.upstream.AddDownstream(s); err != nil {
		return err
	}
	s.upstream.start()

	// an empty script means the default, as NULL does
	connectScript := config.script.String
	if connectScript == "" {
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
	authenticated := false
	var connecting *upstream // set if this login is the one connecting

	// deferred functions run last first, so the client is told why its
	// login failed before removing the upstream can close the client, and
	// logins waiting on this one are only woken once that is done
	defer func() {
		if connecting != nil {
			connecting.endConnect()
		}
	}()
	defer func() {
		if err != nil && s.upstream != nil {
			s.pool.removeUnconnected(s.upstream)
		}
	}()
	defer func() {
		if err != nil {
			s.writeLoginError(err, authenticated)
		}
	}()

	login, err := s.readLogin()
	if err != nil {
		return err
	}

	if err := auth.CheckPassword(s.Context(), s.pool.db, login.Password); err != nil {
		return err
	}
	authenticated = true

	config, err := loadUpstream(s.Context(), s.pool.db, login.Name)
	if err != nil {
		return err
	}

	u := s.pool.upstreamFor(config.id, login.Name)
	first, done := u.beginConnect()
	if !first {
		// another login connected this upstream, or is connecting it. The
		// client is only attached once that has succeeded, so a failure
		// doesn't close it before it is told why.
		<-done
		// the row may have been deleted since it was loaded
		if err := s.pool.syncUpstream(s.Context(), config.id); err != nil {
			return err
		}
		if !u.IsConnected() {
			return errUpstreamNotConnected
		}
		s.upstream = u
		return s.joinUpstream(u)
	}

	connecting = u
	s.upstream = u

	// the row may have changed since it was loaded. A rename or delete committed
	// before this found no session to update, and one committed after finds this
	// one, so a deleted upstream is never dialed with the password loaded above.
	if err := s.pool.syncUpstream(s.Context(), config.id); err != nil {
		return err
	}

	return s.connectNewUpstream(config, login.Options)
}

// readLogin reads the client's JSON login, which must arrive within
// loginTimeout and maxLoginBytes.
func (s *downstream) readLogin() (*loginRequest, error) {
	if err := s.conn.SetReadDeadline(time.Now().Add(loginTimeout)); err != nil {
		return nil, err
	}
	var login loginRequest
	decoder := json.NewDecoder(&limitReader{r: s, n: maxLoginBytes})
	if err := decoder.Decode(&login); err != nil {
		return nil, err
	}
	// the decoder reads past the end of the login. The first newline is the
	// Enter that finished the login line, so it isn't input for the game.
	buffered, err := io.ReadAll(decoder.Buffered())
	if err != nil {
		return nil, err
	}
	s.afterLogin = bytes.TrimPrefix(buffered, []byte("\n"))
	return &login, s.conn.SetReadDeadline(time.Time{})
}

// writeLoginError tells the client why its login failed. The line ends in
// "\n" alone, which the Telnet writer sends as CR LF.
func (s *downstream) writeLoginError(err error, authenticated bool) {
	s.conn.SetWriteDeadline(time.Now().Add(loginErrorTimeout))
	fmt.Fprintf(s, "login failed: %s\n", loginErrorMessage(err, authenticated))
}

// loginErrorMessage describes err to the client. Errors it doesn't recognize
// are only described to a client that has given the Iris password; anyone
// else gets "internal error", so the details stay in the server log.
func loginErrorMessage(err error, authenticated bool) string {
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
		return "incorrect Iris password"
	case errors.Is(err, sql.ErrNoRows):
		return "upstream not found"
	case errors.Is(err, secret.ErrDecrypt), errors.Is(err, secret.ErrUnsupportedVersion):
		return "unable to decrypt upstream password"
	case errors.Is(err, errUpstreamClosed):
		return errUpstreamClosed.Error()
	case !authenticated:
		return "internal error"
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

func (s *downstream) runForever() {
	s.logger.Debug().Msg("connected")
	defer s.logger.Debug().Msg("disconnected")

	s.negotiateOptions()
	if err := s.connectUpstream(); err != nil {
		s.logger.Info().AnErr("error", err).Msg("error connecting upstream")
		return
	}
	io.Copy(s.upstream, io.MultiReader(bytes.NewReader(s.afterLogin), s))
}

type upstream struct {
	pool *SessionPool
	*telnetSession
	id int64

	// mux guards downstream. It is only held to read or change the list,
	// never while writing to a client, so a client that isn't reading can't
	// keep the upstream from closing.
	mux        sync.Mutex
	downstream []io.WriteCloser

	// state guards the fields below, and setting telnetSession. It can be taken
	// while holding mux, but not the other way around.
	state      sync.Mutex
	name       string
	closed     bool
	history    History
	connecting chan struct{}      // closed once the first login has connected, or failed to
	cancelDial context.CancelFunc // set while dialing, so Close can cancel it

	dispatcher event.Dispatcher
	logger     zerolog.Logger
}

const EventConnectUpstream event.Name = "upstream.connect"

// beginConnect reports whether the caller is the first login to this
// upstream, and so must connect it and then call endConnect. Every later
// login gets a channel that is closed once the first login is done,
// whether or not it connected.
func (s *upstream) beginConnect() (first bool, done <-chan struct{}) {
	s.state.Lock()
	defer s.state.Unlock()
	if s.connecting != nil {
		return false, s.connecting
	}
	s.connecting = make(chan struct{})
	return true, s.connecting
}

// endConnect wakes the logins waiting for the first one.
func (s *upstream) endConnect() {
	s.state.Lock()
	defer s.state.Unlock()
	close(s.connecting)
}

// Connect connects to the game at addr. It fails with errUpstreamClosed if
// the upstream is closed first, such as by being deleted while connecting,
// which also cancels the dial. Call start once it succeeds, after attaching the
// first client.
func (s *upstream) Connect(addr string) error {
	s.state.Lock()
	if s.closed {
		s.state.Unlock()
		return errUpstreamClosed
	}
	name := s.name
	ctx, cancel := context.WithTimeout(context.Background(), dialTimeout)
	defer cancel()
	s.cancelDial = cancel
	s.state.Unlock()

	tcp, err := dial(ctx, "tcp", addr)
	s.state.Lock()
	s.cancelDial = nil
	closed := s.closed
	s.state.Unlock()
	if closed {
		if tcp != nil {
			tcp.Close()
		}
		return errUpstreamClosed
	}
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

	if err := s.AddDownstream(history); err != nil {
		history.Close()
		return err
	}
	s.dispatcher.Dispatch(s.Context(), event.Event{
		Name: EventConnectUpstream,
		Data: s,
	})
	return nil
}

// start begins reading from the game. Anything the game has already sent waits
// for this, so a client attached first receives it.
func (s *upstream) start() {
	go s.runForever()
}

// AddDownstream attaches w, or returns errUpstreamClosed if the upstream is
// closed. Close sets closed before it reads the list, so w is either refused
// here or closed there.
func (s *upstream) AddDownstream(w io.WriteCloser) error {
	s.mux.Lock()
	defer s.mux.Unlock()
	s.state.Lock()
	closed := s.closed
	s.state.Unlock()
	if closed {
		return errUpstreamClosed
	}
	s.downstream = append(s.downstream, w)
	return nil
}

// clients returns a copy of the client list.
func (s *upstream) clients() []io.WriteCloser {
	s.mux.Lock()
	defer s.mux.Unlock()
	return slices.Clone(s.downstream)
}

func (s *upstream) Close() error {
	s.state.Lock()
	s.closed = true
	session := s.telnetSession
	if s.cancelDial != nil {
		s.cancelDial()
	}
	s.state.Unlock()

	for _, wc := range s.clients() {
		wc.Close()
	}
	if session != nil {
		session.Close()
	}
	return nil
}

// IsConnected reports whether the upstream has connected and not been closed.
func (s *upstream) IsConnected() bool {
	s.state.Lock()
	defer s.state.Unlock()
	return s.telnetSession != nil && !s.closed
}

// info returns what GET /sessions reports about the upstream.
func (s *upstream) info() (name string, startedAt time.Time, connected bool) {
	s.state.Lock()
	defer s.state.Unlock()
	if s.telnetSession == nil || s.closed {
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

// sendDownstream writes buf to every client, and drops the ones that fail.
// It writes without holding mux, so a client added meanwhile starts with the
// next buf.
func (s *upstream) sendDownstream(buf []byte) {
	var failed []io.WriteCloser
	for _, wc := range s.clients() {
		if _, err := wc.Write(buf); err != nil {
			failed = append(failed, wc)
		}
	}
	if len(failed) == 0 {
		return
	}
	s.mux.Lock()
	defer s.mux.Unlock()
	s.downstream = slices.DeleteFunc(s.downstream, func(wc io.WriteCloser) bool {
		return slices.Contains(failed, wc)
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
	// mu guards the fields below. It is never held while writing to a client,
	// so one that isn't reading can't block logging.
	mu          sync.Mutex
	file        *os.File
	key         string
	historySize int64
}

func (f *logFile) Open() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.open()
}

func (f *logFile) open() (err error) {
	logFileName := path.Join(
		viper.GetString("log.dir"),
		fmt.Sprintf("%s-%s.log", time.Now().Format("2006-01-02"), f.key),
	)
	f.file, err = os.OpenFile(
		logFileName,
		os.O_APPEND|os.O_CREATE|os.O_WRONLY,
		0644,
	)
	if err == nil {
		t := time.Now()
		fmt.Fprintf(f.file, logSeperator, "opened", t.Format(logTimeFormat))
	}
	return
}

func (f *logFile) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.close()
}

func (f *logFile) close() error {
	t := time.Now()
	fmt.Fprintf(f.file, logSeperator, "closed", t.Format(logTimeFormat))
	return f.file.Close()
}

func (f *logFile) Reopen(name string) (err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err = f.close(); err != nil {
		return
	}
	f.key = name
	return f.open()
}

func (f *logFile) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.file.Write(p)
}

func (f *logFile) WriteTo(w io.Writer) (int64, error) {
	buf, err := f.readHistory()
	if err != nil {
		return 0, err
	}
	n, err := w.Write(buf)
	return int64(n), err
}

// readHistory returns what to replay to a client: the end of the log, starting
// after the last time it was opened.
func (f *logFile) readHistory() ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.file == nil {
		return nil, errors.New("log file not open")
	}
	file, err := os.Open(f.file.Name())
	if err != nil {
		return nil, err
	}
	defer file.Close()
	end, err := file.Seek(0, io.SeekEnd)
	if err != nil {
		return nil, err
	}
	_, err = file.Seek(0, io.SeekStart)
	if err != nil {
		return nil, err
	}
	if end > f.historySize {
		_, err = file.Seek(end-f.historySize, io.SeekCurrent)
		if err != nil {
			return nil, err
		}
	}
	buf := make([]byte, f.historySize)
	n, err := file.Read(buf)
	if err != nil && err != io.EOF {
		return nil, err
	}
	buf = buf[:n]
	if n = bytes.LastIndex(buf, []byte(logSepOpened)); n >= 0 {
		buf = buf[n:]
		n = bytes.IndexByte(buf, '\n')
		buf = buf[n+1:]
	}
	return buf, nil
}
