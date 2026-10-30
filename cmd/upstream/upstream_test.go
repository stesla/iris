package upstream

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
)

type request struct {
	method, path string
	user, pass   string
	body         string
}

// newTestServer starts a fake management API that records each request and
// answers with status and body.
func newTestServer(t *testing.T, status int, body string) *[]request {
	t.Helper()
	var requests []request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, _ := r.BasicAuth()
		b, _ := io.ReadAll(r.Body)
		requests = append(requests, request{r.Method, r.URL.EscapedPath(), user, pass, string(b)})
		w.WriteHeader(status)
		io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)

	viper.Set("api.client.url", srv.URL)
	viper.Set("api.client.password", "iris-secret")
	t.Cleanup(func() {
		viper.Set("api.client.url", "")
		viper.Set("api.client.password", "")
	})
	return &requests
}

// setStdin replaces standard input with a pipe holding input, which is never
// a terminal.
func setStdin(t *testing.T, input string) *os.File {
	t.Helper()
	r, w, err := os.Pipe()
	require.NoError(t, err)
	_, err = io.WriteString(w, input)
	require.NoError(t, err)
	require.NoError(t, w.Close())
	old := os.Stdin
	os.Stdin = r
	t.Cleanup(func() {
		os.Stdin = old
		r.Close()
	})
	return r
}

// requireUnread checks that nothing has been read from stdin yet.
func requireUnread(t *testing.T, stdin *os.File, want string) {
	t.Helper()
	got, err := io.ReadAll(stdin)
	require.NoError(t, err)
	require.Equal(t, want, string(got))
}

// run runs `iris upstream` with args, on a fresh command tree so that no flag
// state carries over between runs.
func run(t *testing.T, args ...string) error {
	t.Helper()
	cmd := newCommand()
	cmd.SetArgs(args)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	return cmd.Execute()
}

func TestAddWithPassword(t *testing.T) {
	requests := newTestServer(t, http.StatusCreated, "")
	setStdin(t, "")

	require.NoError(t, run(t, "add", "shang.beth", "-p", "game-secret", "-a", "example.com:4201", "-l", "beth"))
	require.Len(t, *requests, 1)
	r := (*requests)[0]
	require.Equal(t, "POST", r.method)
	require.Equal(t, "/upstreams", r.path)
	require.Equal(t, "iris", r.user)
	require.Equal(t, "iris-secret", r.pass)
	require.JSONEq(t, `{"name":"shang.beth","address":"example.com:4201","login":"beth","password":"game-secret"}`, r.body)
}

func TestAddPasswordFromStdin(t *testing.T) {
	requests := newTestServer(t, http.StatusCreated, "")
	setStdin(t, "piped-secret\n")

	require.NoError(t, run(t, "add", "game", "-p", "-", "-a", "example.com:4201", "-l", "me", "-s", "connect %LOGIN%"))
	require.Len(t, *requests, 1)
	require.JSONEq(t,
		`{"name":"game","address":"example.com:4201","login":"me","password":"piped-secret","script":"connect %LOGIN%"}`,
		(*requests)[0].body)
}

func TestAddEmptyPassword(t *testing.T) {
	requests := newTestServer(t, http.StatusCreated, "")
	setStdin(t, "\n")

	require.ErrorContains(t, run(t, "add", "game", "-p", "-", "-a", "example.com:4201", "-l", "me"), "upstream password cannot be empty")
	require.Empty(t, *requests)
}

func TestAddRequiresPassword(t *testing.T) {
	requests := newTestServer(t, http.StatusCreated, "")
	stdin := setStdin(t, "piped-secret\n")

	require.ErrorContains(t, run(t, "add", "game", "-a", "example.com:4201", "-l", "me"), "password is required")
	require.Error(t, run(t, "add", "game", "game-secret", "-a", "example.com:4201", "-l", "me"))
	require.Empty(t, *requests)
	requireUnread(t, stdin, "piped-secret\n")
}

// TestValidateBeforePrompt checks that a request that would fail is rejected
// before the Iris or upstream password is asked for.
func TestValidateBeforePrompt(t *testing.T) {
	for _, args := range [][]string{
		{"add", "game", "-p", "-", "-l", "me"},
		{"add", "game", "-p", "-", "-a", "example.com:4201"},
		{"add", "bad/name", "-p", "-", "-a", "example.com:4201", "-l", "me"},
		{"edit", "game", "-p", "-", "-n", ".hidden"},
	} {
		requests := newTestServer(t, http.StatusNoContent, "")
		// with no Iris password set, newClient would fail with a different error
		viper.Set("api.client.password", "")
		stdin := setStdin(t, "piped-secret\n")

		err := run(t, args...)
		require.Error(t, err, "%v", args)
		require.NotContains(t, err.Error(), "api.client.password", "%v", args)
		require.Empty(t, *requests, "%v", args)
		requireUnread(t, stdin, "piped-secret\n")
	}
}

