package serve

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/spf13/viper"
	"golang.org/x/crypto/bcrypt"

	"github.com/stesla/iris/api"
	"github.com/stesla/iris/internal/event"
	"github.com/stesla/iris/internal/secret"
	"github.com/stesla/iris/internal/telnet"
)

// testUpstreamPassword is the password of the "game" upstream in newTestPool.
// It differs from testIrisPassword, so tests can tell which one was used.
const testUpstreamPassword = "game-secret"

func newTestPool(t *testing.T, address string) *SessionPool {
	t.Helper()
	logger = zerolog.Nop()
	viper.Set("log.dir", t.TempDir())

	db, box := newTestDB(t), newTestBox(t)
	insertUpstream(t, db, box, "game", address, testUpstreamPassword)

	p := NewSessionPool(db, box, logger)
	t.Cleanup(p.CloseAll)
	return p
}

// insertUpstream adds an upstream with its password sealed by box.
func insertUpstream(t *testing.T, db *sql.DB, box *secret.Box, name, address, password string) {
	t.Helper()
	res, err := db.Exec("INSERT INTO upstreams (name, address, login) VALUES (?, ?, ?)", name, address, "me")
	if err != nil {
		t.Fatal(err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE upstreams SET password=? WHERE id=?", box.Seal(id, []byte(password)), id); err != nil {
		t.Fatal(err)
	}
}

func loginMsg(name, password string) string {
	return fmt.Sprintf(`{"name":%q,"password":%q}`, name, password)
}

// login sends msg over a pipe and runs connectUpstream on the downstream end,
// discarding anything Iris writes back to the client.
func login(t *testing.T, p *SessionPool, msg string) (*downstream, error) {
	t.Helper()
	d, _, err := loginOutput(t, p, msg)
	return d, err
}

// loginOutput is like login, but also returns a function that disconnects the
// client and returns everything Iris wrote to it.
func loginOutput(t *testing.T, p *SessionPool, msg string) (*downstream, func() string, error) {
	t.Helper()
	client, server := net.Pipe()
	t.Cleanup(func() { client.Close(); server.Close() })
	// writing and reading in separate goroutines, since net.Pipe is
	// unbuffered and Iris may write before it has read everything
	go io.WriteString(client, msg)
	var out bytes.Buffer
	done := make(chan struct{})
	go func() {
		io.Copy(&out, client)
		close(done)
	}()
	d := p.NewDownstream(server)
	err := d.connectUpstream()
	output := func() string {
		server.Close()
		<-done
		return out.String()
	}
	return d, output, err
}

// upstreamID returns the id of the named upstream.
func upstreamID(t *testing.T, db *sql.DB, name string) int64 {
	t.Helper()
	var id int64
	if err := db.QueryRow("SELECT id FROM upstreams WHERE name=?", name).Scan(&id); err != nil {
		t.Fatalf("upstream %q: %v", name, err)
	}
	return id
}

// addConnected puts the named upstream into the pool, looking connected
// without opening a real connection.
func addConnected(t *testing.T, p *SessionPool, name string) *upstream {
	t.Helper()
	conn, remote := net.Pipe()
	t.Cleanup(func() { remote.Close() })
	id := upstreamID(t, p.db, name)
	connected := make(chan struct{})
	close(connected)
	u := &upstream{pool: p, id: id, name: name, telnetSession: newSession(conn, logger), connecting: connected}
	p.streams[id] = u
	return u
}

// holdDials makes each dial to a game wait until n dials have started, or
// until wait has passed, so that concurrent logins overlap. It returns the
// number of dials started.
func holdDials(t *testing.T, n int32, wait time.Duration) *atomic.Int32 {
	t.Helper()
	var started atomic.Int32
	all := make(chan struct{})
	var once sync.Once
	real := dial
	dial = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if started.Add(1) == n {
			once.Do(func() { close(all) })
		}
		select {
		case <-all:
		case <-time.After(wait):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return real(ctx, network, addr)
	}
	t.Cleanup(func() { dial = real })
	return &started
}

// setLoginTimeout changes loginTimeout for the rest of the test.
func setLoginTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	old := loginTimeout
	loginTimeout = d
	t.Cleanup(func() { loginTimeout = old })
}

// countDials counts dials to games, without holding them.
func countDials(t *testing.T) *atomic.Int32 {
	t.Helper()
	return holdDials(t, 1, 0)
}

func poolSize(p *SessionPool) int {
	p.RLock()
	defer p.RUnlock()
	return len(p.streams)
}

// listenUpstream starts a fake game that sends each line it receives to the
// returned channel.
func listenUpstream(t *testing.T) (string, <-chan string) {
	t.Helper()
	addr, lines, _ := listenGame(t)
	return addr, lines
}

// listenGame is like listenUpstream, but also returns a function that sends
// text from the game to Iris.
func listenGame(t *testing.T) (string, <-chan string, func(string)) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	lines := make(chan string, 16)
	accepted := make(chan net.Conn, 1)
	send := func(text string) {
		t.Helper()
		select {
		case conn := <-accepted:
			defer func() { accepted <- conn }()
			if _, err := io.WriteString(conn, text); err != nil {
				t.Fatalf("game sending %q: %v", text, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("Iris never connected to the game")
		}
	}
	go func() {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		accepted <- conn
		conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		r := bufio.NewReader(conn)
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				close(lines)
				return
			}
			lines <- line
		}
	}()
	return l.Addr().String(), lines, send
}

// waitForHistory waits until the named upstream's history log contains want.
func waitForHistory(t *testing.T, name, want string) {
	t.Helper()
	path := filepath.Join(viper.GetString("log.dir"), time.Now().Format("2006-01-02")+"-"+name+".log")
	deadline := time.Now().Add(5 * time.Second)
	for {
		b, _ := os.ReadFile(path)
		if strings.Contains(string(b), want) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("history never contained %q: %q", want, b)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// waitForLine returns the first line from lines that contains want.
func waitForLine(t *testing.T, lines <-chan string, want string) string {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case line, ok := <-lines:
			if !ok {
				t.Fatalf("upstream never received %q", want)
			}
			if strings.Contains(line, want) {
				return line
			}
		case <-timeout:
			t.Fatalf("upstream never received %q", want)
		}
	}
}

func TestLoginConnectedUpstreamWrongPassword(t *testing.T) {
	p := newTestPool(t, "unused:1")
	u := addConnected(t, p, "game")

	_, err := login(t, p, loginMsg("game", "wrong"))
	if !errors.Is(err, bcrypt.ErrMismatchedHashAndPassword) {
		t.Fatalf("got %v", err)
	}
	if len(u.downstream) != 0 {
		t.Errorf("client was attached to upstream: %v", u.downstream)
	}
}

