package mcp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"
)

// Client handles communication with MCP server
type Client struct {
	def        *MCPDefinition
	cmd        *exec.Cmd
	stdin      io.WriteCloser
	stdout     *bufio.Scanner
	stderr     io.ReadCloser
	connected  bool
	mu         sync.Mutex
	requestID  int64
	pending    map[int64]chan *Response
}

// Request represents an MCP JSON-RPC request
type Request struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      int64       `json:"id"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params,omitempty"`
}

// Response represents an MCP JSON-RPC response
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

// Error represents an MCP error
type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

// InitializeResult contains server initialization info
type InitializeResult struct {
	ProtocolVersion string                 `json:"protocolVersion"`
	Capabilities    map[string]interface{} `json:"capabilities"`
	ServerInfo      ServerInfo             `json:"serverInfo"`
}

// ServerInfo contains server metadata
type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// ToolsListResult contains list of available tools
type ToolsListResult struct {
	Tools []ToolDefinition `json:"tools"`
}

// CallToolParams contains parameters for calling a tool
type CallToolParams struct {
	Name      string                 `json:"name"`
	Arguments map[string]interface{} `json:"arguments,omitempty"`
}

// NewClient creates a new MCP client
func NewClient(def *MCPDefinition) *Client {
	return &Client{
		def:     def,
		pending: make(map[int64]chan *Response),
	}
}

// Connect establishes connection to MCP server
func (c *Client) Connect() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.connected {
		return nil
	}

	switch c.def.Transport.Type {
	case "stdio":
		return c.connectStdio()
	case "http":
		return fmt.Errorf("http transport not yet implemented")
	case "sse":
		return fmt.Errorf("sse transport not yet implemented")
	default:
		return fmt.Errorf("unsupported transport type: %s", c.def.Transport.Type)
	}
}

func (c *Client) connectStdio() error {
	cmd := exec.Command(c.def.Transport.Command[0], c.def.Transport.Command[1:]...)
	
	// Set environment variables
	if c.def.Transport.Env != nil {
		cmd.Env = os.Environ()
		for k, v := range c.def.Transport.Env {
			cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", k, v))
		}
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("failed to create stdin pipe: %w", err)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("failed to create stdout pipe: %w", err)
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("failed to create stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start MCP server: %w", err)
	}

	c.cmd = cmd
	c.stdin = stdin
	c.stdout = bufio.NewScanner(stdout)
	c.stderr = stderr

	// Start response handler
	go c.handleResponses()
	go c.handleStderr()

	// Initialize connection
	if err := c.initialize(); err != nil {
		c.Disconnect()
		return fmt.Errorf("failed to initialize MCP connection: %w", err)
	}

	c.connected = true
	return nil
}

func (c *Client) initialize() error {
	initParams := map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"capabilities": map[string]interface{}{},
		"clientInfo": map[string]interface{}{
			"name":    "chrome-agent",
			"version": "1.0.0",
		},
	}

	resp, err := c.call("initialize", initParams)
	if err != nil {
		return err
	}

	var initResult InitializeResult
	if err := json.Unmarshal(resp.Result, &initResult); err != nil {
		return fmt.Errorf("failed to parse initialize result: %w", err)
	}

	// Send initialized notification
	c.notify("notifications/initialized", nil)

	return nil
}

func (c *Client) handleResponses() {
	for c.stdout.Scan() {
		line := c.stdout.Bytes()
		if len(line) == 0 {
			continue
		}

		var resp Response
		if err := json.Unmarshal(line, &resp); err != nil {
			continue
		}

		c.mu.Lock()
		ch, ok := c.pending[resp.ID]
		c.mu.Unlock()

		if ok {
			select {
			case ch <- &resp:
			default:
			}
		}
	}
}

func (c *Client) handleStderr() {
	scanner := bufio.NewScanner(c.stderr)
	for scanner.Scan() {
		// Log stderr output for debugging
		fmt.Fprintf(os.Stderr, "[MCP Server] %s\n", scanner.Text())
	}
}

