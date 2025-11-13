package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
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
	httpClient *http.Client
	httpURL    string
	httpStream io.ReadCloser
	sseURL     string
	sseStream  io.ReadCloser
	sseCancel  context.CancelFunc
	connected  bool
	serverInfo *ServerInfo
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
	Code    int         `json:"code"`
	Message string      `json:"message"`
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
		// Check if this is actually SSE (based on Accept header requirement)
		if c.def.Transport.HTTP != nil {
			// Try SSE first if server requires text/event-stream
			return c.connectSSE()
		}
		return c.connectHTTP()
	case "sse":
		return c.connectSSE()
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

func (c *Client) connectSSE() error {
	// Determine base URL and SSE URL
	var baseURL string
	var sseURL string

	if c.def.Transport.SSE != nil && c.def.Transport.SSE.URL != "" {
		sseURL = c.def.Transport.SSE.URL
		baseURL = sseURL
	} else if c.def.Transport.HTTP != nil && c.def.Transport.HTTP.URL != "" {
		baseURL = c.def.Transport.HTTP.URL
		// Try different SSE endpoint patterns
		if strings.HasSuffix(baseURL, "/mcp") {
			sseURL = strings.TrimSuffix(baseURL, "/mcp") + "/messages"
		} else {
			sseURL = baseURL + "/messages"
		}
	} else {
		return fmt.Errorf("sse.url or http.url is required for SSE transport")
	}

	c.httpClient = &http.Client{
		Timeout: 0, // No timeout for SSE connections
	}
	c.httpURL = baseURL
	c.sseURL = sseURL

	// For SSE with Playwright MCP, we establish connection via POST to base URL
	// The server responds with SSE stream
	ctx, cancel := context.WithCancel(context.Background())
	c.sseCancel = cancel

	// Create initialize request
	initReq := Request{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "initialize",
		Params: map[string]interface{}{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]interface{}{},
			"clientInfo": map[string]interface{}{
				"name":    "chrome-agent",
				"version": "1.0.0",
			},
		},
	}

	data, err := json.Marshal(initReq)
	if err != nil {
		return fmt.Errorf("failed to marshal initialize request: %w", err)
	}

	// POST to base URL with SSE Accept header
	req, err := http.NewRequestWithContext(ctx, "POST", baseURL, bytes.NewBuffer(data))
	if err != nil {
		return fmt.Errorf("failed to create SSE request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")
	if c.def.Transport.SSE != nil && c.def.Transport.SSE.Headers != nil {
		for k, v := range c.def.Transport.SSE.Headers {
			req.Header.Set(k, v)
		}
	} else if c.def.Transport.HTTP != nil && c.def.Transport.HTTP.Headers != nil {
		for k, v := range c.def.Transport.HTTP.Headers {
			req.Header.Set(k, v)
		}
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("SSE connection failed: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return fmt.Errorf("SSE connection failed with status: %d, body: %s", resp.StatusCode, string(bodyBytes))
	}

	// Check if response is actually SSE stream
	contentType := resp.Header.Get("Content-Type")
	if !strings.Contains(contentType, "text/event-stream") {
		// Not SSE, might be regular JSON response
		bodyBytes, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return fmt.Errorf("expected SSE stream but got content-type: %s, body: %s", contentType, string(bodyBytes))
	}

	c.sseURL = baseURL

	// Store SSE stream
	c.sseStream = resp.Body

	// Start reading SSE events
	go c.handleSSEStream()

	// Wait for initialize response (we already sent it in the POST)
	ch := make(chan *Response, 1)
	c.mu.Lock()
	c.pending[1] = ch
	c.mu.Unlock()

	select {
	case initResp := <-ch:
		c.mu.Lock()
		delete(c.pending, 1)
		c.mu.Unlock()

		if initResp.Error != nil {
			c.sseStream.Close()
			return fmt.Errorf("initialize failed: %s (code: %d)", initResp.Error.Message, initResp.Error.Code)
		}

		var initResult InitializeResult
		if err := json.Unmarshal(initResp.Result, &initResult); err != nil {
			c.sseStream.Close()
			return fmt.Errorf("failed to parse initialize result: %w", err)
		}

		// Store server info
		c.serverInfo = &initResult.ServerInfo
		if c.def.Name == "" || c.def.Name == "playwright" {
			c.def.Name = initResult.ServerInfo.Name
		}
		if c.def.Version == "" || c.def.Version == "1.0.0" {
			c.def.Version = initResult.ServerInfo.Version
		}

		// Send initialized notification
		c.notify("notifications/initialized", nil)

		c.connected = true
		return nil
	case <-time.After(10 * time.Second):
		c.mu.Lock()
		delete(c.pending, 1)
		c.mu.Unlock()
		c.sseStream.Close()
		return fmt.Errorf("timeout waiting for initialize response")
	}
}

func (c *Client) connectHTTP() error {
	if c.def.Transport.HTTP == nil || c.def.Transport.HTTP.URL == "" {
		return fmt.Errorf("http.url is required for http transport")
	}

	c.httpClient = &http.Client{
		Timeout: 0, // No timeout for streaming connections
	}
	c.httpURL = c.def.Transport.HTTP.URL

	// Initialize connection (will use callHTTP which handles streaming)
	if err := c.initialize(); err != nil {
		return fmt.Errorf("failed to initialize MCP connection: %w", err)
	}

	c.connected = true
	return nil
}

func (c *Client) initialize() error {
	initParams := map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]interface{}{},
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

	// Store server info
	c.serverInfo = &initResult.ServerInfo

	// Update definition with server info if not already set
	if c.def.Name == "" || c.def.Name == "playwright" {
		c.def.Name = initResult.ServerInfo.Name
	}
	if c.def.Version == "" || c.def.Version == "1.0.0" {
		c.def.Version = initResult.ServerInfo.Version
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

func (c *Client) handleHTTPStream() {
	if c.httpStream == nil {
		return
	}

	scanner := bufio.NewScanner(c.httpStream)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var resp Response
		if err := json.Unmarshal(line, &resp); err != nil {
			// Try to parse as a single JSON object (non-NDJSON)
			if err := json.Unmarshal(line, &resp); err != nil {
				continue
			}
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

	// Stream closed
	if err := scanner.Err(); err != nil && err != io.EOF {
		fmt.Fprintf(os.Stderr, "[MCP HTTP Stream] Error: %v\n", err)
	}
}

func (c *Client) handleSSEStream() {
	if c.sseStream == nil {
		return
	}

	scanner := bufio.NewScanner(c.sseStream)
	var currentData string
	var expectingData bool

	for scanner.Scan() {
		line := scanner.Text()

		// SSE format from Playwright MCP:
		// event: message
		// data: {json}
		// (empty line)
		if strings.HasPrefix(line, "event: ") {
			// Reset for new message
			currentData = ""
			expectingData = true
		} else if strings.HasPrefix(line, "data: ") && expectingData {
			jsonData := strings.TrimPrefix(line, "data: ")
			currentData = jsonData
		} else if line == "" && currentData != "" {
			// Empty line after data indicates end of SSE message
			var resp Response
			if err := json.Unmarshal([]byte(currentData), &resp); err != nil {
				currentData = ""
				expectingData = false
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

			currentData = ""
			expectingData = false
		}
	}

	// Handle any remaining data
	if currentData != "" {
		var resp Response
		if err := json.Unmarshal([]byte(currentData), &resp); err == nil {
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

	// Stream closed
	if err := scanner.Err(); err != nil && err != io.EOF {
		fmt.Fprintf(os.Stderr, "[MCP SSE Stream] Error: %v\n", err)
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

	// Use HTTP transport if configured
	if c.httpClient != nil {
		return c.callHTTP(data)
	}

	// Use stdio transport
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

func (c *Client) callHTTP(data []byte) (*Response, error) {
	var req Request
	if err := json.Unmarshal(data, &req); err != nil {
		return nil, fmt.Errorf("failed to parse request: %w", err)
	}

	// If SSE is active, send via POST to the base URL and wait for SSE response
	if c.sseStream != nil {
		return c.callSSE(data, req.ID)
	}

	// Regular HTTP POST request
	httpReq, err := http.NewRequest("POST", c.httpURL, bytes.NewBuffer(data))
	if err != nil {
		return nil, fmt.Errorf("failed to create HTTP request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json, text/event-stream")
	if c.def.Transport.HTTP != nil && c.def.Transport.HTTP.Headers != nil {
		for k, v := range c.def.Transport.HTTP.Headers {
			httpReq.Header.Set(k, v)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	httpReq = httpReq.WithContext(ctx)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("HTTP request failed with status: %d, body: %s", resp.StatusCode, string(bodyBytes))
	}

	// Read the entire response body first (streamable HTTP may send all at once)
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	// Try parsing as a single JSON object first
	var mcpResp Response
	if err := json.Unmarshal(bodyBytes, &mcpResp); err == nil {
		// Single JSON object response
		if mcpResp.ID == req.ID {
			if mcpResp.Error != nil {
				return nil, fmt.Errorf("MCP error: %s (code: %d)", mcpResp.Error.Message, mcpResp.Error.Code)
			}
			return &mcpResp, nil
		}
	}

	// Try parsing as newline-delimited JSON (NDJSON)
	scanner := bufio.NewScanner(bytes.NewReader(bodyBytes))
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var mcpResp Response
		if err := json.Unmarshal(line, &mcpResp); err != nil {
			continue
		}

		// Match response ID with request ID
		if mcpResp.ID == req.ID {
			if mcpResp.Error != nil {
				return nil, fmt.Errorf("MCP error: %s (code: %d)", mcpResp.Error.Message, mcpResp.Error.Code)
			}
			return &mcpResp, nil
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("failed to read response stream: %w, body: %s", err, string(bodyBytes))
	}

	return nil, fmt.Errorf("no matching response found for request ID %d, body: %s", req.ID, string(bodyBytes))
}

func (c *Client) callSSE(data []byte, requestID int64) (*Response, error) {
	// For SSE, send POST request to base URL (not /messages)
	baseURL := c.sseURL
	if strings.HasSuffix(baseURL, "/messages") {
		baseURL = strings.TrimSuffix(baseURL, "/messages")
	}
	if baseURL == "" && c.def.Transport.HTTP != nil {
		baseURL = c.def.Transport.HTTP.URL
	}

	httpReq, err := http.NewRequest("POST", baseURL, bytes.NewBuffer(data))
	if err != nil {
		return nil, fmt.Errorf("failed to create SSE POST request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	if c.def.Transport.HTTP != nil && c.def.Transport.HTTP.Headers != nil {
		for k, v := range c.def.Transport.HTTP.Headers {
			httpReq.Header.Set(k, v)
		}
	}

	// Send the request (response will come via SSE stream)
	_, err = c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("SSE POST request failed: %w", err)
	}

	// Wait for response on SSE stream
	ch := make(chan *Response, 1)
	c.mu.Lock()
	c.pending[requestID] = ch
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		delete(c.pending, requestID)
		c.mu.Unlock()
	}()

	select {
	case mcpResp := <-ch:
		if mcpResp.Error != nil {
			return nil, fmt.Errorf("MCP error: %s (code: %d)", mcpResp.Error.Message, mcpResp.Error.Code)
		}
		return mcpResp, nil
	case <-time.After(30 * time.Second):
		return nil, fmt.Errorf("request timeout waiting for SSE response")
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

	// Use HTTP/SSE transport if configured
	if c.httpClient != nil {
		baseURL := c.httpURL
		if c.sseStream != nil && c.sseURL != "" {
			// For SSE, send to base URL (not /messages)
			baseURL = c.sseURL
			if strings.HasSuffix(baseURL, "/messages") {
				baseURL = strings.TrimSuffix(baseURL, "/messages")
			}
			if baseURL == "" && c.def.Transport.HTTP != nil {
				baseURL = c.def.Transport.HTTP.URL
			}
		}

		httpReq, err := http.NewRequest("POST", baseURL, bytes.NewBuffer(data))
		if err != nil {
			return err
		}
		httpReq.Header.Set("Content-Type", "application/json")
		if c.def.Transport.HTTP != nil && c.def.Transport.HTTP.Headers != nil {
			for k, v := range c.def.Transport.HTTP.Headers {
				httpReq.Header.Set(k, v)
			}
		}
		resp, err := c.httpClient.Do(httpReq)
		if err != nil {
			return err
		}
		resp.Body.Close()
		return nil
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

	// Use server info from initialize response if available
	if c.serverInfo != nil {
		report.ServerName = c.serverInfo.Name
		report.Version = c.serverInfo.Version
	}

	// Try to list tools from server
	resp, err := c.call("tools/list", nil)
	if err != nil {
		report.ErrorMessage = err.Error()
		// Still mark as connected if we got past initialize
		report.Connected = true
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
		// Update definition with fetched tools for future reference
		c.def.Tools = toolsResult.Tools
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

	// Close HTTP stream if open
	if c.httpStream != nil {
		c.httpStream.Close()
		c.httpStream = nil
	}

	// Close SSE stream if open
	if c.sseStream != nil {
		c.sseStream.Close()
		c.sseStream = nil
	}

	if c.sseCancel != nil {
		c.sseCancel()
		c.sseCancel = nil
	}

	// HTTP client doesn't need explicit cleanup
	c.httpClient = nil
	c.httpURL = ""
	c.sseURL = ""

	return nil
}

// GetDefinition returns the loaded MCP definition
func (c *Client) GetDefinition() *MCPDefinition {
	return c.def
}