func TestLoginConnectedUpstream(t *testing.T) {
	p := newTestPool(t, "unused:1")
	u := addConnected(t, p, "game")

	d, err := login(t, p, loginMsg("game", testIrisPassword))
	if err != nil {
		t.Fatal(err)
	}
	if d.upstream != u {
		t.Error("client was not attached to the existing upstream")
	}
	if len(u.downstream) != 1 || u.downstream[0] != d {
		t.Errorf("downstreams = %v", u.downstream)
	}
	if poolSize(p) != 1 {
		t.Errorf("pool size = %d", poolSize(p))
	}
}

func TestLoginConnectedUpstreamDoesNotNeedUpstreamPassword(t *testing.T) {
	p := newTestPool(t, "unused:1")
	if _, err := p.db.Exec("UPDATE upstreams SET password=NULL"); err != nil {
		t.Fatal(err)
	}
	u := addConnected(t, p, "game")

	d, err := login(t, p, loginMsg("game", testIrisPassword))
	if err != nil {
		t.Fatal(err)
	}
	if d.upstream != u {
		t.Error("client was not attached to the existing upstream")
	}
}

func TestLoginWithUpstreamPasswordRejected(t *testing.T) {
	// the upstream's own password used to be the login; now only the Iris
	// password is
	p := newTestPool(t, "unused:1")

	_, err := login(t, p, loginMsg("game", testUpstreamPassword))
	if !errors.Is(err, bcrypt.ErrMismatchedHashAndPassword) {
		t.Fatalf("got %v", err)
	}
	if poolSize(p) != 0 {
		t.Errorf("pool size = %d", poolSize(p))
	}
}

func TestLoginUnknownUpstream(t *testing.T) {
	p := newTestPool(t, "unused:1")

	_, err := login(t, p, loginMsg("nope", testIrisPassword))
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("got %v", err)
	}
	if poolSize(p) != 0 {
		t.Errorf("pool size = %d", poolSize(p))
	}
}

func TestLoginUnknownUpstreamWrongPassword(t *testing.T) {
	// the Iris password is checked first, so a client without it can't tell
	// which upstreams exist
	p := newTestPool(t, "unused:1")

	_, err := login(t, p, loginMsg("nope", "wrong"))
	if !errors.Is(err, bcrypt.ErrMismatchedHashAndPassword) {
		t.Fatalf("got %v", err)
	}
}

func TestLoginUnconnectedUpstreamWrongPassword(t *testing.T) {
	p := newTestPool(t, "unused:1")

	_, err := login(t, p, loginMsg("game", "wrong"))
	if !errors.Is(err, bcrypt.ErrMismatchedHashAndPassword) {
		t.Fatalf("got %v", err)
	}
	if poolSize(p) != 0 {
		t.Errorf("pool size = %d", poolSize(p))
	}
}

func TestLoginUpstreamPasswordNotSet(t *testing.T) {
	p := newTestPool(t, "unused:1")
	dials := countDials(t)
	if _, err := p.db.Exec("UPDATE upstreams SET password=NULL"); err != nil {
		t.Fatal(err)
	}

	_, output, err := loginOutput(t, p, loginMsg("game", testIrisPassword))
	if !errors.Is(err, errUpstreamPasswordNotSet) {
		t.Fatalf("got %v", err)
	}
	if poolSize(p) != 0 {
		t.Errorf("pool size = %d", poolSize(p))
	}
	if got := output(); !strings.Contains(got, "login failed: upstream password not set") {
		t.Errorf("client received %q", got)
	}
	if n := dials.Load(); n != 0 {
		t.Errorf("dialed the game %d times without a password", n)
	}
}

