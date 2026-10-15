package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/liocoder/TraceMind/internal/tool"
)

// Server identity reported during the initialize handshake.
const (
	ServerName    = "tracemind"
	ServerVersion = "0.1.0"
)

// Server implements the server side of the MCP stdio transport.
//
// Messages are newline-delimited JSON-RPC 2.0 objects (the MCP stdio
// framing), not LSP-style Content-Length frames. The server operates on
// arbitrary io.Reader/io.Writer pairs so it can be driven in-process by
// tests and embedded by a host; cmd/mcp-server wires it to os.Stdin and
// os.Stdout.
type Server struct {
	registry *tool.Registry

	in  io.Reader
	out io.Writer
	mu  sync.Mutex

	// initReceived records that the client sent initialize.
	// initialized records that the client sent notifications/initialized.
	// Requests other than initialize, ping and shutdown are rejected until
	// the handshake completes (MCP error -32002).
	initReceived bool
	initialized  bool
	shutdown     bool
}

// NewServer creates an MCP server that exposes the registered tools.
func NewServer(registry *tool.Registry, in io.Reader, out io.Writer) *Server {
	return &Server{
		registry: registry,
		in:       in,
		out:      out,
	}
}

// Serve reads JSON-RPC messages until EOF and writes responses back.
// A clean EOF or a shutdown request terminates Serve with a nil error.
func (s *Server) Serve() error {
	if s == nil || s.in == nil || s.out == nil || s.registry == nil {
		return errors.New("mcp server: registry, input and output are required")
	}

	scanner := bufio.NewScanner(s.in)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		if err := s.handleLine([]byte(line)); err != nil {
			return err
		}
		if s.shutdown {
			return nil
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("mcp server: read input: %w", err)
	}
	return nil
}

func (s *Server) handleLine(line []byte) error {
	var msg message
	if err := json.Unmarshal(line, &msg); err != nil {
		return s.write(nil, nil, &Error{
			Code:    CodeParseError,
			Message: "parse error: invalid JSON",
		})
	}

	if msg.JSONRPC != "2.0" {
		return s.write(msg.ID, nil, &Error{
			Code:    CodeInvalidRequest,
			Message: "invalid jsonrpc version",
		})
	}

	// Notifications carry no ID and never produce a response.
	if len(msg.ID) == 0 {
		s.handleNotification(msg)
		return nil
	}

	result, err := s.dispatch(msg)
	if err != nil {
		var rpcErr *Error
		if errors.As(err, &rpcErr) {
			return s.write(msg.ID, nil, rpcErr)
		}
		return s.write(msg.ID, nil, &Error{
			Code:    CodeInternalError,
			Message: err.Error(),
		})
	}
	return s.write(msg.ID, result, nil)
}

func (s *Server) handleNotification(msg message) {
	switch msg.Method {
	case "notifications/initialized":
		// Ignore the notification unless it follows a real initialize request.
		if s.initReceived {
			s.initialized = true
		}
	case "notifications/cancelled":
		// Cancellation is a hint. This server executes tools synchronously,
		// so there is nothing to cancel.
	default:
		// Unknown notifications are ignored, as required by JSON-RPC.
	}
}

func (s *Server) dispatch(msg message) (any, error) {
	switch msg.Method {
	case "initialize":
		return s.handleInitialize(msg.Params)
	case "ping":
		return map[string]any{}, nil
	case "shutdown":
		s.shutdown = true
		return map[string]any{}, nil
	default:
		if !s.initialized {
			return nil, &Error{
				Code:    CodeServerNotInitialized,
				Message: "server not initialized: complete the initialize handshake first",
			}
		}

		switch msg.Method {
		case "tools/list":
			return s.handleListTools()
		case "tools/call":
			return s.handleCallTool(msg.Params)
		default:
			return nil, &Error{
				Code:    CodeMethodNotFound,
				Message: fmt.Sprintf("method not found: %s", msg.Method),
			}
		}
	}
}

func (s *Server) handleInitialize(params json.RawMessage) (any, error) {
	var p struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, &Error{
				Code:    CodeInvalidParams,
				Message: "invalid initialize params",
			}
		}
	}
	if strings.TrimSpace(p.ProtocolVersion) == "" {
		return nil, &Error{
			Code:    CodeInvalidParams,
			Message: "initialize requires protocolVersion",
		}
	}

	s.initReceived = true

	return initializeResult{
		ProtocolVersion: ProtocolVersion,
		Capabilities: serverCapabilities{
			Tools: &struct {
				ListChanged bool `json:"listChanged"`
			}{ListChanged: false},
		},
		ServerInfo: implementationInfo{
			Name:    ServerName,
			Version: ServerVersion,
		},
	}, nil
}

func (s *Server) handleListTools() (any, error) {
	definitions, err := s.registry.ToLLMDefinitions()
	if err != nil {
		return nil, err
	}

	tools := make([]Tool, 0, len(definitions))
	for _, definition := range definitions {
		tools = append(tools, Tool{
			Name:        definition.Function.Name,
			Description: definition.Function.Description,
			InputSchema: definition.Function.Parameters,
		})
	}
	return listToolsResult{Tools: tools}, nil
}

func (s *Server) handleCallTool(params json.RawMessage) (any, error) {
	var p struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments,omitempty"`
	}
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, &Error{
				Code:    CodeInvalidParams,
				Message: "invalid tools/call params",
			}
		}
	}
	if strings.TrimSpace(p.Name) == "" {
		return nil, &Error{
			Code:    CodeInvalidParams,
			Message: "tools/call requires a tool name",
		}
	}

	result, err := s.registry.Execute(context.Background(), p.Name, p.Arguments)
	if err != nil {
		// Tool-level failures are returned as structured content, not as
		// JSON-RPC errors, so the client can surface them to the model.
		return callToolResult{
			Content: []contentItem{{
				Type: "text",
				Text: fmt.Sprintf("tool call failed: %v", err),
			}},
			IsError: true,
		}, nil
	}

	return callToolResult{
		Content: []contentItem{{Type: "text", Text: result.Content}},
	}, nil
}

func (s *Server) write(id json.RawMessage, result any, rpcErr *Error) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := json.Marshal(response{
		JSONRPC: "2.0",
		ID:      id,
		Result:  result,
		Error:   rpcErr,
	})
	if err != nil {
		return fmt.Errorf("mcp server: marshal response: %w", err)
	}

	if _, err := s.out.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("mcp server: write response: %w", err)
	}
	return nil
}
