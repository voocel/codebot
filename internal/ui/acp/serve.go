// Package acp is the Agent Client Protocol frontend: an editor runs codebot
// as a child process and drives it over JSON-RPC on stdio. Stdout is the
// protocol channel, so nothing else may write to it.
package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"

	acp "github.com/coder/acp-go-sdk"

	"github.com/voocel/codebot/internal/app"
)

func NewServer(version string) *Server {
	return &Server{version: version, sessions: make(map[acp.SessionId]*session)}
}

func (s *Server) Serve(a *app.App) error { return s.serve(a, os.Stdin, os.Stdout) }

func (s *Server) serve(a *app.App, in io.Reader, out io.Writer) error {
	s.app = a
	unsubscribe := a.Subscribe(s.onEvent)
	defer unsubscribe()
	// MCP tools join the sessions as their servers connect.
	go func() {
		report := a.Connect(context.Background())
		for _, e := range report.Errors {
			fmt.Fprintf(os.Stderr, "mcp: %s\n", e)
		}
		for _, name := range report.Login {
			fmt.Fprintf(os.Stderr, "mcp: %s needs a login: log in with /mcp in codebot\n", name)
		}
	}()
	w := &wire{in: in, out: out, opened: s.opened, ready: make(chan struct{}), opening: make(map[string]acp.SessionId)}
	s.conn = acp.NewAgentSideConnection(s, w, w)
	close(w.ready)
	<-s.conn.Done()
	return nil
}

// wire carries the protocol, and calls opened once the response to a request
// opening a session is out: the client drops updates for a session it has
// yet to hear of, and the SDK writes the response only after the handler
// returns.
type wire struct {
	in     io.Reader
	out    io.Writer
	opened func(acp.SessionId)
	// ready holds the reading back until the server has its connection,
	// which the SDK starts reading with.
	ready chan struct{}

	mu      sync.Mutex
	partial []byte                   // an incoming line read in part
	opening map[string]acp.SessionId // request ID → the session it loads or resumes, "" for a new one
}

func (w *wire) Read(p []byte) (int, error) {
	<-w.ready
	n, err := w.in.Read(p)
	w.note(p[:n])
	return n, err
}

// note records the requests that open a session.
func (w *wire) note(data []byte) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.partial = append(w.partial, data...)
	for {
		i := bytes.IndexByte(w.partial, '\n')
		if i < 0 {
			return
		}
		line := w.partial[:i]
		w.partial = w.partial[i+1:]
		if !bytes.Contains(line, []byte(`"session/`)) {
			continue
		}
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				SessionID acp.SessionId `json:"sessionId"`
			} `json:"params"`
		}
		if json.Unmarshal(line, &req) != nil || req.ID == nil {
			continue
		}
		switch req.Method {
		case acp.AgentMethodSessionNew, acp.AgentMethodSessionLoad, acp.AgentMethodSessionResume:
			w.opening[string(req.ID)] = req.Params.SessionID
		}
	}
}

func (w *wire) Write(p []byte) (int, error) {
	n, err := w.out.Write(p)
	if id, ok := w.answered(p); ok {
		go w.opened(id) // the SDK holds its write lock until Write returns
	}
	return n, err
}

// answered returns the session a successful response to a request in
// opening opens.
func (w *wire) answered(line []byte) (acp.SessionId, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.opening) == 0 {
		return "", false
	}
	var res struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Result *struct {
			SessionID acp.SessionId `json:"sessionId"`
		} `json:"result"`
	}
	if json.Unmarshal(line, &res) != nil || res.ID == nil || res.Method != "" {
		return "", false
	}
	id, ok := w.opening[string(res.ID)]
	if !ok {
		return "", false
	}
	delete(w.opening, string(res.ID))
	if res.Result == nil {
		return "", false // it failed
	}
	if id == "" {
		id = res.Result.SessionID
	}
	return id, id != ""
}