func TestLoginUpstreamPasswordUndecryptable(t *testing.T) {
	p := newTestPool(t, "unused:1")
	other, err := secret.NewBox(bytes.Repeat([]byte{8}, secret.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	p.box = other
	dials := countDials(t)

	_, output, err := loginOutput(t, p, loginMsg("game", testIrisPassword))
	if !errors.Is(err, secret.ErrDecrypt) {
		t.Fatalf("got %v", err)
	}
	if n := dials.Load(); n != 0 {
		t.Errorf("dialed the game %d times without a password", n)
	}
	if poolSize(p) != 0 {
		t.Errorf("pool size = %d", poolSize(p))
	}
	if got := output(); !strings.Contains(got, "login failed: unable to decrypt upstream password") {
		t.Errorf("client received %q", got)
	}
}

func TestLoginConnectFailureRemovesUpstream(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	p := newTestPool(t, addr)

	_, output, err := loginOutput(t, p, loginMsg("game", testIrisPassword))
	if err == nil || !strings.Contains(err.Error(), "error connecting") {
		t.Fatalf("got %v", err)
	}
	if poolSize(p) != 0 {
		t.Errorf("pool size = %d", poolSize(p))
	}
	if got := output(); !strings.Contains(got, "login failed: error connecting") {
		t.Errorf("client received %q", got)
	}
}

func TestLoginBadOptionRemovesUpstream(t *testing.T) {
	p := newTestPool(t, "unused:1")

	msg := fmt.Sprintf(`{"name":"game","password":%q,"options":{"always_allow_charset":"maybe"}}`, testIrisPassword)
	_, err := login(t, p, msg)
	if err == nil {
		t.Fatal("expected error for bad option value")
	}
	if poolSize(p) != 0 {
		t.Errorf("pool size = %d", poolSize(p))
	}
}

func TestLoginConnectsNewUpstream(t *testing.T) {
	addr, lines := listenUpstream(t)
	p := newTestPool(t, addr)

	if _, err := login(t, p, loginMsg("game", testIrisPassword)); err != nil {
		t.Fatal(err)
	}

	line := waitForLine(t, lines, "connect me")
	if want := "connect me " + testUpstreamPassword; !strings.Contains(line, want) {
		t.Errorf("upstream received %q, want %q", line, want)
	}
	if strings.Contains(line, testIrisPassword) {
		t.Errorf("Iris password was sent upstream: %q", line)
	}

	p.RLock()
	u := p.streams[upstreamID(t, p.db, "game")]
	p.RUnlock()
	if u == nil || !u.IsConnected() {
		t.Error("upstream is not connected")
	}
}

func TestLoginAfterMalformedSubnegotiation(t *testing.T) {
	const (
		iac = telnet.IAC
		sb  = telnet.SB
		se  = telnet.SE
		cs  = telnet.Charset
		tb  = telnet.TransmitBinary
	)
	enableCharset := []byte{iac, telnet.DO, cs}
	enableBinary := []byte{iac, telnet.DO, tb, iac, telnet.WILL, tb}
	tests := []struct {
		name  string
		parts [][]byte
	}{
		{"no option", [][]byte{{iac, sb, iac, se}}},
		{"no charset command", [][]byte{enableCharset, {iac, sb, cs, iac, se}}},
		{"unknown charset accepted", [][]byte{enableCharset, enableBinary,
			{iac, sb, cs, telnet.CharsetAccepted, 'B', 'O', 'G', 'U', 'S', iac, se}}},
		{"empty charset accepted", [][]byte{enableCharset, enableBinary,
			{iac, sb, cs, telnet.CharsetAccepted, iac, se}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			addr, lines := listenUpstream(t)
			p := newTestPool(t, addr)

			msg := string(bytes.Join(test.parts, nil)) + loginMsg("game", testIrisPassword)
			if _, err := login(t, p, msg); err != nil {
				t.Fatal(err)
			}
			waitForLine(t, lines, "connect me")
		})
	}
}

func TestLoginConnectScript(t *testing.T) {
	addr, lines := listenUpstream(t)
	p := newTestPool(t, addr)
	if _, err := p.db.Exec("UPDATE upstreams SET script='login %LOGIN% with %PASSWORD% now'"); err != nil {
		t.Fatal(err)
	}

	if _, err := login(t, p, loginMsg("game", testIrisPassword)); err != nil {
		t.Fatal(err)
	}
	line := waitForLine(t, lines, "login me")
	if want := "login me with " + testUpstreamPassword + " now"; !strings.Contains(line, want) {
		t.Errorf("upstream received %q, want %q", line, want)
	}
}

func TestLoginEmptyConnectScriptUsesDefault(t *testing.T) {
	addr, lines := listenUpstream(t)
	p := newTestPool(t, addr)
	if _, err := p.db.Exec("UPDATE upstreams SET script=''"); err != nil {
		t.Fatal(err)
	}

	if _, err := login(t, p, loginMsg("game", testIrisPassword)); err != nil {
		t.Fatal(err)
	}
	select {
	case line := <-lines:
		if want := "connect me " + testUpstreamPassword + "\r\n"; line != want {
			t.Errorf("upstream received %q, want %q", line, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("upstream received nothing")
	}
}

func TestLoginErrorMessages(t *testing.T) {
	p := newTestPool(t, "unused:1")
	for msg, want := range map[string]string{
		loginMsg("game", "wrong"):          "login failed: incorrect Iris password",
		loginMsg("nope", testIrisPassword): "login failed: upstream not found",
		`not json`:                         "login failed: invalid login",
		`{"name":42}`:                      "login failed: invalid login",
	} {
		_, output, err := loginOutput(t, p, msg)
		if err == nil {
			t.Errorf("%s: expected error", msg)
			continue
		}
		if got := output(); !strings.Contains(got, want) {
			t.Errorf("%s: client received %q, want %q", msg, got, want)
		}
	}
}

func TestLoginErrorBeforeAuthIsHidden(t *testing.T) {
	p := newTestPool(t, "unused:1")
	if _, err := p.db.Exec("DROP TABLE iris_auth"); err != nil {
		t.Fatal(err)
	}

	_, output, err := loginOutput(t, p, loginMsg("game", testIrisPassword))
	if err == nil || !strings.Contains(err.Error(), "no such table") {
		t.Fatalf("got %v", err)
	}
	got := output()
	if !strings.Contains(got, "login failed: internal error") {
		t.Errorf("client received %q, want internal error", got)
	}
	if strings.Contains(got, "no such table") {
		t.Errorf("client received the database error: %q", got)
	}
}

func TestLoginTimeout(t *testing.T) {
	p := newTestPool(t, "unused:1")
	setLoginTimeout(t, 50*time.Millisecond)

	type result struct {
		output func() string
		err    error
	}
	done := make(chan result, 1)
	go func() {
		_, output, err := loginOutput(t, p, "")
		done <- result{output, err}
	}()

	var r result
	select {
	case r = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("login never timed out")
	}
	if !errors.Is(r.err, os.ErrDeadlineExceeded) {
		t.Fatalf("got %v", r.err)
	}
	if got := r.output(); !strings.Contains(got, "login failed: timed out waiting for login") {
		t.Errorf("client received %q", got)
	}
}

func TestLoginTimeoutClearedAfterLogin(t *testing.T) {
	p := newTestPool(t, "unused:1")
	addConnected(t, p, "game")
	setLoginTimeout(t, 50*time.Millisecond)

	client, server := net.Pipe()
	t.Cleanup(func() { client.Close(); server.Close() })
	go io.Copy(io.Discard, client)
	go io.WriteString(client, loginMsg("game", testIrisPassword))
	d := p.NewDownstream(server)
	if err := d.connectUpstream(); err != nil {
		t.Fatal(err)
	}

	time.Sleep(2 * loginTimeout)
	go io.WriteString(client, "look\n")
	buf := make([]byte, 64)
	n, err := d.Read(buf)
	if err != nil {
		t.Fatalf("read after login: %v", err)
	}
	if !strings.Contains(string(buf[:n]), "look") {
		t.Errorf("read %q", buf[:n])
	}
}

func TestLoginTooLong(t *testing.T) {
	p := newTestPool(t, "unused:1")

	// whitespace keeps the decoder reading without a syntax error
	_, output, err := loginOutput(t, p, strings.Repeat(" ", maxLoginBytes+1))
	if !errors.Is(err, errLoginTooLong) {
		t.Fatalf("got %v", err)
	}
	if got := output(); !strings.Contains(got, "login failed: login too long") {
		t.Errorf("client received %q", got)
	}
}

func TestLoginAtSizeLimit(t *testing.T) {
	p := newTestPool(t, "unused:1")
	addConnected(t, p, "game")

	msg := loginMsg("game", testIrisPassword)
	msg = strings.Repeat(" ", maxLoginBytes-len(msg)) + msg
	if _, err := login(t, p, msg); err != nil {
		t.Fatal(err)
	}
}

func TestLoginAfterRenameJoinsSameSession(t *testing.T) {
	p := newTestPool(t, "unused:1")
	u := addConnected(t, p, "game")
	if _, err := p.db.Exec("UPDATE upstreams SET name='renamed' WHERE name='game'"); err != nil {
		t.Fatal(err)
	}
	if err := p.syncUpstream(context.Background(), u.id); err != nil {
		t.Fatal(err)
	}

	d, err := login(t, p, loginMsg("renamed", testIrisPassword))
	if err != nil {
		t.Fatal(err)
	}
	if d.upstream != u {
		t.Error("login under the new name didn't join the live session")
	}
	if name, _, _ := u.info(); name != "renamed" {
		t.Errorf("name = %q", name)
	}

	_, err = login(t, p, loginMsg("game", testIrisPassword))
	if !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("login under the old name: got %v", err)
	}
}

func TestLoginReusedNameGetsNewSession(t *testing.T) {
	// a session left over from a deleted upstream must never be reached
	// through a new upstream that takes its name
	p := newTestPool(t, "unused:1")
	old := addConnected(t, p, "game")
	if _, err := p.db.Exec("DELETE FROM upstreams WHERE name='game'"); err != nil {
		t.Fatal(err)
	}
	insertUpstream(t, p.db, p.box, "game", "unused:1", "other-secret")
	if id := upstreamID(t, p.db, "game"); id == old.id {
		t.Fatalf("new upstream reused id %d", id)
	}

	d, _ := login(t, p, loginMsg("game", testIrisPassword))
	if d.upstream == old {
		t.Error("login joined the deleted upstream's session")
	}
	if len(old.downstream) != 0 {
		t.Errorf("client was attached to the deleted upstream: %v", old.downstream)
	}
}

func TestConcurrentLoginsConnectOnce(t *testing.T) {
	addr, lines, send := listenGame(t)
	p := newTestPool(t, addr)
	dials := holdDials(t, 2, 500*time.Millisecond)

	type result struct {
		d      *downstream
		output func() string
		err    error
	}
	results := make(chan result, 2)
	for range 2 {
		go func() {
			d, output, err := loginOutput(t, p, loginMsg("game", testIrisPassword))
			results <- result{d, output, err}
		}()
	}
	var rs []result
	for range 2 {
		r := <-results
		if r.err != nil {
			t.Fatal(r.err)
		}
		rs = append(rs, r)
	}

	if n := dials.Load(); n != 1 {
		t.Errorf("dialed the game %d times", n)
	}
	if rs[0].d.upstream != rs[1].d.upstream {
		t.Fatal("clients were attached to different upstreams")
	}
	waitForLine(t, lines, "connect me")

	// Iris only reads the game's second line once it has written the first
	// to every client, so once the second is in the history log, both
	// clients have the first
	send("welcome\n")
	waitForHistory(t, "game", "welcome")
	send("second\n")
	waitForHistory(t, "game", "second")
	for i, r := range rs {
		if got := r.output(); !strings.Contains(got, "welcome") {
			t.Errorf("client %d received %q, want the game's output", i, got)
		}
	}
	// the connect script was sent once, and nothing else
	select {
	case line := <-lines:
		t.Errorf("upstream received another line %q", line)
	default:
	}
}

// stuckClient is a client that never reads: writes block until it is
// closed.
type stuckClient struct {
	writing chan struct{}
	closed  chan struct{}
	once    sync.Once
}

func newStuckClient() *stuckClient {
	return &stuckClient{writing: make(chan struct{}, 1), closed: make(chan struct{})}
}

func (c *stuckClient) Write(p []byte) (int, error) {
	select {
	case c.writing <- struct{}{}:
	default:
	}
	<-c.closed
	return 0, net.ErrClosed
}

func (c *stuckClient) Close() error {
	c.once.Do(func() { close(c.closed) })
	return nil
}

func TestDeleteWithStuckClient(t *testing.T) {
	addr, _, send := listenGame(t)
	p := newTestPool(t, addr)
	d, err := login(t, p, loginMsg("game", testIrisPassword))
	if err != nil {
		t.Fatal(err)
	}
	stuck := newStuckClient()
	d.upstream.AddDownstream(stuck)

	// the game's output leaves the upstream's read loop blocked writing to
	// the stuck client
	send("hello\n")
	select {
	case <-stuck.writing:
	case <-time.After(5 * time.Second):
		t.Fatal("game output never reached the stuck client")
	}

	done := make(chan struct{})
	go func() {
		p.closeUpstream(upstreamID(t, p.db, "game"))
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a client that isn't reading blocked deleting the upstream")
	}
}

func TestConcurrentLoginWhenFirstFails(t *testing.T) {
	p := newTestPool(t, "unused:1")
	real := dial
	dial = func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("connection refused")
	}
	t.Cleanup(func() { dial = real })
	holdDials(t, 2, 500*time.Millisecond)

	type result struct {
		output string
		err    error
	}
	results := make(chan result, 2)
	for range 2 {
		go func() {
			_, output, err := loginOutput(t, p, loginMsg("game", testIrisPassword))
			results <- result{output(), err}
		}()
	}
	var outputs []string
	for range 2 {
		r := <-results
		if r.err == nil {
			t.Fatal("login succeeded")
		}
		outputs = append(outputs, r.output)
	}
	if poolSize(p) != 0 {
		t.Errorf("pool size = %d", poolSize(p))
	}
	// both clients are told why, including the one that was waiting
	for _, want := range []string{"login failed: error connecting", "login failed: " + errUpstreamNotConnected.Error()} {
		if !strings.Contains(outputs[0], want) && !strings.Contains(outputs[1], want) {
			t.Errorf("no client received %q: %q", want, outputs)
		}
	}
}

func TestConnectAfterCloseDoesNotDial(t *testing.T) {
	p := newTestPool(t, "unused:1")
	dials := countDials(t)
	u := p.upstreamFor(upstreamID(t, p.db, "game"), "game")
	u.Close()

	if err := u.Connect("unused:1"); !errors.Is(err, errUpstreamClosed) {
		t.Fatalf("got %v", err)
	}
	if n := dials.Load(); n != 0 {
		t.Errorf("dialed the game %d times", n)
	}
}

func TestCloseCancelsDial(t *testing.T) {
	p := newTestPool(t, "unused:1")
	started := make(chan struct{})
	real := dial
	dial = func(ctx context.Context, network, addr string) (net.Conn, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	t.Cleanup(func() { dial = real })
	id := upstreamID(t, p.db, "game")

	done := make(chan error, 1)
	go func() {
		_, err := login(t, p, loginMsg("game", testIrisPassword))
		done <- err
	}()
	<-started
	p.closeUpstream(id)

	select {
	case err := <-done:
		if !errors.Is(err, errUpstreamClosed) {
			t.Errorf("got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("deleting the upstream didn't cancel the dial")
	}
}

func TestDialTimeout(t *testing.T) {
	p := newTestPool(t, "unused:1")
	old := dialTimeout
	dialTimeout = 50 * time.Millisecond
	t.Cleanup(func() { dialTimeout = old })
	real := dial
	dial = func(ctx context.Context, network, addr string) (net.Conn, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	t.Cleanup(func() { dial = real })

	type result struct {
		output func() string
		err    error
	}
	done := make(chan result, 1)
	go func() {
		_, output, err := loginOutput(t, p, loginMsg("game", testIrisPassword))
		done <- result{output, err}
	}()
	var r result
	select {
	case r = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("dial didn't time out")
	}
	if !errors.Is(r.err, context.DeadlineExceeded) {
		t.Fatalf("got %v", r.err)
	}
	if got := r.output(); !strings.Contains(got, "login failed: error connecting") {
		t.Errorf("client received %q", got)
	}
	if poolSize(p) != 0 {
		t.Errorf("pool size = %d", poolSize(p))
	}
}

func TestCloseWhileDialing(t *testing.T) {
	addr, lines := listenUpstream(t)
	p := newTestPool(t, addr)
	u := p.upstreamFor(upstreamID(t, p.db, "game"), "game")
	// closing just after the dial succeeds, before the connection is used
	real := dial
	dial = func(ctx context.Context, network, addr string) (net.Conn, error) {
		conn, err := real(ctx, network, addr)
		u.Close()
		return conn, err
	}
	t.Cleanup(func() { dial = real })

	if err := u.Connect(addr); !errors.Is(err, errUpstreamClosed) {
		t.Fatalf("got %v", err)
	}
	if u.IsConnected() {
		t.Error("closed upstream is connected")
	}
	// the fake game sees the connection closed without anything sent
	select {
	case line, ok := <-lines:
		if ok {
			t.Errorf("upstream received %q", line)
		}
	case <-time.After(5 * time.Second):
		t.Error("connection to the game was left open")
	}
}

func TestLoginWhileDeletedReportsError(t *testing.T) {
	addr, _ := listenUpstream(t)
	p := newTestPool(t, addr)
	id := upstreamID(t, p.db, "game")
	// closing the upstream as soon as it is added to the pool, as deleting
	// it mid-connect would
	u := p.upstreamFor(id, "game")
	u.Close()

	_, output, err := loginOutput(t, p, loginMsg("game", testIrisPassword))
	if !errors.Is(err, errUpstreamClosed) {
		t.Fatalf("got %v", err)
	}
	if poolSize(p) != 0 {
		t.Errorf("pool size = %d", poolSize(p))
	}
	got := output()
	if !strings.Contains(got, "login failed: "+errUpstreamClosed.Error()+"\r\n") {
		t.Errorf("client received %q", got)
	}
	if strings.Contains(got, "\r\x00") {
		t.Errorf("login error ends with a bare CR: %q", got)
	}
}

func TestReopenHistoriesUsesNewName(t *testing.T) {
	addr, _ := listenUpstream(t)
	p := newTestPool(t, addr)
	if _, err := login(t, p, loginMsg("game", testIrisPassword)); err != nil {
		t.Fatal(err)
	}
	id := upstreamID(t, p.db, "game")
	dir := viper.GetString("log.dir")
	date := time.Now().Format("2006-01-02")
	if _, err := os.Stat(filepath.Join(dir, date+"-game.log")); err != nil {
		t.Fatal(err)
	}

	if _, err := p.db.Exec("UPDATE upstreams SET name='renamed' WHERE id=?", id); err != nil {
		t.Fatal(err)
	}
	if err := p.syncUpstream(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	p.ReopenHistories()
	if _, err := os.Stat(filepath.Join(dir, date+"-renamed.log")); err != nil {
		t.Errorf("log under new name: %v", err)
	}
}

func TestCloseAllAndReopenWithUnconnectedUpstream(t *testing.T) {
	p := newTestPool(t, "unused:1")
	p.upstreamFor(99, "connecting")

	p.ReopenHistories()
	p.CloseAll()
}

// useLogDir points log.dir at a temporary directory, which it returns.
func useLogDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	viper.Set("log.dir", dir)
	return dir
}

// readLogs returns the contents of every log file in dir. A test that runs
// past midnight writes to two of them.
func readLogs(t *testing.T, dir string) string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(dir, "*.log"))
	if err != nil {
		t.Fatal(err)
	}
	var all strings.Builder
	for _, path := range paths {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		all.Write(b)
	}
	return all.String()
}

func TestLogFileConcurrentUse(t *testing.T) {
	dir := useLogDir(t)
	history, err := newHistory("stress")
	if err != nil {
		t.Fatal(err)
	}
	defer history.Close()

	const writers, lines = 2, 500
	var wg sync.WaitGroup
	for w := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range lines {
				if _, err := fmt.Fprintf(history, "writer-%d-line-%d\n", w, i); err != nil {
					t.Errorf("write: %v", err)
					return
				}
			}
		}()
	}

	done := make(chan struct{})
	var background sync.WaitGroup
	background.Add(2)
	go func() {
		defer background.Done()
		for {
			select {
			case <-done:
				return
			default:
			}
			if err := history.Reopen("stress"); err != nil {
				t.Errorf("reopen: %v", err)
				return
			}
		}
	}()
	go func() {
		defer background.Done()
		for {
			select {
			case <-done:
				return
			default:
			}
			if _, err := history.WriteTo(io.Discard); err != nil {
				t.Errorf("replay: %v", err)
				return
			}
		}
	}()

	wg.Wait()
	close(done)
	background.Wait()

	logs := readLogs(t, dir)
	for w := range writers {
		for i := range lines {
			line := fmt.Sprintf("writer-%d-line-%d\n", w, i)
			if got := strings.Count(logs, line); got != 1 {
				t.Errorf("%q appears %d times", line, got)
			}
		}
	}
}

func TestGameOutputDuringReopenHistories(t *testing.T) {
	addr, _, send := listenGame(t)
	p := newTestPool(t, addr)
	d, err := login(t, p, loginMsg("game", testIrisPassword))
	if err != nil {
		t.Fatal(err)
	}
	u := d.upstream

	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-done:
				return
			case <-time.After(time.Millisecond):
				p.ReopenHistories()
			}
		}
	}()

	const lines = 500
	for i := range lines {
		send(fmt.Sprintf("line-%d\n", i))
	}
	waitForHistory(t, "game", fmt.Sprintf("line-%d\n", lines-1))
	close(done)
	wg.Wait()

	u.state.Lock()
	history := u.history
	u.state.Unlock()
	if !slices.Contains(u.clients(), io.WriteCloser(history)) {
		t.Error("the history was dropped from the upstream's clients")
	}
	logs := readLogs(t, viper.GetString("log.dir"))
	for i := range lines {
		if line := fmt.Sprintf("line-%d\n", i); !strings.Contains(logs, line) {
			t.Errorf("%q is missing from the log", line)
		}
	}
}

