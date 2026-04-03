// Package mcp provides Model Context Protocol (MCP) client bootstrapping for direct
// tool server connections. Inspired by claw-code-parity's mcp_client.rs and mcp_stdio.rs.
// This enables Stoke to connect to MCP servers (filesystem, database, custom tools)
// and pass them through to Claude Code or use them directly.
package mcp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// ServerConfig describes how to launch and connect to an MCP server.
type ServerConfig struct {
	Name    string            `json:"name"`
	Command string            `json:"command"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	Enabled bool              `json:"enabled"`
}

// ToolDefinition is an MCP tool definition received from a server.
type ToolDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// StdioClient communicates with an MCP server over stdin/stdout using JSON-RPC.
type StdioClient struct {
	name    string
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	scanner *bufio.Scanner
	mu      sync.Mutex
	nextID  int
	tools   []ToolDefinition
}

// jsonRPCRequest is a JSON-RPC 2.0 request message.
type jsonRPCRequest struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      int         `json:"id"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params,omitempty"`
}

// jsonRPCResponse is a JSON-RPC 2.0 response message.
type jsonRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int             `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *jsonRPCError   `json:"error,omitempty"`
}

type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// NewStdioClient launches an MCP server process and establishes a JSON-RPC connection.
func NewStdioClient(config ServerConfig) (*StdioClient, error) {
	if !config.Enabled {
		return nil, fmt.Errorf("MCP server %q is disabled", config.Name)
	}

	cmd := exec.Command(config.Command, config.Args...)
	// Set up environment
	cmd.Env = os.Environ()
	for k, v := range config.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	cmd.Stderr = nil // discard MCP server stderr

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start MCP server %q: %w", config.Name, err)
	}

	client := &StdioClient{
		name:    config.Name,
		cmd:     cmd,
		stdin:   stdin,
		scanner: bufio.NewScanner(stdout),
		nextID:  1,
	}
	client.scanner.Buffer(make([]byte, 0, 1024*1024), 4*1024*1024)

	return client, nil
}

// Initialize performs the MCP initialization handshake.
func (c *StdioClient) Initialize() error {
	resp, err := c.call("initialize", map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]interface{}{},
		"clientInfo": map[string]interface{}{
			"name":    "stoke",
			"version": "1.0.0",
		},
	})
	if err != nil {
		return fmt.Errorf("initialize: %w", err)
	}
	_ = resp // server capabilities returned but not needed yet

	// Send initialized notification
	return c.notify("notifications/initialized", nil)
}

// ListTools queries the server for available tools.
func (c *StdioClient) ListTools() ([]ToolDefinition, error) {
	resp, err := c.call("tools/list", nil)
	if err != nil {
		return nil, fmt.Errorf("list tools: %w", err)
	}

	var result struct {
		Tools []ToolDefinition `json:"tools"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		return nil, fmt.Errorf("parse tools list: %w", err)
	}
	c.tools = result.Tools
	return result.Tools, nil
}

// CallTool invokes a tool on the MCP server.
func (c *StdioClient) CallTool(name string, arguments map[string]interface{}) (json.RawMessage, error) {
	return c.call("tools/call", map[string]interface{}{
		"name":      name,
		"arguments": arguments,
	})
}

// Close shuts down the MCP server process.
func (c *StdioClient) Close() error {
	c.stdin.Close()
	return c.cmd.Wait()
}

func (c *StdioClient) call(method string, params interface{}) (json.RawMessage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	id := c.nextID
	c.nextID++

	req := jsonRPCRequest{JSONRPC: "2.0", ID: id, Method: method, Params: params}
	data, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	data = append(data, '\n')

	if _, err := c.stdin.Write(data); err != nil {
		return nil, fmt.Errorf("write request: %w", err)
	}

	// Read response lines until we get our ID back
	for c.scanner.Scan() {
		line := c.scanner.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		var resp jsonRPCResponse
		if err := json.Unmarshal([]byte(line), &resp); err != nil {
			continue // skip malformed lines
		}
		if resp.ID == id {
			if resp.Error != nil {
				return nil, fmt.Errorf("RPC error %d: %s", resp.Error.Code, resp.Error.Message)
			}
			return resp.Result, nil
		}
		// Response for different ID — skip (could be notification)
	}
	if err := c.scanner.Err(); err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	return nil, fmt.Errorf("MCP server closed connection")
}

func (c *StdioClient) notify(method string, params interface{}) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Notifications have no ID
	req := map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  method,
	}
	if params != nil {
		req["params"] = params
	}
	data, err := json.Marshal(req)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	_, err = c.stdin.Write(data)
	return err
}

// ConfigFromFile loads MCP server configurations from a JSON file.
// Compatible with Claude Code's .claude/mcp.json format.
func ConfigFromFile(path string) ([]ServerConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var raw struct {
		MCPServers map[string]struct {
			Command string            `json:"command"`
			Args    []string          `json:"args"`
			Env     map[string]string `json:"env"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse MCP config: %w", err)
	}

	var configs []ServerConfig
	for name, srv := range raw.MCPServers {
		configs = append(configs, ServerConfig{
			Name:    name,
			Command: srv.Command,
			Args:    srv.Args,
			Env:     srv.Env,
			Enabled: true,
		})
	}
	return configs, nil
}

// EmptyConfigPath writes an empty MCP config file and returns its path.
// Used for MCP isolation (disabling all MCP servers for a phase).
func EmptyConfigPath(dir string) (string, error) {
	path := filepath.Join(dir, "empty-mcp.json")
	return path, os.WriteFile(path, []byte("{}"), 0644)
}
