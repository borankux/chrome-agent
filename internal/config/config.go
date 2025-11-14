package config

import (
	"encoding/json"
	"fmt"
	"os"
)

// Config represents the agent configuration
type Config struct {
	Proxy *ProxyConfig `json:"proxy,omitempty"`
}

// ProxyConfig represents proxy configuration
type ProxyConfig struct {
	HTTPProxy  string `json:"http_proxy,omitempty"`
	HTTPSProxy string `json:"https_proxy,omitempty"`
	AllProxy   string `json:"all_proxy,omitempty"`
	NoProxy    string `json:"no_proxy,omitempty"`
}

// LoadConfig loads configuration from a JSON file
// Falls back to environment variables if file doesn't exist or proxy section is missing
func LoadConfig(path string) (*Config, error) {
	config := &Config{}

	// Try to load from file
	if path != "" {
		data, err := os.ReadFile(path)
		if err == nil {
			if err := json.Unmarshal(data, config); err != nil {
				return nil, fmt.Errorf("failed to parse config file: %w", err)
			}
		}
		// If file doesn't exist, continue to env var fallback
	}

	// Fallback to environment variables if proxy config not in file
	if config.Proxy == nil {
		config.Proxy = &ProxyConfig{}
	}

	// Load from environment variables (case-insensitive)
	if config.Proxy.HTTPProxy == "" {
		config.Proxy.HTTPProxy = getEnvVar("HTTP_PROXY", "http_proxy")
	}
	if config.Proxy.HTTPSProxy == "" {
		config.Proxy.HTTPSProxy = getEnvVar("HTTPS_PROXY", "https_proxy")
	}
	if config.Proxy.AllProxy == "" {
		config.Proxy.AllProxy = getEnvVar("ALL_PROXY", "all_proxy")
	}
	if config.Proxy.NoProxy == "" {
		config.Proxy.NoProxy = getEnvVar("NO_PROXY", "no_proxy")
	}

	// If all proxy fields are empty, set to nil (no proxy)
	if config.Proxy.HTTPProxy == "" && config.Proxy.HTTPSProxy == "" && config.Proxy.AllProxy == "" {
		config.Proxy = nil
	}

	return config, nil
}

// getEnvVar gets environment variable, trying both uppercase and lowercase
func getEnvVar(upper, lower string) string {
	if val := os.Getenv(upper); val != "" {
		return val
	}
	return os.Getenv(lower)
}

// GetProxyConfig returns the proxy configuration, or nil if not configured
func (c *Config) GetProxyConfig() *ProxyConfig {
	return c.Proxy
}

// HasProxy returns true if any proxy is configured
func (c *Config) HasProxy() bool {
	return c.Proxy != nil && (c.Proxy.HTTPProxy != "" || c.Proxy.HTTPSProxy != "" || c.Proxy.AllProxy != "")
}