func TestLogFileReopenAfterClose(t *testing.T) {
	dir := useLogDir(t)
	history, err := newHistory("game")
	if err != nil {
		t.Fatal(err)
	}
	if err := history.Close(); err != nil {
		t.Fatal(err)
	}

	if err := history.Reopen("other"); err == nil {
		t.Error("no error")
	}
	if _, err := history.Write([]byte("late\n")); err == nil {
		t.Error("write after close: no error")
	}
	paths, err := filepath.Glob(filepath.Join(dir, "*-other.log"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 0 {
		t.Errorf("reopened as %v", paths)
	}
}

func TestLogFileReplayToStuckClientDoesNotBlockWrite(t *testing.T) {
	useLogDir(t)
	history, err := newHistory("game")
	if err != nil {
		t.Fatal(err)
	}
	defer history.Close()

	stuck := newStuckClient()
	defer stuck.Close()
	go history.WriteTo(stuck)
	select {
	case <-stuck.writing:
	case <-time.After(5 * time.Second):
		t.Fatal("replay never reached the stuck client")
	}

	written := make(chan error, 1)
	go func() {
		_, err := history.Write([]byte("hello\n"))
		written <- err
	}()
	select {
	case err := <-written:
		if err != nil {
			t.Error(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a client that isn't reading blocked writing to the log")
	}
}

func TestSyncUpstreamUsesLatestCommit(t *testing.T) {
	p := newTestPool(t, "unused:1")
	u := addConnected(t, p, "game")
	for _, name := range []string{"y", "z"} {
		if _, err := p.db.Exec("UPDATE upstreams SET name=? WHERE id=?", name, u.id); err != nil {
			t.Fatal(err)
		}
	}

	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := p.syncUpstream(context.Background(), u.id); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()

	if name, _, _ := u.info(); name != "z" {
		t.Errorf("name = %q", name)
	}
}

func TestSyncUpstreamDuringCommits(t *testing.T) {
	p := newTestPool(t, "unused:1")
	u := addConnected(t, p, "game")

	var wg sync.WaitGroup
	for w := range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 20 {
				name := fmt.Sprintf("w%d-%d", w, i)
				if _, err := p.db.Exec("UPDATE upstreams SET name=? WHERE id=?", name, u.id); err != nil {
					t.Error(err)
					return
				}
				if err := p.syncUpstream(context.Background(), u.id); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	wg.Wait()

	var stored string
	if err := p.db.QueryRow("SELECT name FROM upstreams WHERE id=?", u.id).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if name, _, _ := u.info(); name != stored {
		t.Errorf("database says %q, session says %q", stored, name)
	}
}

// TestSyncUpstreamRunsOneAtATime checks the lock that keeps a slow sync, which
// read an older name, from overwriting a newer one.
func TestSyncUpstreamRunsOneAtATime(t *testing.T) {
	p := newTestPool(t, "unused:1")
	u := addConnected(t, p, "game")
	if _, err := p.db.Exec("UPDATE upstreams SET name='renamed' WHERE id=?", u.id); err != nil {
		t.Fatal(err)
	}

	p.syncMu.Lock()
	done := make(chan error, 1)
	go func() { done <- p.syncUpstream(context.Background(), u.id) }()
	select {
	case <-done:
		p.syncMu.Unlock()
		t.Fatal("sync ran while another held the lock")
	case <-time.After(50 * time.Millisecond):
	}
	if name, _, _ := u.info(); name != "game" {
		t.Errorf("name changed to %q while another sync held the lock", name)
	}
	p.syncMu.Unlock()

	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("sync never finished")
	}
	if name, _, _ := u.info(); name != "renamed" {
		t.Errorf("name = %q", name)
	}
}

func TestSyncUpstreamNoSession(t *testing.T) {
	p := newTestPool(t, "unused:1")
	if err := p.syncUpstream(context.Background(), upstreamID(t, p.db, "game")); err != nil {
		t.Error(err)
	}
	if poolSize(p) != 0 {
		t.Errorf("pool size = %d", poolSize(p))
	}
}

func TestSyncUpstreamQueryFailureKeepsName(t *testing.T) {
	p := newTestPool(t, "unused:1")
	u := addConnected(t, p, "game")
	if _, err := p.db.Exec("UPDATE upstreams SET name='renamed' WHERE id=?", u.id); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := p.syncUpstream(ctx, u.id); !errors.Is(err, context.Canceled) {
		t.Errorf("got %v", err)
	}
	if name, _, _ := u.info(); name != "game" {
		t.Errorf("name = %q", name)
	}
}

// afterLoadUpstream runs f each time a login has read its upstream's row, the
// moment at which a REST request can race with the login.
func afterLoadUpstream(t *testing.T, f func()) {
	t.Helper()
	real := loadUpstream
	loadUpstream = func(ctx context.Context, db *sql.DB, name string) (*upstreamConfig, error) {
		config, err := real(ctx, db, name)
		f()
		return config, err
	}
	t.Cleanup(func() { loadUpstream = real })
}

func TestLoginRenamedAfterLoadUsesNewName(t *testing.T) {
	addr, lines := listenUpstream(t)
	p := newTestPool(t, addr)
	dir := useLogDir(t)
	srv := &apiServer{db: p.db, sessions: p, box: p.box}
	newName := "renamed"
	afterLoadUpstream(t, func() {
		if err := srv.editUpstream(context.Background(), "game", &api.EditUpstreamRequest{NewName: &newName}); err != nil {
			t.Error(err)
		}
	})

	if _, err := login(t, p, loginMsg("game", testIrisPassword)); err != nil {
		t.Fatal(err)
	}
	waitForLine(t, lines, "connect me "+testUpstreamPassword)

	sessions := srv.listSessions()
	if len(sessions) != 1 || sessions[0].UpstreamName != "renamed" {
		t.Errorf("sessions = %+v", sessions)
	}
	if paths, _ := filepath.Glob(filepath.Join(dir, "*-renamed.log")); len(paths) == 0 {
		t.Error("no log under the new name")
	}
	if paths, _ := filepath.Glob(filepath.Join(dir, "*-game.log")); len(paths) != 0 {
		t.Errorf("log under the old name: %v", paths)
	}
}

func TestLoginRenamedWhileDialingUpdatesSession(t *testing.T) {
	addr, _ := listenUpstream(t)
	p := newTestPool(t, addr)
	srv := &apiServer{db: p.db, sessions: p, box: p.box}
	newName := "renamed"
	real := dial
	dial = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if err := srv.editUpstream(context.Background(), "game", &api.EditUpstreamRequest{NewName: &newName}); err != nil {
			t.Error(err)
		}
		return real(ctx, network, addr)
	}
	t.Cleanup(func() { dial = real })

	if _, err := login(t, p, loginMsg("game", testIrisPassword)); err != nil {
		t.Fatal(err)
	}
	sessions := srv.listSessions()
	if len(sessions) != 1 || sessions[0].UpstreamName != "renamed" {
		t.Errorf("sessions = %+v", sessions)
	}
}

func TestLoginDeletedAfterLoadDoesNotConnect(t *testing.T) {
	addr, _ := listenUpstream(t)
	p := newTestPool(t, addr)
	srv := &apiServer{db: p.db, sessions: p, box: p.box}
	dials := countDials(t)
	afterLoadUpstream(t, func() {
		if err := srv.deleteUpstream(context.Background(), "game"); err != nil {
			t.Error(err)
		}
	})

	_, output, err := loginOutput(t, p, loginMsg("game", testIrisPassword))
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("got %v", err)
	}
	if got := output(); !strings.Contains(got, "login failed: upstream not found\r\n") {
		t.Errorf("client received %q", got)
	}
	if n := dials.Load(); n != 0 {
		t.Errorf("dials = %d", n)
	}
	if poolSize(p) != 0 {
		t.Errorf("pool size = %d", poolSize(p))
	}
}

func TestAddDownstreamAfterCloseIsRefused(t *testing.T) {
	u := &upstream{}
	u.Close()

	if err := u.AddDownstream(&closeRecorder{}); !errors.Is(err, errUpstreamClosed) {
		t.Errorf("got %v", err)
	}
	if n := len(u.clients()); n != 0 {
		t.Errorf("clients = %d", n)
	}
}

func TestIsConnectedFalseAfterClose(t *testing.T) {
	p := newTestPool(t, "unused:1")
	u := addConnected(t, p, "game")
	if !u.IsConnected() {
		t.Fatal("not connected before closing")
	}
	u.Close()
	if u.IsConnected() {
		t.Error("connected after closing")
	}
}

func TestLoginToClosedUpstreamIsNotAttached(t *testing.T) {
	p := newTestPool(t, "unused:1")
	u := addConnected(t, p, "game")
	u.Close()

	d, output, err := loginOutput(t, p, loginMsg("game", testIrisPassword))
	if !errors.Is(err, errUpstreamNotConnected) {
		t.Fatalf("got %v", err)
	}
	if d.upstream != nil {
		t.Error("client has an upstream")
	}
	if n := len(u.clients()); n != 0 {
		t.Errorf("clients = %d", n)
	}
	if got := output(); !strings.Contains(got, "login failed: "+errUpstreamNotConnected.Error()+"\r\n") {
		t.Errorf("client received %q", got)
	}
}

// TestCloseDuringAddDownstream checks that a client can't slip in as the
// upstream closes: each one is either refused or closed.
func TestCloseDuringAddDownstream(t *testing.T) {
	for range 200 {
		u := &upstream{}
		clients := make([]*closeRecorder, 8)
		accepted := make([]bool, len(clients))
		var wg sync.WaitGroup
		for i := range clients {
			clients[i] = &closeRecorder{}
			wg.Add(1)
			go func() {
				defer wg.Done()
				accepted[i] = u.AddDownstream(clients[i]) == nil
			}()
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			u.Close()
		}()
		wg.Wait()

		for i, c := range clients {
			if accepted[i] && !c.closed {
				t.Fatal("accepted a client and never closed it")
			}
			if !accepted[i] && c.closed {
				t.Error("refused a client but closed it")
			}
		}
	}
}

func TestSyncUpstreamMissingRowClosesSession(t *testing.T) {
	p := newTestPool(t, "unused:1")
	u := addConnected(t, p, "game")
	if _, err := p.db.Exec("DELETE FROM upstreams WHERE id=?", u.id); err != nil {
		t.Fatal(err)
	}

	if err := p.syncUpstream(context.Background(), u.id); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("got %v", err)
	}
	if u.IsConnected() {
		t.Error("session is still connected")
	}
	if poolSize(p) != 0 {
		t.Errorf("pool size = %d", poolSize(p))
	}
}

// TestLoginJoiningDeletedUpstream deletes only the row, as a delete has done
// just before it reaches closeUpstream.
func TestLoginJoiningDeletedUpstream(t *testing.T) {
	p := newTestPool(t, "unused:1")
	u := addConnected(t, p, "game")
	dials := countDials(t)
	afterLoadUpstream(t, func() {
		if _, err := p.db.Exec("DELETE FROM upstreams WHERE id=?", u.id); err != nil {
			t.Error(err)
		}
	})

	d, output, err := loginOutput(t, p, loginMsg("game", testIrisPassword))
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("got %v", err)
	}
	if got := output(); !strings.Contains(got, "login failed: upstream not found\r\n") {
		t.Errorf("client received %q", got)
	}
	if d.upstream != nil {
		t.Error("client has an upstream")
	}
	if n := len(u.clients()); n != 0 {
		t.Errorf("clients = %d", n)
	}
	if u.IsConnected() {
		t.Error("session is still connected")
	}
	if poolSize(p) != 0 {
		t.Errorf("pool size = %d", poolSize(p))
	}
	if n := dials.Load(); n != 0 {
		t.Errorf("dials = %d", n)
	}
}

