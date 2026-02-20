package agentcore

import (
	"encoding/json"
	"fmt"
)

// ArenaConfig holds the subset of the PromptKit arena config that the
// adapter needs for infrastructure decisions.
type ArenaConfig struct {
	ToolSpecs  map[string]*ArenaToolSpec `json:"tool_specs,omitempty"`
	MCPServers []ArenaMCPServer          `json:"mcp_servers,omitempty"`
}

// ArenaToolSpec describes a single tool from the arena config.
type ArenaToolSpec struct {
	Name        string           `json:"name,omitempty"`
	Description string           `json:"description,omitempty"`
	Mode        string           `json:"mode,omitempty"` // "mock" | "live" | "mcp" | "a2a"
	InputSchema any              `json:"input_schema,omitempty"`
	HTTPConfig  *ArenaHTTPConfig `json:"http,omitempty"`
}

// ArenaHTTPConfig holds HTTP-specific tool configuration.
type ArenaHTTPConfig struct {
	URL    string `json:"url,omitempty"`
	Method string `json:"method,omitempty"`
}

// ArenaMCPServer describes an MCP server from the arena config.
type ArenaMCPServer struct {
	Name    string   `json:"name,omitempty"`
	Command string   `json:"command,omitempty"`
	Args    []string `json:"args,omitempty"`
}

// toolSpecForName returns the tool spec with the given name, or nil if
// not found.
func (a *ArenaConfig) toolSpecForName(name string) *ArenaToolSpec {
	if a == nil || a.ToolSpecs == nil {
		return nil
	}
	return a.ToolSpecs[name]
}

// parseArenaConfig deserializes the arena config JSON string.
func parseArenaConfig(raw string) (*ArenaConfig, error) {
	if raw == "" {
		return nil, fmt.Errorf("arena_config is required")
	}
	var cfg ArenaConfig
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return nil, fmt.Errorf("invalid arena_config JSON: %w", err)
	}
	return &cfg, nil
}
