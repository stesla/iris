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
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/spf13/viper"
	"golang.org/x/crypto/bcrypt"

	"github.com/stesla/iris/internal/secret"
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
	p.renameUpstream(u.id, "renamed")

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

	p.renameUpstream(id, "renamed")
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