// TestLoginWaitingOnDeletedUpstream has a first login that never connected, so
// the client is told the upstream is gone rather than that it failed to connect.
func TestLoginWaitingOnDeletedUpstream(t *testing.T) {
	p := newTestPool(t, "unused:1")
	id := upstreamID(t, p.db, "game")
	u := p.upstreamFor(id, "game")
	u.beginConnect()
	u.endConnect()
	afterLoadUpstream(t, func() {
		if _, err := p.db.Exec("DELETE FROM upstreams WHERE id=?", id); err != nil {
			t.Error(err)
		}
	})

	if _, err := login(t, p, loginMsg("game", testIrisPassword)); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("got %v", err)
	}
	if poolSize(p) != 0 {
		t.Errorf("pool size = %d", poolSize(p))
	}
}

// TestRenameRacingDeleteIsNotAnError deletes the upstream between a rename's
// commit and its sync, which is a normal outcome rather than something to log.
func TestRenameRacingDeleteIsNotAnError(t *testing.T) {
	p := newTestPool(t, "unused:1")
	srv := &apiServer{db: p.db, sessions: p, box: p.box}
	u := addConnected(t, p, "game")
	id := u.id

	var logged bytes.Buffer
	oldLogger := logger
	logger = zerolog.New(&logged)
	t.Cleanup(func() { logger = oldLogger })

	p.syncMu.Lock()
	newName := "renamed"
	done := make(chan error, 1)
	go func() {
		done <- srv.editUpstream(context.Background(), "game", &api.EditUpstreamRequest{NewName: &newName})
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var name string
		if err := p.db.QueryRow("SELECT name FROM upstreams WHERE id=?", id).Scan(&name); err != nil {
			p.syncMu.Unlock()
			t.Fatal(err)
		}
		if name == newName {
			break
		}
		if time.Now().After(deadline) {
			p.syncMu.Unlock()
			t.Fatal("rename never committed")
		}
		time.Sleep(time.Millisecond)
	}
	if err := srv.deleteUpstream(context.Background(), newName); err != nil {
		p.syncMu.Unlock()
		t.Fatal(err)
	}
	p.syncMu.Unlock()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("rename never finished")
	}
	if got := logged.String(); strings.Contains(got, "error syncing") {
		t.Errorf("logged %q", got)
	}
}

