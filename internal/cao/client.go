package cao

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Client struct {
	BeforeLaunch  func(provider, dir string)
	BaseURL       string
	HTTP          *http.Client
	Timeout       time.Duration
	LaunchTimeout time.Duration
	StepMargin    time.Duration
}

func NewClient(baseURL string) *Client {
	return &Client{BaseURL: baseURL, HTTP: &http.Client{}, Timeout: 30 * time.Second, LaunchTimeout: 90 * time.Second, StepMargin: 90 * time.Second}
}

type StepRequest struct {
	SessionName      string  `json:"session_name,omitempty"`
	Provider         string  `json:"provider"`
	Agent            string  `json:"agent"`
	Prompt           string  `json:"prompt"`
	WorkingDirectory string  `json:"working_directory,omitempty"`
	ReuseTerminalID  string  `json:"reuse_terminal_id,omitempty"`
	Description      string  `json:"description,omitempty"`
	Teardown         bool    `json:"teardown"`
	Timeout          float64 `json:"timeout"`
	Model            string  `json:"model,omitempty"`
}

type StepResponse struct {
	TerminalID  string `json:"terminal_id"`
	LastMessage string `json:"last_message"`
	Status      string `json:"status"`
}

type Terminal struct {
	Description  string  `json:"description"`
	TmuxSession  string  `json:"tmux_session"`
	ID           string  `json:"id"`
	Status       string  `json:"status"`
	SessionName  string  `json:"session_name"`
	Provider     string  `json:"provider"`
	AgentProfile string  `json:"agent_profile"`
	Model        *string `json:"model"`
	ModelHonored *bool   `json:"model_honored"`
}

func (c *Client) RunStep(ctx context.Context, req StepRequest) (StepResponse, error) {
	if req.ReuseTerminalID == "" {
		c.beforeLaunch(req.Provider, req.WorkingDirectory)
	}
	var resp StepResponse
	var limit time.Duration
	if req.Timeout > 0 {
		limit = time.Duration(req.Timeout*float64(time.Second)) + c.StepMargin
	}
	err := c.call(ctx, limit, http.MethodPost, "/terminals/run-step", req, &resp)
	return resp, err
}

func (c *Client) Terminal(ctx context.Context, id string) (Terminal, error) {
	var t Terminal
	err := c.call(ctx, c.Timeout, http.MethodGet, "/terminals/"+id, nil, &t)
	return t, err
}

type Profile struct {
	Name     string `json:"name"`
	Loadable bool   `json:"loadable"`
}

func (c *Client) Profiles(ctx context.Context) ([]Profile, error) {
	var out []Profile
	err := c.call(ctx, c.Timeout, http.MethodGet, "/agents/profiles", nil, &out)
	return out, err
}

func (c *Client) DeleteTerminal(ctx context.Context, id string) error {
	return c.call(ctx, c.Timeout, http.MethodDelete, "/terminals/"+id, nil, nil)
}

type Launch struct {
	Profile, Provider, Model, WorkingDirectory string
}

func (l Launch) query() url.Values {
	q := url.Values{"agent_profile": {l.Profile}, "working_directory": {l.WorkingDirectory}}
	if l.Provider != "" {
		q.Set("provider", l.Provider)
	}
	if l.Model != "" {
		q.Set("model", l.Model)
	}
	return q
}

func (c *Client) CreateSession(ctx context.Context, name string, l Launch) (Terminal, error) {
	c.beforeLaunch(l.Provider, l.WorkingDirectory)
	q := l.query()
	q.Set("session_name", name)
	var t Terminal
	err := c.call(ctx, c.LaunchTimeout, http.MethodPost, "/sessions?"+q.Encode(), nil, &t)
	return t, err
}

func (c *Client) AddTerminal(ctx context.Context, session string, l Launch) (Terminal, error) {
	c.beforeLaunch(l.Provider, l.WorkingDirectory)
	var t Terminal
	err := c.call(ctx, c.LaunchTimeout, http.MethodPost, "/sessions/"+url.PathEscape(session)+"/terminals?"+l.query().Encode(), nil, &t)
	return t, err
}

func (c *Client) DeleteSession(ctx context.Context, session string) error {
	return c.call(ctx, c.Timeout, http.MethodDelete, "/sessions/"+url.PathEscape(session), nil, nil)
}

func (c *Client) SetGroup(ctx context.Context, id string, group []string) error {
	return c.call(ctx, c.Timeout, http.MethodPatch, "/terminals/"+id+"/group", map[string]any{"group": group}, nil)
}

