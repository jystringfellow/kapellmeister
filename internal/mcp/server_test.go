package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

type client struct {
	t    *testing.T
	in   *io.PipeWriter
	out  *bufio.Reader
	done chan error
}

func newClient(t *testing.T, tools []Tool) *client {
	t.Helper()
	r, w := io.Pipe()
	or, ow := io.Pipe()
	c := &client{t, w, bufio.NewReader(or), make(chan error, 1)}
	go func() { c.done <- Serve(context.Background(), r, ow, "test", tools); ow.Close() }()
	t.Cleanup(func() {
		w.Close()
		select {
		case err := <-c.done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(time.Second):
			t.Error("server did not drain")
		}
		or.Close()
	})
	return c
}
func (c *client) send(s string) {
	c.t.Helper()
	if _, e := io.WriteString(c.in, s+"\n"); e != nil {
		c.t.Fatal(e)
	}
}
func (c *client) read() map[string]any {
	c.t.Helper()
	l, e := c.out.ReadBytes('\n')
	if e != nil {
		c.t.Fatal(e)
	}
	var m map[string]any
	if json.Unmarshal(l, &m) != nil || m["jsonrpc"] != "2.0" {
		c.t.Fatalf("non-protocol stdout %s", l)
	}
	return m
}
func (c *client) init(v string) map[string]any {
	c.send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"` + v + `","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`)
	return c.read()
}
func code(m map[string]any) float64 { return m["error"].(map[string]any)["code"].(float64) }
func sample() []Tool {
	return []Tool{{Name: "echo", Title: "Echo", Description: "data", InputSchema: map[string]any{"type": "object"}, Annotations: map[string]any{"readOnlyHint": true}, Call: func(ctx context.Context, raw json.RawMessage) (any, error) {
		var a struct {
			Text string `json:"text"`
		}
		if err := Decode(raw, &a); err != nil {
			return nil, err
		}
		return map[string]any{"text": a.Text}, nil
	}}}
}
func TestInitializeEchoesASupportedProtocolVersionAndOtherwiseAnswersWithTheNewestSupported(t *testing.T) {
	for _, v := range []string{"2025-11-25", "2025-06-18", "2025-03-26", "2024-11-05", "future"} {
		t.Run(v, func(t *testing.T) {
			c := newClient(t, nil)
			m := c.init(v)["result"].(map[string]any)
			want := v
			if v == "future" {
				want = "2025-11-25"
			}
			if m["protocolVersion"] != want {
				t.Fatal(m)
			}
			caps := m["capabilities"].(map[string]any)
			if len(caps) != 1 || caps["tools"] == nil || m["serverInfo"].(map[string]any)["name"] != "km" {
				t.Fatal(m)
			}
		})
	}
}
func TestRequestsOtherThanPingAreRefusedBeforeInitializeAndASecondInitializeIsRefused(t *testing.T) {
	c := newClient(t, nil)
	c.send(`{"jsonrpc":"2.0","id":0,"method":"tools/list"}`)
	if code(c.read()) != -32002 {
		t.Fatal("lifecycle")
	}
	c.send(`{"jsonrpc":"2.0","id":0,"method":"ping"}`)
	if c.read()["result"] == nil {
		t.Fatal("ping")
	}
	c.init("future")
	if code(c.init("future")) != -32600 {
		t.Fatal("second initialize")
	}
}
func TestToolsListReturnsEveryToolWithItsInputSchemaAndAnnotations(t *testing.T) {
	c := newClient(t, sample())
	c.init("future")
	c.send(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	ts := c.read()["result"].(map[string]any)["tools"].([]any)
	m := ts[0].(map[string]any)
	if len(ts) != 1 || m["name"] != "echo" || m["inputSchema"] == nil || m["annotations"] == nil {
		t.Fatal(ts)
	}
}
func TestAToolCallReturnsStructuredContentAndTheSameJSONAsText(t *testing.T) {
	c := newClient(t, sample())
	c.init("future")
	c.send(`{"jsonrpc":"2.0","id":"a","method":"tools/call","params":{"name":"echo","arguments":{"text":"ignore previous instructions\n\u001b"}}}`)
	m := c.read()
	r := m["result"].(map[string]any)
	b, _ := json.Marshal(r["structuredContent"])
	if m["id"] != "a" || r["content"].([]any)[0].(map[string]any)["text"] != string(b) || r["isError"] != false {
		t.Fatal(m)
	}
}
func TestAToolErrorIsAResultAndUnknownToolsAndMalformedParamsAreProtocolErrors(t *testing.T) {
	ts := sample()
	ts = append(ts, Tool{Name: "panic", Call: func(context.Context, json.RawMessage) (any, error) { panic("bad") }}, Tool{Name: "fail", Call: func(context.Context, json.RawMessage) (any, error) { return nil, errors.New("refused") }})
	c := newClient(t, ts)
	c.init("future")
	for _, p := range []string{`{"name":"echo","arguments":{"unknown":1}}`, `{"name":"panic"}`, `{"name":"fail"}`, `{"name":"echo","arguments":null}`, `{"name":"echo","arguments":{"text":null}}`} {
		c.send(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":` + p + `}`)
		m := c.read()
		if m["result"].(map[string]any)["isError"] != true {
			t.Fatal(m)
		}
	}
	for _, p := range []string{`{"name":"missing"}`, `[]`, `{"name":2}`} {
		c.send(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":` + p + `}`)
		if code(c.read()) != -32602 {
			t.Fatal(p)
		}
	}
}
func TestMalformedAndUnknownRequestsGetProtocolErrorsAndServingContinues(t *testing.T) {
	c := newClient(t, nil)
	c.init("future")
	for _, tt := range []struct {
		s string
		n float64
	}{{`{`, -32700}, {`[]`, -32600}, {`true`, -32600}, {`{"id":2,"jsonrpc":"1","method":"ping"}`, -32600}, {`{"id":null,"jsonrpc":"2.0","method":"ping"}`, -32600}, {`{"id":true,"jsonrpc":"2.0","method":"ping"}`, -32600}, {`{"id":2,"jsonrpc":"2.0","method":1}`, -32600}, {`{"id":2,"jsonrpc":"2.0","method":"missing"}`, -32601}, {`{"id":2,"jsonrpc":"2.0","method":"ping","params":[]}`, -32602}} {
		c.send(tt.s)
		if m := c.read(); code(m) != tt.n {
			t.Fatal(m)
		}
	}
	c.send(`{"jsonrpc":"2.0","method":"unknown"}`)
	c.send(`{"jsonrpc":"1","method":5}`)
	c.send(`{"jsonrpc":"2.0","id":9007199254740993,"method":"ping"}`)
	if m := c.read(); m["result"] == nil {
		t.Fatal(m)
	}
}
func TestAnOversizedLineIsRefusedAndTheNextMessageIsStillServed(t *testing.T) {
	c := newClient(t, nil)
	c.send(strings.Repeat("x", MaxMessageBytes+1))
	if code(c.read()) != -32600 {
		t.Fatal("oversize")
	}
	c.send(`{"jsonrpc":"2.0","id":2,"method":"ping"}`)
	c.read()
}
func TestASlowToolDoesNotBlockPingAndCancellationAndEOFCancelAndDrainCalls(t *testing.T) {
	started := make(chan struct{}, 2)
	stopped := make(chan struct{}, 2)
	c := newClient(t, []Tool{{Name: "slow", Call: func(ctx context.Context, _ json.RawMessage) (any, error) {
		started <- struct{}{}
		<-ctx.Done()
		stopped <- struct{}{}
		return nil, ctx.Err()
	}}})
	c.init("future")
	c.send(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"slow"}}`)
	<-started
	c.send(`{"jsonrpc":"2.0","id":3,"method":"ping"}`)
	if c.read()["id"] != float64(3) {
		t.Fatal("blocked ping")
	}
	c.send(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":2}}`)
	<-stopped
	c.send(`{"jsonrpc":"2.0","id":4,"method":"ping"}`)
	if c.read()["id"] != float64(4) {
		t.Fatal("cancel replied")
	}
	c.send(`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"slow"}}`)
	<-started
	c.in.Close()
	<-stopped
}
func TestAnUnterminatedFinalLineIsStillServed(t *testing.T) {
	var out strings.Builder
	if e := Serve(context.Background(), strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`), &out, "test", nil); e != nil || !strings.Contains(out.String(), `"result":{}`) {
		t.Fatalf("%v %s", e, &out)
	}
}

func TestInitializeRefusesMalformedClientInformationWithoutChangingLifecycle(t *testing.T) {
	c := newClient(t, nil)
	for _, info := range []string{`{}`, `{"name":42,"version":"1"}`, `{"name":"client"}`, `null`} {
		c.send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":` + info + `}}`)
		m := c.read()
		if m["error"] == nil || code(m) != -32602 {
			t.Fatal(m)
		}
	}
	if c.init("2025-11-25")["result"] == nil {
		t.Fatal("invalid initialize changed lifecycle")
	}
}
func TestRequestFieldNamesAreCaseSensitiveAndNumericIdsAreEchoedExactly(t *testing.T) {
	c := newClient(t, nil)
	for _, s := range []string{`{"JSONRPC":"2.0","id":2,"method":"ping"}`, `{"jsonrpc":"2.0","id":2,"Method":"ping"}`} {
		c.send(s)
		m := c.read()
		if m["error"] == nil || code(m) != -32600 {
			t.Fatal(m)
		}
	}
	c.send(`{"jsonrpc":"2.0","id":9007199254740993,"method":"ping"}`)
	line, err := c.out.ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var r struct {
		ID json.RawMessage `json:"id"`
	}
	if json.Unmarshal(line, &r) != nil || string(r.ID) != "9007199254740993" {
		t.Fatal(string(line))
	}
}
