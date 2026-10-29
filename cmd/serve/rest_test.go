package serve

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"golang.org/x/crypto/bcrypt"

	"github.com/stesla/iris/api"
)

const testSchema = `CREATE TABLE upstreams (
	name     TEXT PRIMARY KEY,
	address  TEXT NOT NULL,
	login    TEXT NOT NULL,
	bcrypt   TEXT NOT NULL,
	script   TEXT
)`

func newTestAPI(t *testing.T) (*api.Client, *apiServer, *httptest.Server) {
	t.Helper()
	logger = zerolog.Nop()

	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(testSchema); err != nil {
		t.Fatal(err)
	}

	srv := &apiServer{db: db, sessions: NewSessionPool(db, logger)}
	ts := httptest.NewServer(srv.handler())
	t.Cleanup(ts.Close)
	return api.NewClient(ts.URL), srv, ts
}

func addTestUpstream(t *testing.T, c *api.Client, name, password string) {
	t.Helper()
	err := c.AddUpstream(context.Background(), &api.AddUpstreamRequest{
		Name: name, Address: "example.com:4201", Login: "login-" + name, Password: password,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func wantStatus(t *testing.T, err error, status int) {
	t.Helper()
	var apiErr *api.Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("want status %d, got %v", status, err)
	}
	if apiErr.StatusCode != status {
		t.Fatalf("want status %d, got %d (%s)", status, apiErr.StatusCode, apiErr.Message)
	}
}

func TestListUpstreamsEmpty(t *testing.T) {
	_, _, ts := newTestAPI(t)
	resp, err := http.Get(ts.URL + "/upstreams")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(body)); got != `{"upstreams":[]}` {
		t.Errorf("body = %s", got)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("content-type = %q", ct)
	}
}

func TestAddAndListUpstreams(t *testing.T) {
	c, srv, _ := newTestAPI(t)
	ctx := context.Background()
	addTestUpstream(t, c, "b", "secret")
	script := "connect %LOGIN%"
	err := c.AddUpstream(ctx, &api.AddUpstreamRequest{
		Name: "a", Address: "example.org:23", Login: "me", Password: "pw", Script: &script,
	})
	if err != nil {
		t.Fatal(err)
	}

	upstreams, err := c.ListUpstreams(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []api.Upstream{
		{Name: "a", Address: "example.org:23", Login: "me"},
		{Name: "b", Address: "example.com:4201", Login: "login-b"},
	}
	if len(upstreams) != 2 || upstreams[0] != want[0] || upstreams[1] != want[1] {
		t.Errorf("got %+v", upstreams)
	}

	var gotScript sql.NullString
	srv.db.QueryRow("SELECT script FROM upstreams WHERE name='a'").Scan(&gotScript)
	if gotScript.String != script {
		t.Errorf("script = %v", gotScript)
	}
	srv.db.QueryRow("SELECT script FROM upstreams WHERE name='b'").Scan(&gotScript)
	if gotScript.Valid {
		t.Errorf("script for b should be NULL, got %q", gotScript.String)
	}
}

func TestAddUpstreamErrors(t *testing.T) {
	c, _, ts := newTestAPI(t)
	addTestUpstream(t, c, "foo", "secret")

	err := c.AddUpstream(context.Background(), &api.AddUpstreamRequest{
		Name: "foo", Address: "x:1", Login: "l", Password: "p",
	})
	wantStatus(t, err, http.StatusConflict)

	err = c.AddUpstream(context.Background(), &api.AddUpstreamRequest{Name: "bar", Address: "x:1", Login: "l"})
	wantStatus(t, err, http.StatusBadRequest)

	err = c.AddUpstream(context.Background(), &api.AddUpstreamRequest{
		Name: "bar", Address: "x:1", Login: "l", Password: strings.Repeat("x", 73),
	})
	wantStatus(t, err, http.StatusBadRequest)

	for _, body := range []string{
		``,
		`not json`,
		`{"name":"bar","address":"x:1","login":"l","password":"p","pasword":"typo"}`,
		`{"name":"bar","address":"x:1","login":"l","password":"p"} {}`,
	} {
		resp, err := http.Post(ts.URL+"/upstreams", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("body %q: status %d", body, resp.StatusCode)
		}
	}
}

func TestEditUpstream(t *testing.T) {
	c, srv, _ := newTestAPI(t)
	ctx := context.Background()
	addTestUpstream(t, c, "foo", "secret")

	newName, newPassword, addr := "bar", "hunter2", "example.org:23"
	err := c.EditUpstream(ctx, "foo", "secret", &api.EditUpstreamRequest{
		NewName: &newName, NewPassword: &newPassword, Address: &addr,
	})
	if err != nil {
		t.Fatal(err)
	}

	upstreams, err := c.ListUpstreams(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := api.Upstream{Name: "bar", Address: addr, Login: "login-foo"}
	if len(upstreams) != 1 || upstreams[0] != want {
		t.Errorf("got %+v", upstreams)
	}

	var hash string
	srv.db.QueryRow("SELECT bcrypt FROM upstreams WHERE name='bar'").Scan(&hash)
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(newPassword)) != nil {
		t.Error("password was not updated")
	}

	if err := c.EditUpstream(ctx, "bar", newPassword, &api.EditUpstreamRequest{}); err != nil {
		t.Errorf("empty edit: %v", err)
	}
}

func TestEditUpstreamErrors(t *testing.T) {
	c, _, ts := newTestAPI(t)
	ctx := context.Background()
	addTestUpstream(t, c, "foo", "secret")
	addTestUpstream(t, c, "bar", "secret")
	login := "x"

	wantStatus(t, c.EditUpstream(ctx, "foo", "wrong", &api.EditUpstreamRequest{Login: &login}), http.StatusUnauthorized)
	wantStatus(t, c.EditUpstream(ctx, "nope", "secret", &api.EditUpstreamRequest{Login: &login}), http.StatusNotFound)

	taken := "bar"
	wantStatus(t, c.EditUpstream(ctx, "foo", "secret", &api.EditUpstreamRequest{NewName: &taken}), http.StatusConflict)

	empty := ""
	wantStatus(t, c.EditUpstream(ctx, "foo", "secret", &api.EditUpstreamRequest{Login: &empty}), http.StatusBadRequest)

	req, _ := http.NewRequest(http.MethodPatch, ts.URL+"/upstreams/foo", strings.NewReader(`{}`))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized || resp.Header.Get("WWW-Authenticate") == "" {
		t.Errorf("no auth: status %d, WWW-Authenticate %q", resp.StatusCode, resp.Header.Get("WWW-Authenticate"))
	}

	req, _ = http.NewRequest(http.MethodPatch, ts.URL+"/upstreams/foo", strings.NewReader(`{}`))
	req.SetBasicAuth("bar", "secret")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("mismatched name: status %d", resp.StatusCode)
	}
}

func TestDeleteUpstream(t *testing.T) {
	c, _, _ := newTestAPI(t)
	ctx := context.Background()
	addTestUpstream(t, c, "foo", "secret")

	wantStatus(t, c.DeleteUpstream(ctx, "foo", "wrong"), http.StatusUnauthorized)
	wantStatus(t, c.DeleteUpstream(ctx, "nope", "secret"), http.StatusNotFound)

	if err := c.DeleteUpstream(ctx, "foo", "secret"); err != nil {
		t.Fatal(err)
	}
	upstreams, err := c.ListUpstreams(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(upstreams) != 0 {
		t.Errorf("got %+v", upstreams)
	}
}

func TestUpstreamNameWithSpaces(t *testing.T) {
	c, _, _ := newTestAPI(t)
	addTestUpstream(t, c, "my game/alt", "secret")
	if err := c.DeleteUpstream(context.Background(), "my game/alt", "secret"); err != nil {
		t.Fatal(err)
	}
}

func TestListSessions(t *testing.T) {
	c, srv, _ := newTestAPI(t)
	started := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	srv.sessions.streams["b"] = &upstream{key: "b", telnetSession: &telnetSession{startedAt: started}}
	srv.sessions.streams["a"] = &upstream{key: "a", telnetSession: &telnetSession{startedAt: started.Add(time.Hour)}}
	srv.sessions.streams["connecting"] = &upstream{key: "connecting"}

	sessions, err := c.ListSessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 2 ||
		sessions[0].UpstreamName != "a" || !sessions[0].TimeStarted.Equal(started.Add(time.Hour)) ||
		sessions[1].UpstreamName != "b" || !sessions[1].TimeStarted.Equal(started) {
		t.Errorf("got %+v", sessions)
	}
}

func TestMethodNotAllowed(t *testing.T) {
	_, _, ts := newTestAPI(t)
	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/upstreams", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("status %d", resp.StatusCode)
	}
}
