package cao

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

type Client struct {
	BaseURL string
	HTTP    *http.Client
}

func NewClient(baseURL string) *Client {
	return &Client{BaseURL: baseURL, HTTP: &http.Client{}}
}

type StepRequest struct {
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
	ID           string  `json:"id"`
	Status       string  `json:"status"`
	SessionName  string  `json:"session_name"`
	Provider     string  `json:"provider"`
	AgentProfile string  `json:"agent_profile"`
	Model        *string `json:"model"`
	ModelHonored *bool   `json:"model_honored"`
}

func (c *Client) RunStep(ctx context.Context, req StepRequest) (StepResponse, error) {
	var resp StepResponse
	err := c.call(ctx, http.MethodPost, "/terminals/run-step", req, &resp)
	return resp, err
}

func (c *Client) Terminal(ctx context.Context, id string) (Terminal, error) {
	var t Terminal
	err := c.call(ctx, http.MethodGet, "/terminals/"+id, nil, &t)
	return t, err
}

type Profile struct {
	Name     string `json:"name"`
	Loadable bool   `json:"loadable"`
}

func (c *Client) Profiles(ctx context.Context) ([]Profile, error) {
	var out []Profile
	err := c.call(ctx, http.MethodGet, "/agents/profiles", nil, &out)
	return out, err
}

func (c *Client) DeleteTerminal(ctx context.Context, id string) error {
	return c.call(ctx, http.MethodDelete, "/terminals/"+id, nil, nil)
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
	q := l.query()
	q.Set("session_name", name)
	var t Terminal
	err := c.call(ctx, http.MethodPost, "/sessions?"+q.Encode(), nil, &t)
	return t, err
}

func (c *Client) AddTerminal(ctx context.Context, session string, l Launch) (Terminal, error) {
	var t Terminal
	err := c.call(ctx, http.MethodPost, "/sessions/"+url.PathEscape(session)+"/terminals?"+l.query().Encode(), nil, &t)
	return t, err
}

func (c *Client) DeleteSession(ctx context.Context, session string) error {
	return c.call(ctx, http.MethodDelete, "/sessions/"+url.PathEscape(session), nil, nil)
}

func (c *Client) SetGroup(ctx context.Context, id string, group []string) error {
	return c.call(ctx, http.MethodPatch, "/terminals/"+id+"/group", map[string]any{"group": group}, nil)
}

func (c *Client) SetMetadata(ctx context.Context, id string, metadata map[string]any) error {
	return c.call(ctx, http.MethodPatch, "/terminals/"+id+"/metadata", map[string]any{"metadata": metadata}, nil)
}

func (c *Client) SendMessage(ctx context.Context, receiver, sender, message string) error {
	q := url.Values{"sender_id": {sender}, "message": {message}}
	return c.call(ctx, http.MethodPost, "/terminals/"+receiver+"/inbox/messages?"+q.Encode(), nil, nil)
}

func (c *Client) Output(ctx context.Context, id string) (string, error) {
	var out struct {
		Output string `json:"output"`
	}
	err := c.call(ctx, http.MethodGet, "/terminals/"+id+"/output?mode=full", nil, &out)
	return out.Output, err
}

func (c *Client) call(ctx context.Context, method, path string, body, out any) error {
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
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		herr := &HTTPError{Method: method, Path: path, Status: resp.StatusCode, Body: truncate(string(data), 600)}
		var detail struct {
			Detail struct {
				TerminalID string `json:"terminal_id"`
			} `json:"detail"`
		}
		if json.Unmarshal(data, &detail) == nil {
			herr.TerminalID = detail.Detail.TerminalID
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
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("%s %s: HTTP %d: %s", e.Method, e.Path, e.Status, e.Body)
}