func TestEdit(t *testing.T) {
	requests := newTestServer(t, http.StatusNoContent, "")
	setStdin(t, "")

	require.NoError(t, run(t, "edit", "game", "-n", "renamed", "-a", "example.org:23", "-l", "other"))
	require.Len(t, *requests, 1)
	r := (*requests)[0]
	require.Equal(t, "PATCH", r.method)
	require.Equal(t, "/upstreams/game", r.path)
	require.Equal(t, "iris-secret", r.pass)
	// no -p, so the password is left alone
	require.JSONEq(t, `{"new_name":"renamed","address":"example.org:23","login":"other"}`, r.body)
}

func TestEditPassword(t *testing.T) {
	requests := newTestServer(t, http.StatusNoContent, "")
	setStdin(t, "")

	require.NoError(t, run(t, "edit", "game", "-p", "literal"))
	require.JSONEq(t, `{"password":"literal"}`, (*requests)[0].body)
}

func TestEditPasswordFromStdin(t *testing.T) {
	for _, flag := range [][]string{{"-p", "-"}, {"--password=-"}} {
		requests := newTestServer(t, http.StatusNoContent, "")
		setStdin(t, "piped-secret\r\n")

		require.NoError(t, run(t, append([]string{"edit", "game"}, flag...)...))
		require.Len(t, *requests, 1, "%v", flag)
		require.JSONEq(t, `{"password":"piped-secret"}`, (*requests)[0].body, "%v", flag)
	}
}

func TestEditEmptyPasswordFromStdin(t *testing.T) {
	requests := newTestServer(t, http.StatusNoContent, "")
	setStdin(t, "")

	require.ErrorContains(t, run(t, "edit", "game", "-p", "-"), "upstream password cannot be empty")
	require.Empty(t, *requests)
}

func TestDelete(t *testing.T) {
	requests := newTestServer(t, http.StatusNoContent, "")
	setStdin(t, "")

	require.NoError(t, run(t, "delete", "shang.beth"))
	require.Len(t, *requests, 1)
	r := (*requests)[0]
	require.Equal(t, "DELETE", r.method)
	require.Equal(t, "/upstreams/shang.beth", r.path)
	require.Equal(t, "iris-secret", r.pass)
}

func TestOldPasswordArgumentsRejected(t *testing.T) {
	requests := newTestServer(t, http.StatusNoContent, "")
	setStdin(t, "")

	require.Error(t, run(t, "delete", "game", "game-secret"))
	require.Error(t, run(t, "edit", "game", "game-secret"))
	require.Empty(t, *requests)
}

func TestFlagsRejected(t *testing.T) {
	for _, args := range [][]string{
		{"delete", "game", "-a", "x"},
		{"delete", "game", "-l", "x"},
		{"delete", "game", "-n", "x"},
		{"delete", "game", "-p", "x"},
		{"delete", "game", "-s", "x"},
		{"list", "-a", "x"},
		{"list", "-l", "me"},
		{"list", "-n", "x"},
		{"list", "-p", "x"},
		{"list", "-s", "x"},
		{"add", "game", "-n", "x", "-a", "example.com:4201", "-l", "me", "-p", "game-secret"},
		// cobra hands a flag before the subcommand to the subcommand
		{"-a", "x", "delete", "game"},
	} {
		requests := newTestServer(t, http.StatusNoContent, "")
		setStdin(t, "")

		require.Error(t, run(t, args...), "%v", args)
		require.Empty(t, *requests, "%v", args)
	}
}

func TestList(t *testing.T) {
	newTestServer(t, http.StatusOK,
		`{"upstreams":[{"name":"shang.beth","address":"example.com:4201","login":"beth"}]}`)
	setStdin(t, "")

	r, w, err := os.Pipe()
	require.NoError(t, err)
	old := os.Stdout
	os.Stdout = w
	err = run(t, "list")
	os.Stdout = old
	w.Close()
	require.NoError(t, err)

	out, err := io.ReadAll(r)
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	require.Len(t, lines, 2)
	require.Equal(t, []string{"NAME", "ADDRESS", "LOGIN"}, strings.Fields(lines[0]))
	require.Equal(t, []string{"shang.beth", "example.com:4201", "beth"}, strings.Fields(lines[1]))
}

func TestNoIrisPassword(t *testing.T) {
	requests := newTestServer(t, http.StatusOK, `{"upstreams":[]}`)
	viper.Set("api.client.password", "")
	setStdin(t, "")

	err := run(t, "list")
	require.ErrorContains(t, err, "api.client.password")
	require.ErrorContains(t, err, "IRIS_API_CLIENT_PASSWORD")
	require.Empty(t, *requests)
}

func TestServerError(t *testing.T) {
	newTestServer(t, http.StatusUnauthorized, `{"error":"incorrect Iris password"}`)
	setStdin(t, "")

	require.EqualError(t, run(t, "list"), "incorrect Iris password")
}