func (c *Client) call(method string, params interface{}) (*Response, error) {
	c.mu.Lock()
	c.requestID++
	id := c.requestID
	c.mu.Unlock()

	req := Request{
		JSONRPC: "2.0",
		ID:      id,
		Method:  method,
		Params:  params,
	}

	data, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	ch := make(chan *Response, 1)
	c.mu.Lock()
	c.pending[id] = ch
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()

	if _, err := fmt.Fprintf(c.stdin, "%s\n", data); err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}

	select {
	case resp := <-ch:
		if resp.Error != nil {
			return nil, fmt.Errorf("MCP error: %s (code: %d)", resp.Error.Message, resp.Error.Code)
		}
		return resp, nil
	case <-time.After(30 * time.Second):
		return nil, fmt.Errorf("request timeout")
	}
}

func (c *Client) notify(method string, params interface{}) error {
	req := Request{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
	}

	data, err := json.Marshal(req)
	if err != nil {
		return err
	}

	_, err = fmt.Fprintf(c.stdin, "%s\n", data)
	return err
}

// ValidateConnection checks if MCP server is usable
func (c *Client) ValidateConnection() (*StatusReport, error) {
	report := &StatusReport{
		ServerName: c.def.Name,
		Version:    c.def.Version,
		Transport:  c.def.Transport.Type,
		Tools:      c.def.Tools,
		ToolsCount: len(c.def.Tools),
	}

	if !c.connected {
		if err := c.Connect(); err != nil {
			report.ErrorMessage = err.Error()
			return report, err
		}
	}

	// Try to list tools from server
	resp, err := c.call("tools/list", nil)
	if err != nil {
		report.ErrorMessage = err.Error()
		return report, err
	}

	var toolsResult ToolsListResult
	if err := json.Unmarshal(resp.Result, &toolsResult); err != nil {
		// If server doesn't support tools/list, use definition tools
		report.Connected = true
		return report, nil
	}

	// Update with server-provided tools if available
	if len(toolsResult.Tools) > 0 {
		report.Tools = toolsResult.Tools
		report.ToolsCount = len(toolsResult.Tools)
	}

	report.Connected = true
	return report, nil
}

// ListTools returns available tools
func (c *Client) ListTools() ([]ToolDefinition, error) {
	if !c.connected {
		return nil, fmt.Errorf("not connected to MCP server")
	}

	resp, err := c.call("tools/list", nil)
	if err != nil {
		// Fallback to definition tools
		return c.def.Tools, nil
	}

	var result ToolsListResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return c.def.Tools, nil
	}

	return result.Tools, nil
}

// CallTool executes a tool with given parameters
func (c *Client) CallTool(name string, arguments map[string]interface{}) (json.RawMessage, error) {
	if !c.connected {
		return nil, fmt.Errorf("not connected to MCP server")
	}

	params := CallToolParams{
		Name:      name,
		Arguments: arguments,
	}

	resp, err := c.call("tools/call", params)
	if err != nil {
		return nil, err
	}

	var toolResult struct {
		Content []struct {
			Type string          `json:"type"`
			Text string          `json:"text,omitempty"`
			Data json.RawMessage `json:"data,omitempty"`
		} `json:"content"`
		IsError bool `json:"isError,omitempty"`
	}

	if err := json.Unmarshal(resp.Result, &toolResult); err != nil {
		return resp.Result, nil
	}

	if toolResult.IsError {
		return nil, fmt.Errorf("tool execution error")
	}

	// Return first content item's text or data
	if len(toolResult.Content) > 0 {
		if toolResult.Content[0].Text != "" {
			return json.RawMessage(fmt.Sprintf(`"%s"`, toolResult.Content[0].Text)), nil
		}
		return toolResult.Content[0].Data, nil
	}

	return resp.Result, nil
}

// Disconnect closes connection to MCP server
func (c *Client) Disconnect() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.connected {
		return nil
	}

	c.connected = false

	if c.stdin != nil {
		c.stdin.Close()
	}

	if c.cmd != nil {
		c.cmd.Process.Kill()
		c.cmd.Wait()
	}

	return nil
}

// GetDefinition returns the loaded MCP definition
func (c *Client) GetDefinition() *MCPDefinition {
	return c.def
}

