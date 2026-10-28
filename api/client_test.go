package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
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
	return NewClient(srv.URL + "/"), rec
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
	if rec.hasAuth {
		t.Error("add should not send basic auth")
	}
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
	err := c.EditUpstream(context.Background(), "my upstream", "secret", &EditUpstreamRequest{Address: &addr})
	if err != nil {
		t.Fatal(err)
	}
	if rec.method != http.MethodPatch || rec.path != "/upstreams/my%20upstream" {
		t.Errorf("got %s %s", rec.method, rec.path)
	}
	if rec.user != "my upstream" || rec.pass != "secret" {
		t.Errorf("basic auth = %q:%q", rec.user, rec.pass)
	}
	if want := `{"address":"example.org:23"}`; rec.body != want {
		t.Errorf("body = %s", rec.body)
	}
}

func TestDeleteUpstream(t *testing.T) {
	c, rec := newTestServer(t, http.StatusNoContent, "")

	if err := c.DeleteUpstream(context.Background(), "foo", "secret"); err != nil {
		t.Fatal(err)
	}
	if rec.method != http.MethodDelete || rec.path != "/upstreams/foo" {
		t.Errorf("got %s %s", rec.method, rec.path)
	}
	if rec.user != "foo" || rec.pass != "secret" {
		t.Errorf("basic auth = %q:%q", rec.user, rec.pass)
	}
	if rec.body != "" {
		t.Errorf("body = %s", rec.body)
	}
}

func TestErrorResponse(t *testing.T) {
	c, _ := newTestServer(t, http.StatusUnauthorized, `{"error":"bad password"}`)

	err := c.DeleteUpstream(context.Background(), "foo", "wrong")
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
	// round-trip check that omitted optionals stay nil
	var r EditUpstreamRequest
	if err := json.Unmarshal([]byte(`{"login":"x"}`), &r); err != nil {
		t.Fatal(err)
	}
	if r.Login == nil || r.NewName != nil || r.Script != nil {
		t.Errorf("got %+v", r)
	}
}
