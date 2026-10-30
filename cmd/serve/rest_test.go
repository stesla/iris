package serve

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"golang.org/x/crypto/bcrypt"

	"github.com/stesla/iris/api"
	"github.com/stesla/iris/internal/database/dbtest"
	"github.com/stesla/iris/internal/secret"
)

const testIrisPassword = "iris-secret"

// authTransport adds basic auth to every request, for tests that don't use
// api.Client.
type authTransport struct {
	username, password string
}

func (t *authTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.SetBasicAuth(t.username, t.password)
	return http.DefaultTransport.RoundTrip(r)
}

var authClient = &http.Client{Transport: &authTransport{"iris", testIrisPassword}}

func newTestBox(t *testing.T) *secret.Box {
	t.Helper()
	box, err := secret.NewBox(bytes.Repeat([]byte{7}, secret.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	return box
}

// newTestDB returns a migrated database with the Iris password set to
// testIrisPassword.
func newTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db := dbtest.New(t)
	setTestPassword(t, db)
	return db
}

// setTestPassword sets the Iris password in db to testIrisPassword.
func setTestPassword(t *testing.T, db *sql.DB) {
	t.Helper()
	// MinCost rather than auth.SetPassword's DefaultCost, since every request
	// and login checks it
	hash, err := bcrypt.GenerateFromPassword([]byte(testIrisPassword), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO iris_auth (id, password_hash) VALUES (1, ?)", hash); err != nil {
		t.Fatal(err)
	}
}

func newTestAPI(t *testing.T) (*api.Client, *apiServer, *httptest.Server) {
	t.Helper()
	logger = zerolog.Nop()

	db := newTestDB(t)
	box := newTestBox(t)
	srv := &apiServer{db: db, sessions: NewSessionPool(db, box, logger), box: box}
	ts := httptest.NewServer(srv.handler())
	t.Cleanup(ts.Close)
	return api.NewClient(ts.URL, testIrisPassword), srv, ts
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

// storedPassword returns the decrypted password of the named upstream.
func storedPassword(t *testing.T, srv *apiServer, name string) string {
	t.Helper()
	var id int64
	var sealed []byte
	err := srv.db.QueryRow("SELECT id, password FROM upstreams WHERE name=?", name).Scan(&id, &sealed)
	if err != nil {
		t.Fatal(err)
	}
	plaintext, err := srv.box.Open(id, sealed)
	if err != nil {
		t.Fatal(err)
	}
	return string(plaintext)
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

func TestRequiresIrisPassword(t *testing.T) {
	c, _, ts := newTestAPI(t)
	addTestUpstream(t, c, "foo", "secret")

	for _, tc := range []struct {
		method, path, body string
	}{
		{http.MethodGet, "/sessions", ""},
		{http.MethodGet, "/upstreams", ""},
		{http.MethodPost, "/upstreams", `{"name":"bar","address":"x:1","login":"l","password":"p"}`},
		{http.MethodPatch, "/upstreams/foo", `{"login":"x"}`},
		{http.MethodDelete, "/upstreams/foo", ""},
		{http.MethodPut, "/upstreams", ""},
	} {
		for _, creds := range []*authTransport{
			nil,
			{"iris", "wrong"},
			{"iris", ""},
			// the upstream's own password no longer works
			{"foo", "secret"},
		} {
			req, _ := http.NewRequest(tc.method, ts.URL+tc.path, strings.NewReader(tc.body))
			if creds != nil {
				req.SetBasicAuth(creds.username, creds.password)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusUnauthorized || resp.Header.Get("WWW-Authenticate") == "" {
				t.Errorf("%s %s with %+v: status %d, WWW-Authenticate %q",
					tc.method, tc.path, creds, resp.StatusCode, resp.Header.Get("WWW-Authenticate"))
			}
		}
	}

	// nothing was changed by the rejected requests
	upstreams, err := c.ListUpstreams(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(upstreams) != 1 || upstreams[0].Login != "login-foo" {
		t.Errorf("got %+v", upstreams)
	}
}

func TestIrisPasswordIgnoresUsername(t *testing.T) {
	_, _, ts := newTestAPI(t)
	for _, username := range []string{"iris", "", "anyone"} {
		req, _ := http.NewRequest(http.MethodGet, ts.URL+"/upstreams", nil)
		req.SetBasicAuth(username, testIrisPassword)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("username %q: status %d", username, resp.StatusCode)
		}
	}
}

func TestIrisPasswordNotSet(t *testing.T) {
	c, srv, _ := newTestAPI(t)
	if _, err := srv.db.Exec("DELETE FROM iris_auth"); err != nil {
		t.Fatal(err)
	}
	_, err := c.ListUpstreams(context.Background())
	wantStatus(t, err, http.StatusServiceUnavailable)
}

func TestListUpstreamsEmpty(t *testing.T) {
	_, _, ts := newTestAPI(t)
	resp, err := authClient.Get(ts.URL + "/upstreams")
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

func TestAddUpstreamEmptyScriptStoresNull(t *testing.T) {
	c, srv, _ := newTestAPI(t)
	empty := ""
	err := c.AddUpstream(context.Background(), &api.AddUpstreamRequest{
		Name: "a", Address: "example.org:23", Login: "me", Password: "pw", Script: &empty,
	})
	if err != nil {
		t.Fatal(err)
	}

	var got sql.NullString
	srv.db.QueryRow("SELECT script FROM upstreams WHERE name='a'").Scan(&got)
	if got.Valid {
		t.Errorf("script should be NULL, got %q", got.String)
	}
}

func TestEditUpstreamScript(t *testing.T) {
	c, srv, _ := newTestAPI(t)
	ctx := context.Background()
	addTestUpstream(t, c, "a", "secret")

	stored := func() sql.NullString {
		t.Helper()
		var got sql.NullString
		if err := srv.db.QueryRow("SELECT script FROM upstreams WHERE name='a'").Scan(&got); err != nil {
			t.Fatal(err)
		}
		return got
	}

	script := "connect %LOGIN%"
	if err := c.EditUpstream(ctx, "a", &api.EditUpstreamRequest{Script: &script}); err != nil {
		t.Fatal(err)
	}
	if got := stored(); got.String != script {
		t.Errorf("script = %v", got)
	}

	// leaving the script out doesn't change it
	login := "other"
	if err := c.EditUpstream(ctx, "a", &api.EditUpstreamRequest{Login: &login}); err != nil {
		t.Fatal(err)
	}
	if got := stored(); got.String != script {
		t.Errorf("script = %v", got)
	}

	empty := ""
	if err := c.EditUpstream(ctx, "a", &api.EditUpstreamRequest{Script: &empty}); err != nil {
		t.Fatal(err)
	}
	if got := stored(); got.Valid {
		t.Errorf("script should be NULL, got %q", got.String)
	}
}

func TestAddUpstreamEncryptsPassword(t *testing.T) {
	c, srv, _ := newTestAPI(t)
	addTestUpstream(t, c, "a", "first-secret")
	addTestUpstream(t, c, "b", "second-secret")

	rows, err := srv.db.Query("SELECT password FROM upstreams")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var sealed []byte
		if err := rows.Scan(&sealed); err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(sealed, []byte("secret")) {
			t.Errorf("password stored in plaintext: %q", sealed)
		}
	}

	if got := storedPassword(t, srv, "a"); got != "first-secret" {
		t.Errorf("a: password = %q", got)
	}
	if got := storedPassword(t, srv, "b"); got != "second-secret" {
		t.Errorf("b: password = %q", got)
	}
}

func TestAddUpstreamErrors(t *testing.T) {
	c, srv, ts := newTestAPI(t)
	addTestUpstream(t, c, "foo", "secret")

	err := c.AddUpstream(context.Background(), &api.AddUpstreamRequest{
		Name: "foo", Address: "x:1", Login: "l", Password: "p",
	})
	wantStatus(t, err, http.StatusConflict)
	if got := storedPassword(t, srv, "foo"); got != "secret" {
		t.Errorf("conflicting add changed password to %q", got)
	}

	err = c.AddUpstream(context.Background(), &api.AddUpstreamRequest{Name: "bar", Address: "x:1", Login: "l"})
	wantStatus(t, err, http.StatusBadRequest)

	for _, body := range []string{
		``,
		`not json`,
		`{"name":"bar","address":"x:1","login":"l","password":"p","pasword":"typo"}`,
		`{"name":"bar","address":"x:1","login":"l","password":"p"} {}`,
	} {
		resp, err := authClient.Post(ts.URL+"/upstreams", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("body %q: status %d", body, resp.StatusCode)
		}
	}
}

func TestAddUpstreamLongPassword(t *testing.T) {
	// upstream passwords aren't bcrypt hashes anymore, so bcrypt's 72-byte
	// limit doesn't apply
	c, srv, _ := newTestAPI(t)
	long := strings.Repeat("x", 200)
	addTestUpstream(t, c, "foo", long)
	if got := storedPassword(t, srv, "foo"); got != long {
		t.Errorf("password = %q", got)
	}
}

func TestEditUpstream(t *testing.T) {
	c, srv, _ := newTestAPI(t)
	ctx := context.Background()
	addTestUpstream(t, c, "foo", "secret")

	newName, newPassword, addr := "bar", "hunter2", "example.org:23"
	err := c.EditUpstream(ctx, "foo", &api.EditUpstreamRequest{
		NewName: &newName, Password: &newPassword, Address: &addr,
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
	if got := storedPassword(t, srv, "bar"); got != newPassword {
		t.Errorf("password = %q", got)
	}

	if err := c.EditUpstream(ctx, "bar", &api.EditUpstreamRequest{}); err != nil {
		t.Errorf("empty edit: %v", err)
	}
}

func TestEditUpstreamRenameKeepsPassword(t *testing.T) {
	c, srv, _ := newTestAPI(t)
	addTestUpstream(t, c, "foo", "secret")

	newName := "bar"
	if err := c.EditUpstream(context.Background(), "foo", &api.EditUpstreamRequest{NewName: &newName}); err != nil {
		t.Fatal(err)
	}
	if got := storedPassword(t, srv, "bar"); got != "secret" {
		t.Errorf("password = %q", got)
	}
}

func TestEditUpstreamErrors(t *testing.T) {
	c, srv, _ := newTestAPI(t)
	ctx := context.Background()
	addTestUpstream(t, c, "foo", "secret")
	addTestUpstream(t, c, "bar", "secret")
	login := "x"

	wantStatus(t, c.EditUpstream(ctx, "nope", &api.EditUpstreamRequest{Login: &login}), http.StatusNotFound)
	wantStatus(t, c.EditUpstream(ctx, "nope", &api.EditUpstreamRequest{}), http.StatusNotFound)

	taken, newPassword := "bar", "changed"
	err := c.EditUpstream(ctx, "foo", &api.EditUpstreamRequest{NewName: &taken, Password: &newPassword})
	wantStatus(t, err, http.StatusConflict)
	if got := storedPassword(t, srv, "foo"); got != "secret" {
		t.Errorf("failed edit changed password to %q", got)
	}

	empty := ""
	wantStatus(t, c.EditUpstream(ctx, "foo", &api.EditUpstreamRequest{Login: &empty}), http.StatusBadRequest)
}

func TestDeleteUpstream(t *testing.T) {
	c, _, _ := newTestAPI(t)
	ctx := context.Background()
	addTestUpstream(t, c, "foo", "secret")

	wantStatus(t, c.DeleteUpstream(ctx, "nope"), http.StatusNotFound)

	if err := c.DeleteUpstream(ctx, "foo"); err != nil {
		t.Fatal(err)
	}
	upstreams, err := c.ListUpstreams(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(upstreams) != 0 {
		t.Errorf("got %+v", upstreams)
	}
	wantStatus(t, c.DeleteUpstream(ctx, "foo"), http.StatusNotFound)
}

type closeRecorder struct {
	closed bool
}

func (r *closeRecorder) Write(p []byte) (int, error) { return len(p), nil }
func (r *closeRecorder) Close() error                { r.closed = true; return nil }

func TestDeleteUpstreamClosesSession(t *testing.T) {
	c, srv, _ := newTestAPI(t)
	addTestUpstream(t, c, "foo", "secret")
	addTestUpstream(t, c, "bar", "secret")
	foo := addConnected(t, srv.sessions, "foo")
	bar := addConnected(t, srv.sessions, "bar")
	fooClient, barClient := &closeRecorder{}, &closeRecorder{}
	foo.AddDownstream(fooClient)
	bar.AddDownstream(barClient)

	if err := c.DeleteUpstream(context.Background(), "foo"); err != nil {
		t.Fatal(err)
	}
	if !fooClient.closed {
		t.Error("client of deleted upstream was not disconnected")
	}
	if barClient.closed {
		t.Error("client of other upstream was disconnected")
	}
	srv.sessions.RLock()
	defer srv.sessions.RUnlock()
	if _, found := srv.sessions.streams[foo.id]; found {
		t.Error("deleted upstream is still in the pool")
	}
	if srv.sessions.streams[bar.id] != bar {
		t.Error("other upstream was removed from the pool")
	}
}

func TestInvalidUpstreamNames(t *testing.T) {
	c, _, _ := newTestAPI(t)
	ctx := context.Background()
	addTestUpstream(t, c, "foo", "secret")

	for _, name := range []string{"my game/alt", "../../tmp/evil", ".hidden"} {
		err := c.AddUpstream(ctx, &api.AddUpstreamRequest{Name: name, Address: "x:1", Login: "l", Password: "p"})
		wantStatus(t, err, http.StatusBadRequest)
		wantStatus(t, c.EditUpstream(ctx, "foo", &api.EditUpstreamRequest{NewName: &name}), http.StatusBadRequest)
	}
}

func TestExistingInvalidNameCanBeFixed(t *testing.T) {
	// upstreams added before names were validated can still be renamed to a
	// valid name, or deleted
	c, srv, _ := newTestAPI(t)
	ctx := context.Background()
	for _, name := range []string{"my game/alt", "old name"} {
		if _, err := srv.db.Exec("INSERT INTO upstreams (name, address, login) VALUES (?, 'x:1', 'l')", name); err != nil {
			t.Fatal(err)
		}
	}

	newName := "my-game-alt"
	if err := c.EditUpstream(ctx, "my game/alt", &api.EditUpstreamRequest{NewName: &newName}); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteUpstream(ctx, "old name"); err != nil {
		t.Fatal(err)
	}
	upstreams, err := c.ListUpstreams(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(upstreams) != 1 || upstreams[0].Name != newName {
		t.Errorf("got %+v", upstreams)
	}
}

func TestRenameKeepsSession(t *testing.T) {
	c, srv, _ := newTestAPI(t)
	ctx := context.Background()
	addTestUpstream(t, c, "foo", "secret")
	u := addConnected(t, srv.sessions, "foo")

	newName := "bar"
	if err := c.EditUpstream(ctx, "foo", &api.EditUpstreamRequest{NewName: &newName}); err != nil {
		t.Fatal(err)
	}
	sessions, err := c.ListSessions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].UpstreamName != "bar" {
		t.Errorf("got %+v", sessions)
	}
	if srv.sessions.streams[u.id] != u {
		t.Error("renamed upstream's session was replaced")
	}
}

func TestDeleteThenReuseName(t *testing.T) {
	c, srv, _ := newTestAPI(t)
	ctx := context.Background()
	addTestUpstream(t, c, "foo", "secret")
	old := addConnected(t, srv.sessions, "foo")

	if err := c.DeleteUpstream(ctx, "foo"); err != nil {
		t.Fatal(err)
	}
	addTestUpstream(t, c, "foo", "new-secret")
	if id := upstreamID(t, srv.db, "foo"); id == old.id {
		t.Errorf("new upstream reused id %d", id)
	}
	if poolSize(srv.sessions) != 0 {
		t.Errorf("pool size = %d", poolSize(srv.sessions))
	}
	if got := storedPassword(t, srv, "foo"); got != "new-secret" {
		t.Errorf("password = %q", got)
	}
}

func TestListSessions(t *testing.T) {
	c, srv, _ := newTestAPI(t)
	started := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	srv.sessions.streams[1] = &upstream{id: 1, name: "b", telnetSession: &telnetSession{startedAt: started}}
	srv.sessions.streams[2] = &upstream{id: 2, name: "a", telnetSession: &telnetSession{startedAt: started.Add(time.Hour)}}
	srv.sessions.streams[3] = &upstream{id: 3, name: "connecting"}

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
	resp, err := authClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("status %d", resp.StatusCode)
	}
}

func TestConcurrentRenamesMatchDatabase(t *testing.T) {
	c, srv, _ := newTestAPI(t)
	ctx := context.Background()
	addTestUpstream(t, c, "n0", "secret")
	addConnected(t, srv.sessions, "n0")

	// rename retries until from exists, so the commits land in order
	rename := func(from, to string) {
		for {
			err := c.EditUpstream(ctx, from, &api.EditUpstreamRequest{NewName: &to})
			var apiErr *api.Error
			if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound {
				continue
			}
			if err != nil {
				t.Errorf("%s to %s: %v", from, to, err)
			}
			return
		}
	}

	for round := range 30 {
		a, b, last := fmt.Sprintf("n%d", 2*round), fmt.Sprintf("n%d", 2*round+1), fmt.Sprintf("n%d", 2*round+2)
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); rename(a, b) }()
		go func() { defer wg.Done(); rename(b, last) }()
		wg.Wait()

		var stored string
		if err := srv.db.QueryRow("SELECT name FROM upstreams").Scan(&stored); err != nil {
			t.Fatal(err)
		}
		sessions, err := c.ListSessions(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(sessions) != 1 || sessions[0].UpstreamName != stored {
			t.Fatalf("round %d: database says %q, sessions say %+v", round, stored, sessions)
		}
	}
}
