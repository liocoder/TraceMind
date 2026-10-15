// Package mcp implements a minimal Model Context Protocol (MCP) server and
// client over the stdio transport, following the 2025-06-18 revision of the
// MCP specification (JSON-RPC 2.0).
//
// The implementation covers the subset needed to expose TraceMind's
// investigation tools to any MCP client: the initialize handshake, ping,
// tools/list, tools/call and shutdown. Tool-level failures are returned as
// structured content (isError=true), while protocol violations map to
// JSON-RPC error codes.
package mcp

import (
	"encoding/json"
	"fmt"
)

// ProtocolVersion is the MCP specification revision implemented by this
// package. During the initialize handshake the server always responds with
// this version; clients that request an older compatible version can still
// proceed.
const ProtocolVersion = "2025-06-18"

// JSON-RPC 2.0 error codes.
const (
	CodeParseError     = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternalError  = -32603
)

// MCP-specific error codes.
const (
	// CodeServerNotInitialized is returned when a request other than
	// initialize, ping or shutdown arrives before the handshake completed.
	CodeServerNotInitialized = -32002
)

// Error is a JSON-RPC 2.0 error object.
type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func (e *Error) Error() string {
	if e == nil {
		return "mcp: nil error"
	}
	if e.Data == nil {
		return fmt.Sprintf("mcp: code %d: %s", e.Code, e.Message)
	}
	return fmt.Sprintf("mcp: code %d: %s (%v)", e.Code, e.Message, e.Data)
}

// message is a JSON-RPC 2.0 message. Requests carry a non-nil ID;
// notifications have no ID member and never receive a response. The
// omitempty tag matters: marshaling a notification with "id":null would
// make it indistinguishable from a request with a null id.
type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// response is a JSON-RPC 2.0 response. Exactly one of Result and Error is set.
type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

// implementationInfo identifies a client or server implementation.
type implementationInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// initializeResult is the result of the initialize handshake.
type initializeResult struct {
	ProtocolVersion string             `json:"protocolVersion"`
	Capabilities    serverCapabilities `json:"capabilities"`
	ServerInfo      implementationInfo `json:"serverInfo"`
}

// serverCapabilities declares which protocol capabilities the server offers.
type serverCapabilities struct {
	Tools *struct {
		ListChanged bool `json:"listChanged"`
	} `json:"tools,omitempty"`
}

// Tool is an MCP tool definition. InputSchema is a JSON Schema object.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// listToolsResult is the result of tools/list.
type listToolsResult struct {
	Tools []Tool `json:"tools"`
}

// callToolResult is the result of tools/call. Tool-level failures are
// reported through Content with IsError set, not through JSON-RPC errors.
type callToolResult struct {
	Content []contentItem `json:"content"`
	IsError bool          `json:"isError"`
}

// contentItem is one item of structured tool output.
type contentItem struct {
	Type string `json:"type"`
	Text string `json:"text"`
}
