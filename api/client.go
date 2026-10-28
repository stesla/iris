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

type Client struct {
	BaseURL string
	HTTP    *http.Client
}

func NewClient(baseURL string) *Client {
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		HTTP:    http.DefaultClient,
	}
}

func (c *Client) ListSessions(ctx context.Context) ([]Session, error) {
	var resp ListSessionsResponse
	err := c.do(ctx, http.MethodGet, "/sessions", nil, nil, &resp)
	return resp.Sessions, err
}

func (c *Client) ListUpstreams(ctx context.Context) ([]Upstream, error) {
	var resp ListUpstreamsResponse
	err := c.do(ctx, http.MethodGet, "/upstreams", nil, nil, &resp)
	return resp.Upstreams, err
}

func (c *Client) AddUpstream(ctx context.Context, r *AddUpstreamRequest) error {
	return c.do(ctx, http.MethodPost, "/upstreams", nil, r, nil)
}

func (c *Client) EditUpstream(ctx context.Context, name, password string, r *EditUpstreamRequest) error {
	return c.do(ctx, http.MethodPatch, upstreamPath(name), &auth{name, password}, r, nil)
}

func (c *Client) DeleteUpstream(ctx context.Context, name, password string) error {
	return c.do(ctx, http.MethodDelete, upstreamPath(name), &auth{name, password}, nil, nil)
}

func upstreamPath(name string) string {
	return "/upstreams/" + url.PathEscape(name)
}

type auth struct {
	name, password string
}

func (c *Client) do(ctx context.Context, method, path string, a *auth, in, out any) error {
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
	if a != nil {
		req.SetBasicAuth(a.name, a.password)
	}

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
