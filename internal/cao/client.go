package cao

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
