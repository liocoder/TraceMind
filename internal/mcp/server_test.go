package mcp

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/liocoder/TraceMind/internal/datasource/mock"
	"github.com/liocoder/TraceMind/internal/tool"
)

// testDataset is a small mock observation dataset used by the protocol tests.
const testDataset = `{
  "scenarios": {
    "default": {
      "metrics": [
        {
          "service": "order-service",
          "timestamp": "2026-10-15T10:00:00Z",
          "status": "degraded",
          "values": {
            "connection_pool_usage": {"value": 98, "unit": "%"},
            "error_rate": {"value": 8.5, "unit": "%"}
          }
        }
      ],
      "logs": [
        {
          "timestamp": "2026-10-15T10:00:01Z",
          "service": "order-service",
          "level": "ERROR",
          "message": "request timeout while acquiring database connection"
        }
      ]
    }
  }
}`

// newTestRegistry builds a registry backed by a mock provider over a
// temporary copy of testDataset.
func newTestRegistry(t *testing.T) *tool.Registry {
	t.Helper()

	datasetPath := filepath.Join(t.TempDir(), "observations.json")
	if err := os.WriteFile(datasetPath, []byte(testDataset), 0o644); err != nil {
		t.Fatalf("write test dataset: %v", err)
	}

	provider, err := mock.NewProvider(datasetPath, "default")
	if err != nil {
		t.Fatalf("create mock provider: %v", err)
	}

	registry := tool.NewRegistry()
	if err := registry.Register(tool.NewLogsSearchTool(provider)); err != nil {
		t.Fatalf("register logs tool: %v", err)
	}
	if err := registry.Register(tool.NewMetricsQueryTool(provider)); err != nil {
		t.Fatalf("register metrics tool: %v", err)
	}
	return registry
}

// newPair starts an in-process server+client pair over io.Pipe so the tests
// exercise the real stdio framing without spawning processes. The raw
// reader/writer ends are returned for tests that need to send malformed
// input directly.
func newPair(t *testing.T, registry *tool.Registry) (*Client, io.Reader, io.Writer) {
	t.Helper()

	serverIn, clientOut := io.Pipe()
	clientIn, serverOut := io.Pipe()

	server := NewServer(registry, serverIn, serverOut)
	go func() {
		if err := server.Serve(); err != nil {
			t.Errorf("mcp server: %v", err)
		}
	}()

	return NewClient(clientIn, clientOut), clientIn, clientOut
}

// handshake runs the initialize exchange expected by the server.
func handshake(t *testing.T, client *Client) error {
	t.Helper()
	if _, err := client.Initialize(); err != nil {
		return err
	}
	return client.Initialized()
}

func TestInitializeHandshake(t *testing.T) {
	client, _, _ := newPair(t, newTestRegistry(t))

	res, err := client.Initialize()
	if err != nil {
		t.Fatalf("initialize: %v", err)
	}
	if res.ProtocolVersion != ProtocolVersion {
		t.Fatalf(
			"protocol version: got %q, want %q",
			res.ProtocolVersion,
			ProtocolVersion,
		)
	}
	if res.ServerInfo.Name != ServerName {
		t.Fatalf("server name: got %q, want %q", res.ServerInfo.Name, ServerName)
	}
	if res.Capabilities.Tools == nil {
		t.Fatal("expected tools capability to be advertised")
	}
}

func TestRejectsBeforeInitialized(t *testing.T) {
	client, _, _ := newPair(t, newTestRegistry(t))

	_, err := client.call("tools/list", nil)
	if err == nil {
		t.Fatal("expected tools/list before handshake to fail")
	}
	rpcErr, ok := err.(*Error)
	if !ok {
		t.Fatalf("expected *Error, got %T: %v", err, err)
	}
	if rpcErr.Code != CodeServerNotInitialized {
		t.Fatalf(
			"error code: got %d, want %d",
			rpcErr.Code,
			CodeServerNotInitialized,
		)
	}
}

func TestListTools(t *testing.T) {
	client, _, _ := newPair(t, newTestRegistry(t))
	if err := handshake(t, client); err != nil {
		t.Fatal(err)
	}

	tools, err := client.ListTools()
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	if len(tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(tools))
	}

	byName := make(map[string]Tool, len(tools))
	for _, tl := range tools {
		byName[tl.Name] = tl
	}
	for _, name := range []string{"search_logs", "query_metrics"} {
		tl, ok := byName[name]
		if !ok {
			t.Fatalf("missing tool %q", name)
		}
		if len(tl.InputSchema) == 0 {
			t.Fatalf("tool %q has no inputSchema", name)
		}
	}
}

