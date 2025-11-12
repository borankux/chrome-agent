package mcp

import (
	"encoding/json"
	"fmt"
	"os"
)

// MCPDefinition represents the standard MCP JSON definition structure
type MCPDefinition struct {
	Name        string                 `json:"name"`
	Version     string                 `json:"version"`
	Description string                 `json:"description"`
	Transport   TransportConfig        `json:"transport"`
	Tools       []ToolDefinition       `json:"tools,omitempty"`
	Resources   []ResourceDefinition   `json:"resources,omitempty"`
	Prompts     []PromptDefinition     `json:"prompts,omitempty"`
}

type TransportConfig struct {
	Type    string                 `json:"type"` // "stdio", "http", "sse"
	Command []string               `json:"command,omitempty"`
	Env     map[string]string      `json:"env,omitempty"`
	HTTP    *HTTPTransportConfig   `json:"http,omitempty"`
	SSE     *SSETransportConfig    `json:"sse,omitempty"`
}

type HTTPTransportConfig struct {
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
}

type SSETransportConfig struct {
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
}

type ToolDefinition struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	InputSchema map[string]interface{} `json:"inputSchema"`
}

type ResourceDefinition struct {
	URI         string `json:"uri"`
	Name        string `json:"name"`
	Description string `json:"description"`
	MimeType    string `json:"mimeType,omitempty"`
}

type PromptDefinition struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Arguments   []PromptArgument       `json:"arguments,omitempty"`
}

type PromptArgument struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Required    bool   `json:"required,omitempty"`
}

// StatusReport contains information about MCP server status
type StatusReport struct {
	Connected    bool
	ServerName   string
	Version      string
	ToolsCount   int
	Tools        []ToolDefinition
	Transport    string
	ErrorMessage string
}

// LoadDefinition loads and parses MCP JSON definition from file
func LoadDefinition(path string) (*MCPDefinition, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read MCP definition file: %w", err)
	}

	var def MCPDefinition
	if err := json.Unmarshal(data, &def); err != nil {
		return nil, fmt.Errorf("failed to parse MCP definition JSON: %w", err)
	}

	if err := validateDefinition(&def); err != nil {
		return nil, fmt.Errorf("invalid MCP definition: %w", err)
	}

	return &def, nil
}

func validateDefinition(def *MCPDefinition) error {
	if def.Name == "" {
		return fmt.Errorf("name is required")
	}
	if def.Transport.Type == "" {
		return fmt.Errorf("transport type is required")
	}
	
	switch def.Transport.Type {
	case "stdio":
		if len(def.Transport.Command) == 0 {
			return fmt.Errorf("command is required for stdio transport")
		}
	case "http":
		if def.Transport.HTTP == nil || def.Transport.HTTP.URL == "" {
			return fmt.Errorf("http.url is required for http transport")
		}
	case "sse":
		if def.Transport.SSE == nil || def.Transport.SSE.URL == "" {
			return fmt.Errorf("sse.url is required for sse transport")
		}
	default:
		return fmt.Errorf("unsupported transport type: %s", def.Transport.Type)
	}

	return nil
}

// FormatStatusReport returns a human-readable status report
func FormatStatusReport(report StatusReport) string {
	var statusIcon string
	if report.Connected {
		statusIcon = "✓"
	} else {
		statusIcon = "✗"
	}

	output := fmt.Sprintf("\n=== MCP Server Status Report ===\n")
	output += fmt.Sprintf("Status: %s %s\n", statusIcon, map[bool]string{true: "Connected", false: "Disconnected"}[report.Connected])
	output += fmt.Sprintf("Server: %s\n", report.ServerName)
	output += fmt.Sprintf("Version: %s\n", report.Version)
	output += fmt.Sprintf("Transport: %s\n", report.Transport)
	output += fmt.Sprintf("Tools Available: %d\n", report.ToolsCount)
	
	if report.ErrorMessage != "" {
		output += fmt.Sprintf("Error: %s\n", report.ErrorMessage)
	}

	if len(report.Tools) > 0 {
		output += "\nAvailable Tools:\n"
		for i, tool := range report.Tools {
			output += fmt.Sprintf("  %d. %s - %s\n", i+1, tool.Name, tool.Description)
		}
	}

	output += "================================\n"
	return output
}