func TestDeleteWhileDialingTellsClient(t *testing.T) {
	p := newTestPool(t, "unused:1")
	srv := &apiServer{db: p.db, sessions: p, box: p.box}
	started := make(chan struct{})
	real := dial
	dial = func(ctx context.Context, network, addr string) (net.Conn, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	t.Cleanup(func() { dial = real })

	client, server := net.Pipe()
	t.Cleanup(func() { client.Close(); server.Close() })
	go io.WriteString(client, loginMsg("game", testIrisPassword))
	var out bytes.Buffer
	disconnected := make(chan struct{})
	go func() {
		io.Copy(&out, client)
		close(disconnected)
	}()
	// the same steps as the goroutine serving a client in runTelnetProxy
	go func() {
		d := p.NewDownstream(server)
		defer d.Close()
		d.runForever()
	}()

	<-started
	if err := srv.deleteUpstream(context.Background(), "game"); err != nil {
		t.Fatal(err)
	}

	select {
	case <-disconnected:
	case <-time.After(5 * time.Second):
		t.Fatal("client was never disconnected")
	}
	if got := out.String(); !strings.Contains(got, "login failed: "+errUpstreamClosed.Error()+"\r\n") {
		t.Errorf("client received %q", got)
	}
}

// TestCloseAfterConnectBeforeAttach closes the upstream once it has connected
// but before the client is attached, so the client is refused and told why.
func TestCloseAfterConnectBeforeAttach(t *testing.T) {
	addr, lines := listenUpstream(t)
	p := newTestPool(t, addr)
	u := p.upstreamFor(upstreamID(t, p.db, "game"), "game")
	u.dispatcher.ListenFunc(EventConnectUpstream, func(context.Context, event.Event) error {
		u.Close()
		return nil
	})

	_, output, err := loginOutput(t, p, loginMsg("game", testIrisPassword))
	if !errors.Is(err, errUpstreamClosed) {
		t.Fatalf("got %v", err)
	}
	if got := output(); !strings.Contains(got, "login failed: "+errUpstreamClosed.Error()+"\r\n") {
		t.Errorf("client received %q", got)
	}
	if poolSize(p) != 0 {
		t.Errorf("pool size = %d", poolSize(p))
	}
	timeout := time.After(5 * time.Second)
	for {
		select {
		case line, ok := <-lines:
			if !ok {
				return
			}
			t.Errorf("game received %q", line)
		case <-timeout:
			t.Fatal("connection to the game was left open")
		}
	}
}

func TestFirstClientReceivesGameOutputSentOnAccept(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		t.Cleanup(func() { conn.Close() })
		io.WriteString(conn, "welcome to the game\r\n")
	}()
	p := newTestPool(t, l.Addr().String())

	client, server := net.Pipe()
	t.Cleanup(func() { client.Close(); server.Close() })
	go io.WriteString(client, loginMsg("game", testIrisPassword))
	received := make(chan struct{})
	go func() {
		var out []byte
		buf := make([]byte, readBufSize)
		for {
			n, err := client.Read(buf)
			if err != nil {
				return
			}
			out = append(out, buf[:n]...)
			if bytes.Contains(out, []byte("welcome to the game")) {
				close(received)
				io.Copy(io.Discard, client)
				return
			}
		}
	}()
	if err := p.NewDownstream(server).connectUpstream(); err != nil {
		t.Fatal(err)
	}

	select {
	case <-received:
	case <-time.After(5 * time.Second):
		t.Fatal("client never received the game's first output")
	}
}

