package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type recorded struct {
	method, path string
	user, pass   string
	hasAuth      bool
	contentType  string
	body         string
}

func newTestServer(t *testing.T, status int, respBody string) (*Client, *recorded) {
	t.Helper()
	rec := &recorded{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.method = r.Method
		rec.path = r.URL.EscapedPath()
		rec.user, rec.pass, rec.hasAuth = r.BasicAuth()
		rec.contentType = r.Header.Get("Content-Type")
		b, _ := io.ReadAll(r.Body)
		rec.body = string(b)
		w.WriteHeader(status)
		io.WriteString(w, respBody)
	}))
	t.Cleanup(srv.Close)
	return NewClient(srv.URL+"/", "iris-secret"), rec
}

// wantAuth checks that the request had basic auth with the Iris password.
func wantAuth(t *testing.T, rec *recorded) {
	t.Helper()
	if !rec.hasAuth || rec.user != DefaultUsername || rec.pass != "iris-secret" {
		t.Errorf("basic auth = %v %q:%q", rec.hasAuth, rec.user, rec.pass)
	}
}

func TestListSessions(t *testing.T) {
	c, rec := newTestServer(t, http.StatusOK,
		`{"sessions":[{"upstream_name":"foo","time_started":"2026-10-28T12:00:00Z"}]}`)

	sessions, err := c.ListSessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rec.method != http.MethodGet || rec.path != "/sessions" {
		t.Errorf("got %s %s", rec.method, rec.path)
	}
	wantAuth(t, rec)
	want := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	if len(sessions) != 1 || sessions[0].UpstreamName != "foo" || !sessions[0].TimeStarted.Equal(want) {
		t.Errorf("got %+v", sessions)
	}
}

func TestListUpstreams(t *testing.T) {
	c, rec := newTestServer(t, http.StatusOK,
		`{"upstreams":[{"name":"foo","address":"example.com:4201","login":"bar"}]}`)

	upstreams, err := c.ListUpstreams(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rec.method != http.MethodGet || rec.path != "/upstreams" {
		t.Errorf("got %s %s", rec.method, rec.path)
	}
	wantAuth(t, rec)
	want := Upstream{Name: "foo", Address: "example.com:4201", Login: "bar"}
	if len(upstreams) != 1 || upstreams[0] != want {
		t.Errorf("got %+v", upstreams)
	}
}

func TestAddUpstream(t *testing.T) {
	c, rec := newTestServer(t, http.StatusCreated, "")

	err := c.AddUpstream(context.Background(), &AddUpstreamRequest{
		Name: "foo", Address: "example.com:4201", Login: "bar", Password: "secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	if rec.method != http.MethodPost || rec.path != "/upstreams" {
		t.Errorf("got %s %s", rec.method, rec.path)
	}
	wantAuth(t, rec)
	if rec.contentType != "application/json" {
		t.Errorf("content-type = %q", rec.contentType)
	}
	want := `{"name":"foo","address":"example.com:4201","login":"bar","password":"secret"}`
	if rec.body != want {
		t.Errorf("body = %s", rec.body)
	}
}

func TestEditUpstream(t *testing.T) {
	c, rec := newTestServer(t, http.StatusNoContent, "")

	addr := "example.org:23"
	password := "hunter2"
	err := c.EditUpstream(context.Background(), "my upstream", &EditUpstreamRequest{Address: &addr, Password: &password})
	if err != nil {
		t.Fatal(err)
	}
	if rec.method != http.MethodPatch || rec.path != "/upstreams/my%20upstream" {
		t.Errorf("got %s %s", rec.method, rec.path)
	}
	wantAuth(t, rec)
	if want := `{"password":"hunter2","address":"example.org:23"}`; rec.body != want {
		t.Errorf("body = %s", rec.body)
	}
}

func TestDeleteUpstream(t *testing.T) {
	c, rec := newTestServer(t, http.StatusNoContent, "")

	if err := c.DeleteUpstream(context.Background(), "foo"); err != nil {
		t.Fatal(err)
	}
	if rec.method != http.MethodDelete || rec.path != "/upstreams/foo" {
		t.Errorf("got %s %s", rec.method, rec.path)
	}
	wantAuth(t, rec)
	if rec.body != "" {
		t.Errorf("body = %s", rec.body)
	}
}

func TestErrorResponse(t *testing.T) {
	c, _ := newTestServer(t, http.StatusUnauthorized, `{"error":"bad password"}`)

	err := c.DeleteUpstream(context.Background(), "foo")
	var apiErr *Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("got %T %v", err, err)
	}
	if apiErr.StatusCode != http.StatusUnauthorized || apiErr.Message != "bad password" {
		t.Errorf("got %+v", apiErr)
	}
}

func TestErrorResponseWithoutBody(t *testing.T) {
	c, _ := newTestServer(t, http.StatusInternalServerError, "")

	_, err := c.ListUpstreams(context.Background())
	if err == nil || err.Error() != "Internal Server Error" {
		t.Errorf("got %v", err)
	}
}

func TestValidate(t *testing.T) {
	empty := ""
	full := AddUpstreamRequest{Name: "n", Address: "a", Login: "l", Password: "p"}
	if err := full.Validate(); err != nil {
		t.Errorf("valid add: %v", err)
	}
	missing := full
	missing.Password = ""
	if err := missing.Validate(); err == nil {
		t.Error("add without password should fail")
	}
	if err := (&EditUpstreamRequest{}).Validate(); err != nil {
		t.Errorf("empty edit: %v", err)
	}
	if err := (&EditUpstreamRequest{NewName: &empty}).Validate(); err == nil {
		t.Error("edit with empty new_name should fail")
	}
	if err := (&EditUpstreamRequest{Password: &empty}).Validate(); err == nil {
		t.Error("edit with empty password should fail")
	}
	for _, name := range []string{"shang.beth", "a", "Game_2-alt", strings.Repeat("x", 64)} {
		if !ValidName(name) {
			t.Errorf("%q should be valid", name)
		}
		add := full
		add.Name = name
		if err := add.Validate(); err != nil {
			t.Errorf("add %q: %v", name, err)
		}
	}
	for _, name := range []string{
		".", "..", ".hidden", "-flag", "_x", "my game", "a/b", "../../etc/x", `a\b`,
		"tab\there", "new\nline", "café", strings.Repeat("x", 65),
	} {
		if ValidName(name) {
			t.Errorf("%q should be invalid", name)
		}
		add := full
		add.Name = name
		if err := add.Validate(); err == nil {
			t.Errorf("add %q should fail", name)
		}
		edit := EditUpstreamRequest{NewName: &name}
		if err := edit.Validate(); err == nil {
			t.Errorf("rename to %q should fail", name)
		}
	}
	// round-trip check that omitted optionals stay nil
	var r EditUpstreamRequest
	if err := json.Unmarshal([]byte(`{"login":"x"}`), &r); err != nil {
		t.Fatal(err)
	}
	if r.Login == nil || r.NewName != nil || r.Script != nil {
		t.Errorf("got %+v", r)
	}
}