func TestCallMetricsTool(t *testing.T) {
	client, _, _ := newPair(t, newTestRegistry(t))
	if err := handshake(t, client); err != nil {
		t.Fatal(err)
	}

	result, err := client.CallTool(
		"query_metrics",
		map[string]any{"service": "order-service"},
	)
	if err != nil {
		t.Fatalf("tools/call: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected tool error: %+v", result.Content)
	}
	if len(result.Content) == 0 || result.Content[0].Type != "text" {
		t.Fatalf("unexpected content: %+v", result.Content)
	}
	if !strings.Contains(result.Content[0].Text, "order-service") ||
		!strings.Contains(result.Content[0].Text, "error_rate") {
		t.Fatalf("unexpected metrics content: %s", result.Content[0].Text)
	}
}

func TestCallLogsTool(t *testing.T) {
	client, _, _ := newPair(t, newTestRegistry(t))
	if err := handshake(t, client); err != nil {
		t.Fatal(err)
	}

	result, err := client.CallTool(
		"search_logs",
		map[string]any{"query": "timeout"},
	)
	if err != nil {
		t.Fatalf("tools/call: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected tool error: %+v", result.Content)
	}
	if len(result.Content) == 0 ||
		!strings.Contains(result.Content[0].Text, "timeout") {
		t.Fatalf("expected log content to match query, got: %+v", result.Content)
	}
}

func TestCallUnknownTool(t *testing.T) {
	client, _, _ := newPair(t, newTestRegistry(t))
	if err := handshake(t, client); err != nil {
		t.Fatal(err)
	}

	result, err := client.CallTool("no_such_tool", nil)
	if err != nil {
		t.Fatalf(
			"tools/call should not return a JSON-RPC error for an unknown tool: %v",
			err,
		)
	}
	if !result.IsError {
		t.Fatal("expected isError=true for an unknown tool")
	}
	if len(result.Content) == 0 ||
		!strings.Contains(result.Content[0].Text, "not found") {
		t.Fatalf(
			"expected 'not found' in content, got: %+v",
			result.Content,
		)
	}
}

func TestCallToolMissingName(t *testing.T) {
	client, _, _ := newPair(t, newTestRegistry(t))
	if err := handshake(t, client); err != nil {
		t.Fatal(err)
	}

	_, err := client.call("tools/call", map[string]any{
		"arguments": map[string]any{},
	})
	if err == nil {
		t.Fatal("expected invalid params error")
	}
	rpcErr, ok := err.(*Error)
	if !ok || rpcErr.Code != CodeInvalidParams {
		t.Fatalf("expected code %d, got %v", CodeInvalidParams, err)
	}
}

func TestMethodNotFound(t *testing.T) {
	client, _, _ := newPair(t, newTestRegistry(t))
	if err := handshake(t, client); err != nil {
		t.Fatal(err)
	}

	_, err := client.call("unknown/method", nil)
	if err == nil {
		t.Fatal("expected method not found error")
	}
	rpcErr, ok := err.(*Error)
	if !ok || rpcErr.Code != CodeMethodNotFound {
		t.Fatalf("expected code %d, got %v", CodeMethodNotFound, err)
	}
}

func TestParseError(t *testing.T) {
	_, rawIn, rawOut := newPair(t, newTestRegistry(t))

	if _, err := rawOut.Write([]byte(
		`{"jsonrpc":"2.0","id":1,"method":"tools/list"` + "\n",
	)); err != nil {
		t.Fatalf("write malformed message: %v", err)
	}

	line, err := bufio.NewReader(rawIn).ReadString('\n')
	if err != nil {
		t.Fatalf("read response: %v", err)
	}

	var resp response
	if err := json.Unmarshal([]byte(line), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Error == nil || resp.Error.Code != CodeParseError {
		t.Fatalf("expected parse error, got %+v", resp)
	}
	if string(resp.ID) != "null" {
		t.Fatalf("expected null id for parse error, got %s", resp.ID)
	}
}

func TestPing(t *testing.T) {
	client, _, _ := newPair(t, newTestRegistry(t))
	if err := handshake(t, client); err != nil {
		t.Fatal(err)
	}

	if err := client.Ping(); err != nil {
		t.Fatalf("ping: %v", err)
	}
}

func TestShutdown(t *testing.T) {
	client, _, _ := newPair(t, newTestRegistry(t))
	if err := handshake(t, client); err != nil {
		t.Fatal(err)
	}

	if err := client.Shutdown(); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
}
