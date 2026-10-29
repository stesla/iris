package serve

import (
	"bufio"
	"database/sql"
	"errors"
	"io"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/spf13/viper"
	"golang.org/x/crypto/bcrypt"
)

func newTestPool(t *testing.T, address string) *SessionPool {
	t.Helper()
	logger = zerolog.Nop()
	viper.Set("log.dir", t.TempDir())

	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(testSchema); err != nil {
		t.Fatal(err)
	}

	hash, err := bcrypt.GenerateFromPassword([]byte("secret"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec("INSERT INTO upstreams (name, address, login, bcrypt) VALUES (?, ?, ?, ?)",
		"game", address, "me", hash)
	if err != nil {
		t.Fatal(err)
	}

	p := NewSessionPool(db, logger)
	t.Cleanup(p.CloseAll)
	return p
}

// login sends a JSON login over a pipe and runs connectUpstream on the
// downstream end, discarding anything Iris writes back to the client.
func login(t *testing.T, p *SessionPool, msg string) (*downstream, error) {
	t.Helper()
	client, server := net.Pipe()
	t.Cleanup(func() { client.Close(); server.Close() })
	go func() {
		if _, err := io.WriteString(client, msg); err == nil {
			io.Copy(io.Discard, client)
		}
	}()
	d := p.NewDownstream(server)
	return d, d.connectUpstream()
}

// addConnected puts an upstream into the pool that looks connected without
// opening a real connection.
func addConnected(t *testing.T, p *SessionPool, key string) *upstream {
	t.Helper()
	conn, remote := net.Pipe()
	t.Cleanup(func() { remote.Close() })
	u := &upstream{pool: p, key: key, telnetSession: newSession(conn, logger)}
	p.streams[key] = u
	return u
}

func poolSize(p *SessionPool) int {
	p.RLock()
	defer p.RUnlock()
	return len(p.streams)
}

func TestLoginConnectedUpstreamWrongPassword(t *testing.T) {
	p := newTestPool(t, "unused:1")
	u := addConnected(t, p, "game")

	_, err := login(t, p, `{"name":"game","password":"wrong"}`)
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

	d, err := login(t, p, `{"name":"game","password":"secret"}`)
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

func TestLoginUnknownUpstream(t *testing.T) {
	p := newTestPool(t, "unused:1")

	_, err := login(t, p, `{"name":"nope","password":"secret"}`)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("got %v", err)
	}
	if poolSize(p) != 0 {
		t.Errorf("pool size = %d", poolSize(p))
	}
}

func TestLoginUnconnectedUpstreamWrongPassword(t *testing.T) {
	p := newTestPool(t, "unused:1")

	_, err := login(t, p, `{"name":"game","password":"wrong"}`)
	if !errors.Is(err, bcrypt.ErrMismatchedHashAndPassword) {
		t.Fatalf("got %v", err)
	}
	if poolSize(p) != 0 {
		t.Errorf("pool size = %d", poolSize(p))
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

	_, err = login(t, p, `{"name":"game","password":"secret"}`)
	if err == nil || !strings.Contains(err.Error(), "error connecting") {
		t.Fatalf("got %v", err)
	}
	if poolSize(p) != 0 {
		t.Errorf("pool size = %d", poolSize(p))
	}
}

func TestLoginBadOptionRemovesUpstream(t *testing.T) {
	p := newTestPool(t, "unused:1")

	_, err := login(t, p, `{"name":"game","password":"secret","options":{"always_allow_charset":"maybe"}}`)
	if err == nil {
		t.Fatal("expected error for bad option value")
	}
	if poolSize(p) != 0 {
		t.Errorf("pool size = %d", poolSize(p))
	}
}

func TestLoginConnectsNewUpstream(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	p := newTestPool(t, l.Addr().String())

	received := make(chan string, 1)
	go func() {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		r := bufio.NewReader(conn)
		for {
			line, err := r.ReadString('\n')
			if strings.Contains(line, "connect me secret") || err != nil {
				received <- line
				return
			}
		}
	}()

	if _, err := login(t, p, `{"name":"game","password":"secret"}`); err != nil {
		t.Fatal(err)
	}

	select {
	case line := <-received:
		if !strings.Contains(line, "connect me secret") {
			t.Errorf("upstream received %q", line)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("upstream never received connect script")
	}

	p.RLock()
	u := p.streams["game"]
	p.RUnlock()
	if u == nil || !u.IsConnected() {
		t.Error("upstream is not connected")
	}
}

func TestCloseAllAndReopenWithUnconnectedUpstream(t *testing.T) {
	p := newTestPool(t, "unused:1")
	p.upstreamForKey("connecting")

	p.ReopenHistories()
	p.CloseAll()
}
