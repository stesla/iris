package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// Error is returned by Client when the server responds with a non-2xx status.
type Error struct {
	StatusCode int
	Message    string
}

func (e *Error) Error() string {
	if e.Message == "" {
		return http.StatusText(e.StatusCode)
	}
	return e.Message
}

// DefaultUsername is sent as the basic auth username. The server ignores it
// until Iris supports multiple users.
const DefaultUsername = "iris"

type Client struct {
	BaseURL  string
	HTTP     *http.Client
	Username string
	Password string
}

// NewClient returns a client that authenticates every request with the Iris
// password.
func NewClient(baseURL, password string) *Client {
	return &Client{
		BaseURL:  strings.TrimRight(baseURL, "/"),
		HTTP:     http.DefaultClient,
		Username: DefaultUsername,
		Password: password,
	}
}

func (c *Client) ListSessions(ctx context.Context) ([]Session, error) {
	var resp ListSessionsResponse
	err := c.do(ctx, http.MethodGet, "/sessions", nil, &resp)
	return resp.Sessions, err
}

func (c *Client) ListUpstreams(ctx context.Context) ([]Upstream, error) {
	var resp ListUpstreamsResponse
	err := c.do(ctx, http.MethodGet, "/upstreams", nil, &resp)
	return resp.Upstreams, err
}

func (c *Client) AddUpstream(ctx context.Context, r *AddUpstreamRequest) error {
	return c.do(ctx, http.MethodPost, "/upstreams", r, nil)
}

func (c *Client) EditUpstream(ctx context.Context, name string, r *EditUpstreamRequest) error {
	return c.do(ctx, http.MethodPatch, upstreamPath(name), r, nil)
}

func (c *Client) DeleteUpstream(ctx context.Context, name string) error {
	return c.do(ctx, http.MethodDelete, upstreamPath(name), nil, nil)
}

func upstreamPath(name string) string {
	return "/upstreams/" + url.PathEscape(name)
}

func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	req.SetBasicAuth(c.Username, c.Password)

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var e ErrorResponse
		json.NewDecoder(resp.Body).Decode(&e)
		return &Error{StatusCode: resp.StatusCode, Message: e.Error}
	}

	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return fmt.Errorf("decoding response: %w", err)
		}
	}
	return nil
}
