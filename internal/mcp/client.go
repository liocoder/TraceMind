package mcp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
)

// Client is a minimal MCP client over the stdio transport.
//
// It is used by the end-to-end tests and can serve as the starting point for
// embedding TraceMind as an MCP host. Calls are strictly synchronous: one
// request in flight at a time, which matches how the stdio transport is
// normally driven.
type Client struct {
	in  *bufio.Reader
	out io.Writer

	mu     sync.Mutex
	nextID int
}

// NewClient creates an MCP client reading from in and writing to out.
func NewClient(in io.Reader, out io.Writer) *Client {
	return &Client{
		in:  bufio.NewReader(in),
		out: out,
	}
}

// Initialize performs the MCP handshake and returns the server's capabilities.
func (c *Client) Initialize() (*initializeResult, error) {
	result, err := c.call("initialize", map[string]any{
		"protocolVersion": ProtocolVersion,
		"clientInfo": map[string]any{
			"name":    "tracemind-client",
			"version": "0.1.0",
		},
	})
	if err != nil {
		return nil, err
	}

	var res initializeResult
	if err := json.Unmarshal(result, &res); err != nil {
		return nil, fmt.Errorf("mcp client: decode initialize result: %w", err)
	}
	return &res, nil
}

// Initialized sends the notifications/initialized notification.
func (c *Client) Initialized() error {
	return c.sendNotification("notifications/initialized")
}

// Ping checks that the server is alive.
func (c *Client) Ping() error {
	_, err := c.call("ping", nil)
	return err
}

// ListTools returns the tools exposed by the server.
func (c *Client) ListTools() ([]Tool, error) {
	result, err := c.call("tools/list", nil)
	if err != nil {
		return nil, err
	}

	var res listToolsResult
	if err := json.Unmarshal(result, &res); err != nil {
		return nil, fmt.Errorf("mcp client: decode tools/list result: %w", err)
	}
	return res.Tools, nil
}

// CallTool invokes a tool with the given arguments.
func (c *Client) CallTool(name string, arguments map[string]any) (*callToolResult, error) {
	if arguments == nil {
		arguments = map[string]any{}
	}

	result, err := c.call("tools/call", map[string]any{
		"name":      name,
		"arguments": arguments,
	})
	if err != nil {
		return nil, err
	}

	var res callToolResult
	if err := json.Unmarshal(result, &res); err != nil {
		return nil, fmt.Errorf("mcp client: decode tools/call result: %w", err)
	}
	return &res, nil
}

// Shutdown politely asks the server to stop serving.
func (c *Client) Shutdown() error {
	_, err := c.call("shutdown", nil)
	return err
}

func (c *Client) sendNotification(method string) error {
	data, err := json.Marshal(message{JSONRPC: "2.0", Method: method})
	if err != nil {
		return fmt.Errorf("mcp client: marshal notification: %w", err)
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if _, err := c.out.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("mcp client: write notification: %w", err)
	}
	return nil
}

func (c *Client) call(method string, params any) (json.RawMessage, error) {
	if c == nil {
		return nil, fmt.Errorf("mcp client: nil client")
	}

	c.mu.Lock()
	c.nextID++
	id := c.nextID
	c.mu.Unlock()

	req := message{
		JSONRPC: "2.0",
		ID:      []byte(fmt.Sprintf("%d", id)),
		Method:  method,
	}
	if params != nil {
		paramsRaw, err := json.Marshal(params)
		if err != nil {
			return nil, fmt.Errorf("mcp client: encode params: %w", err)
		}
		req.Params = paramsRaw
	}

	reqData, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("mcp client: marshal request: %w", err)
	}

	c.mu.Lock()
	_, err = c.out.Write(append(reqData, '\n'))
	c.mu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("mcp client: write request: %w", err)
	}

	for {
		line, err := c.in.ReadString('\n')
		if err != nil {
			return nil, fmt.Errorf("mcp client: read response: %w", err)
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		var resp response
		if err := json.Unmarshal([]byte(line), &resp); err != nil {
			// Ignore non-JSON-RPC noise on the stream.
			continue
		}

		// Responses carry an ID; notifications are ignored by the client.
		if len(resp.ID) == 0 {
			continue
		}
		if string(resp.ID) != string(req.ID) {
			continue
		}

		if resp.Error != nil {
			return nil, resp.Error
		}
		if resp.Result == nil {
			return nil, nil
		}

		data, err := json.Marshal(resp.Result)
		if err != nil {
			return nil, fmt.Errorf("mcp client: re-encode result: %w", err)
		}
		return data, nil
	}
}
