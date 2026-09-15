// Package ipc defines the wire protocol between the chief CLI (client)
// and chiefd (server) over the unix socket.
//
// The protocol is line-delimited JSON. Each frame is a single JSON object
// terminated by a newline. Requests carry a method name and free-form params;
// responses carry either a result or an error. An id field correlates
// request/response pairs so a single connection can multiplex calls.
package ipc

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
)

// SocketPath returns the chief.sock path under
// ~/Library/Application Support/Chief/.  It does not create the directory.
func SocketPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "Application Support", "Chief", "chief.sock"), nil
}

// DataDir returns ~/Library/Application Support/Chief/, creating it if missing.
func DataDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, "Library", "Application Support", "Chief")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

// LogDir returns ~/Library/Logs/Chief/, creating it if missing.
func LogDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, "Library", "Logs", "Chief")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

// Request is one call from client to server.
type Request struct {
	ID     uint64          `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

// Response is one reply from server to client. Exactly one of Result or Error is set.
type Response struct {
	ID     uint64          `json:"id"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *RPCError       `json:"error,omitempty"`
}

// RPCError is a structured error payload.
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *RPCError) Error() string { return fmt.Sprintf("rpc error %d: %s", e.Code, e.Message) }

// Standard error codes.
const (
	ErrCodeParse       = -32700
	ErrCodeMethod      = -32601
	ErrCodeInvalidArgs = -32602
	ErrCodeInternal    = -32603
)

// Handler processes a single request. Returning a non-nil error yields an
// RPCError to the caller; returning a value marshals it as the result.
type Handler func(ctx HandlerContext, params json.RawMessage) (any, error)

// HandlerContext carries per-request metadata to the handler. Kept minimal for
// now; peer credentials will be added in M2 when MCP mutations need scoping.
type HandlerContext struct {
	Method string
}

// Server dispatches requests to registered handlers over a single connection.
type Server struct {
	mu       sync.RWMutex
	handlers map[string]Handler
}

// NewServer returns an empty dispatcher.
func NewServer() *Server {
	return &Server{handlers: map[string]Handler{}}
}

// Register a method handler. Overwrites any prior registration for the same name.
func (s *Server) Register(method string, h Handler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handlers[method] = h
}

// Serve reads line-delimited requests from rw and writes responses back.
// One goroutine per connection; requests are handled sequentially within a
// connection. Returns when the peer closes the connection.
func (s *Server) Serve(rw io.ReadWriter) error {
	scanner := bufio.NewScanner(rw)
	scanner.Buffer(make([]byte, 64*1024), 1<<20)
	enc := json.NewEncoder(rw)
	for scanner.Scan() {
		var req Request
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			_ = enc.Encode(Response{Error: &RPCError{Code: ErrCodeParse, Message: err.Error()}})
			continue
		}
		s.mu.RLock()
		h, ok := s.handlers[req.Method]
		s.mu.RUnlock()
		if !ok {
			_ = enc.Encode(Response{ID: req.ID, Error: &RPCError{Code: ErrCodeMethod, Message: "unknown method: " + req.Method}})
			continue
		}
		result, err := h(HandlerContext{Method: req.Method}, req.Params)
		if err != nil {
			var rpce *RPCError
			if errors.As(err, &rpce) {
				_ = enc.Encode(Response{ID: req.ID, Error: rpce})
			} else {
				_ = enc.Encode(Response{ID: req.ID, Error: &RPCError{Code: ErrCodeInternal, Message: err.Error()}})
			}
			continue
		}
		raw, err := json.Marshal(result)
		if err != nil {
			_ = enc.Encode(Response{ID: req.ID, Error: &RPCError{Code: ErrCodeInternal, Message: "marshal result: " + err.Error()}})
			continue
		}
		_ = enc.Encode(Response{ID: req.ID, Result: raw})
	}
	return scanner.Err()
}

// Client is a synchronous JSON-RPC-ish client over a single connection.
// Not safe for concurrent Call from multiple goroutines; wrap externally
// if that's needed.
type Client struct {
	conn   io.ReadWriteCloser
	enc    *json.Encoder
	dec    *json.Decoder
	nextID atomic.Uint64
}

// NewClient wraps an established connection.
func NewClient(conn io.ReadWriteCloser) *Client {
	return &Client{
		conn: conn,
		enc:  json.NewEncoder(conn),
		dec:  json.NewDecoder(conn),
	}
}

// Call sends a request and blocks for the correlated response.
// out may be nil to discard the result.
func (c *Client) Call(method string, params any, out any) error {
	id := c.nextID.Add(1)
	var raw json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return err
		}
		raw = b
	}
	if err := c.enc.Encode(Request{ID: id, Method: method, Params: raw}); err != nil {
		return err
	}
	var resp Response
	if err := c.dec.Decode(&resp); err != nil {
		return err
	}
	if resp.ID != id {
		return fmt.Errorf("id mismatch: sent %d got %d", id, resp.ID)
	}
	if resp.Error != nil {
		return resp.Error
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(resp.Result, out)
}

// Close closes the underlying connection.
func (c *Client) Close() error { return c.conn.Close() }