// serveClient serves a client as runTelnetProxy does, sending input in one
// write and discarding what Iris writes back. The returned channel is closed
// when Iris is done with the client.
func serveClient(t *testing.T, p *SessionPool, input string) (net.Conn, <-chan struct{}) {
	t.Helper()
	client, server := net.Pipe()
	t.Cleanup(func() { client.Close(); server.Close() })
	go io.Copy(io.Discard, client)
	go io.WriteString(client, input)
	done := make(chan struct{})
	go func() {
		defer close(done)
		d := p.NewDownstream(server)
		defer d.Close()
		d.runForever()
	}()
	return client, done
}

// stripNegotiation removes Telnet option negotiation, which Iris sends to the
// game at the same time as the client's input.
func stripNegotiation(line string) string {
	var out strings.Builder
	for i := 0; i < len(line); i++ {
		if line[i] == telnet.IAC && i+2 < len(line) {
			i += 2
			continue
		}
		out.WriteByte(line[i])
	}
	return out.String()
}

// wantLines checks that the game receives exactly the given lines, in order.
func wantLines(t *testing.T, lines <-chan string, want ...string) {
	t.Helper()
	for _, w := range want {
		select {
		case line, ok := <-lines:
			if !ok {
				t.Fatalf("connection closed waiting for %q", w)
			}
			if got := stripNegotiation(line); got != w {
				t.Fatalf("game received %q, want %q", got, w)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("game never received %q", w)
		}
	}
}

func TestInputSentWithLogin(t *testing.T) {
	const script = "connect me " + testUpstreamPassword + "\r\n"
	tests := []struct {
		name  string
		after string // sent in the same write as the login
		want  []string
	}{
		{"line after login", "\nlook\n", []string{script, "look\r\n"}},
		{"crlf line after login", "\r\nlook\r\n", []string{script, "look\r\n"}},
		{"only the line ending", "\n", []string{script}},
		{"blank line after login", "\n\nlook\n", []string{script, "\r\n", "look\r\n"}},
		{"no line ending", "", []string{script}},
		{"input without a line ending first", "look\n", []string{script, "look\r\n"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			addr, lines := listenUpstream(t)
			p := newTestPool(t, addr)
			client, _ := serveClient(t, p, loginMsg("game", testIrisPassword)+tt.after)
			wantLines(t, lines, tt.want...)

			// the next thing the game receives is what the client sends next,
			// so nothing extra was forwarded
			go io.WriteString(client, "marker\n")
			wantLines(t, lines, "marker\r\n")
		})
	}
}

func TestPartialLineSentWithLoginIsFinishedLater(t *testing.T) {
	addr, lines := listenUpstream(t)
	p := newTestPool(t, addr)
	client, _ := serveClient(t, p, loginMsg("game", testIrisPassword)+"\nlook")
	wantLines(t, lines, "connect me "+testUpstreamPassword+"\r\n")

	go io.WriteString(client, "\nmarker\n")
	wantLines(t, lines, "look\r\n", "marker\r\n")
}

func TestInputSentWithLoginJoiningConnectedUpstream(t *testing.T) {
	addr, lines := listenUpstream(t)
	p := newTestPool(t, addr)
	serveClient(t, p, loginMsg("game", testIrisPassword)+"\n")
	wantLines(t, lines, "connect me "+testUpstreamPassword+"\r\n")

	second, _ := serveClient(t, p, loginMsg("game", testIrisPassword)+"\nlook\n")
	wantLines(t, lines, "look\r\n")

	go io.WriteString(second, "marker\n")
	wantLines(t, lines, "marker\r\n")
}

func TestFailedLoginForwardsNothing(t *testing.T) {
	addr, lines := listenUpstream(t)
	p := newTestPool(t, addr)
	first, _ := serveClient(t, p, loginMsg("game", testIrisPassword)+"\n")
	wantLines(t, lines, "connect me "+testUpstreamPassword+"\r\n")

	_, done := serveClient(t, p, loginMsg("game", "wrong")+"\nlook\n")
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("failed login never finished")
	}

	go io.WriteString(first, "marker\n")
	wantLines(t, lines, "marker\r\n")
}