func (c *Client) SetMetadata(ctx context.Context, id string, metadata map[string]any) error {
	return c.call(ctx, c.Timeout, http.MethodPatch, "/terminals/"+id+"/metadata", map[string]any{"metadata": metadata}, nil)
}

func (c *Client) SendMessage(ctx context.Context, receiver, sender, message string) error {
	q := url.Values{"sender_id": {sender}, "message": {message}}
	return c.call(ctx, c.Timeout, http.MethodPost, "/terminals/"+receiver+"/inbox/messages?"+q.Encode(), nil, nil)
}

func (c *Client) Output(ctx context.Context, id string) (string, error) {
	var out struct {
		Output string `json:"output"`
	}
	err := c.call(ctx, c.Timeout, http.MethodGet, "/terminals/"+id+"/output?mode=full", nil, &out)
	return out.Output, err
}

func (c *Client) call(ctx context.Context, limit time.Duration, method, path string, body, out any) error {
	route, _, _ := strings.Cut(path, "?")
	parent := ctx
	if limit > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, limit)
		defer cancel()
	}
	requestError := func(err error) error {
		if parent.Err() != nil {
			return parent.Err()
		}
		if ctx.Err() == context.DeadlineExceeded {
			return &TimeoutError{Method: method, Path: route, Limit: limit}
		}
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}
		return fmt.Errorf("%s %s: %w", method, route, err)
	}
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return requestError(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return requestError(err)
	}
	if resp.StatusCode >= 300 {
		herr := &HTTPError{Method: method, Path: route, Status: resp.StatusCode, Body: truncate(string(data), 600)}
		var detail struct {
			Detail struct {
				TerminalID string `json:"terminal_id"`
				Kind       string `json:"kind"`
			} `json:"detail"`
		}
		if json.Unmarshal(data, &detail) == nil {
			herr.TerminalID = detail.Detail.TerminalID
			herr.Kind = detail.Detail.Kind
		}
		return herr
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(data, out)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

type HTTPError struct {
	Method, Path string
	Status       int
	Body         string
	TerminalID   string
	Kind         string
}

func (e *HTTPError) Is(target error) bool {
	return target == context.DeadlineExceeded && e.Method == http.MethodPost &&
		e.Path == "/terminals/run-step" && e.Status == http.StatusGatewayTimeout && e.Kind == "timeout"
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("%s %s: HTTP %d: %s", e.Method, e.Path, e.Status, e.Body)
}

func (c *Client) SessionTerminals(ctx context.Context, name string) ([]Terminal, error) {
	var out struct {
		Terminals []Terminal `json:"terminals"`
	}
	err := c.call(ctx, c.Timeout, http.MethodGet, "/sessions/"+url.PathEscape(name), nil, &out)
	return out.Terminals, err
}

func (c *Client) SetDescription(ctx context.Context, id, description string) error {
	return c.call(ctx, c.Timeout, http.MethodPatch, "/terminals/"+url.PathEscape(id)+"/description", map[string]string{"description": description}, nil)
}

type ProfilePermissions struct {
	Description    string   `json:"description"`
	PermissionMode string   `json:"permissionMode"`
	CodexProfile   string   `json:"codexProfile"`
	AllowedTools   []string `json:"allowedTools"`
}

func (c *Client) ProfilePermissions(ctx context.Context, name string) (ProfilePermissions, error) {
	var out ProfilePermissions
	err := c.call(ctx, c.Timeout, http.MethodGet, "/agents/profiles/"+url.PathEscape(name), nil, &out)
	return out, err
}

type TimeoutError struct {
	Method, Path string
	Limit        time.Duration
}

func (e *TimeoutError) Error() string {
	return fmt.Sprintf("%s %s: no response within %s", e.Method, e.Path, e.Limit)
}

func (e *TimeoutError) Unwrap() error { return context.DeadlineExceeded }

func (c *Client) CreateProfile(ctx context.Context, name, content string) error {
	return c.call(ctx, c.Timeout, http.MethodPost, "/agents/profiles", map[string]string{"name": name, "content": content}, nil)
}

func (c *Client) DeleteProfile(ctx context.Context, name string) error {
	return c.call(ctx, c.Timeout, http.MethodDelete, "/agents/profiles/"+url.PathEscape(name), nil, nil)
}

func (c *Client) beforeLaunch(provider, dir string) {
	if c.BeforeLaunch != nil && provider != "" && dir != "" {
		c.BeforeLaunch(provider, dir)
	}
}
