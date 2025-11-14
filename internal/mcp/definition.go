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

// MCPClientConfig represents the MCP client configuration format
type MCPClientConfig struct {
	MCPServers map[string]json.RawMessage `json:"mcpServers,omitempty"`
}

// MCPServerConfig represents a single MCP server configuration
// Supports both formats:
// 1. command as array: {"command": ["npx", "@playwright/mcp"]}
// 2. command as string + args: {"command": "npx", "args": ["@playwright/mcp"]}
type MCPServerConfig struct {
	URL     string            `json:"url,omitempty"`
	Command interface{}       `json:"command,omitempty"` // Can be string or []string
	Args    []string          `json:"args,omitempty"`    // Used when command is a string
	Env     map[string]string `json:"env,omitempty"`
}

// LoadDefinition loads and parses MCP JSON definition from file
// Supports both server definition format and client configuration format
func LoadDefinition(path string) (*MCPDefinition, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read MCP definition file: %w", err)
	}

	// Try parsing as client config format first
	var clientConfig MCPClientConfig
	if err := json.Unmarshal(data, &clientConfig); err == nil && len(clientConfig.MCPServers) > 0 {
		// Extract first server configuration
		var serverName string
		var serverConfigRaw json.RawMessage
		
		for name, config := range clientConfig.MCPServers {
			serverName = name
			serverConfigRaw = config
			break
		}

		// Parse server config (supports both command formats)
		var serverConfig MCPServerConfig
		if err := json.Unmarshal(serverConfigRaw, &serverConfig); err != nil {
			return nil, fmt.Errorf("failed to parse server config: %w", err)
		}

		// Convert to server definition format
		def := &MCPDefinition{
			Name:        serverName,
			Version:     "1.0.0",
			Description: fmt.Sprintf("MCP server: %s", serverName),
		}

		if serverConfig.URL != "" {
			// HTTP transport
			def.Transport = TransportConfig{
				Type: "http",
				HTTP: &HTTPTransportConfig{
					URL: serverConfig.URL,
				},
			}
		} else {
			// Stdio transport - handle both command formats
			var command []string
			
			if serverConfig.Command != nil {
				switch cmd := serverConfig.Command.(type) {
				case string:
					// Format: {"command": "npx", "args": ["@playwright/mcp"]}
					command = []string{cmd}
					if len(serverConfig.Args) > 0 {
						command = append(command, serverConfig.Args...)
					}
				case []interface{}:
					// Format: {"command": ["npx", "@playwright/mcp"]}
					command = make([]string, len(cmd))
					for i, v := range cmd {
						if str, ok := v.(string); ok {
							command[i] = str
						} else {
							return nil, fmt.Errorf("command array must contain strings")
						}
					}
				default:
					return nil, fmt.Errorf("command must be a string or array of strings")
				}
			}

			if len(command) == 0 {
				return nil, fmt.Errorf("no transport configuration found in client config")
			}

			def.Transport = TransportConfig{
				Type:    "stdio",
				Command: command,
				Env:     serverConfig.Env,
			}
		}

		// Tools will be fetched from server during connection
		return def, nil
	}

	// Try parsing as server definition format
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
	if def == nil {
		return fmt.Errorf("definition is nil")
	}
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

	output := "\n=== MCP Server Status Report ===\n"
	output += fmt.Sprintf("Status: %s %s\n", statusIcon, map[bool]string{true: "Connected", false: "Disconnected"}[report.Connected])
	output += fmt.Sprintf("Server: %s\n", report.ServerName)
	output += fmt.Sprintf("Version: %s\n", report.Version)
	output += fmt.Sprintf("Transport: %s\n", report.Transport)
	output += fmt.Sprintf("Tools Available: %d\n", report.ToolsCount)
	
	if report.ErrorMessage != "" {
		output += fmt.Sprintf("Error: %s\n", report.ErrorMessage)
	}

	output += "================================\n"
	return output
}

