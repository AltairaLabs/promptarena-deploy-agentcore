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
	Name        string                 `json:"name,omitempty"`
	Description string                 `json:"description,omitempty"`
	Mode        string                 `json:"mode,omitempty"` // "mock" | "live" | "mcp" | "a2a"
	InputSchema any                    `json:"input_schema,omitempty"`
	HTTPConfig  *ArenaHTTPConfig       `json:"http,omitempty"`
	LambdaARN   string                 `json:"lambda_arn,omitempty"`
	APIGateway  *ArenaAPIGatewayConfig `json:"api_gateway,omitempty"`
	OpenAPI     *ArenaSchemaConfig     `json:"openapi,omitempty"`
	Smithy      *ArenaSchemaConfig     `json:"smithy,omitempty"`
	Credential  *ArenaCredentialConfig `json:"credential,omitempty"`
}

// ArenaCredentialConfig specifies the credential provider for a gateway
// target. API Gateway targets use "GATEWAY_IAM_ROLE"; OpenAPI and Smithy
// targets require "OAUTH" or "API_KEY".
type ArenaCredentialConfig struct {
	Type string `json:"type"` // "GATEWAY_IAM_ROLE" | "OAUTH" | "API_KEY"
}

// ArenaHTTPConfig holds HTTP-specific tool configuration.
type ArenaHTTPConfig struct {
	URL    string `json:"url,omitempty"`
	Method string `json:"method,omitempty"`
}

// ArenaAPIGatewayConfig holds API Gateway target configuration.
type ArenaAPIGatewayConfig struct {
	RestAPIID string                    `json:"rest_api_id"`
	Stage     string                    `json:"stage"`
	Filters   []ArenaAPIGatewayFilter   `json:"filters,omitempty"`
	Overrides []ArenaAPIGatewayOverride `json:"overrides,omitempty"`
}

// ArenaAPIGatewayFilter specifies which operations from the REST API to expose.
type ArenaAPIGatewayFilter struct {
	Path    string   `json:"path"`
	Methods []string `json:"methods"`
}

// ArenaAPIGatewayOverride defines an explicit tool with custom name and description.
type ArenaAPIGatewayOverride struct {
	Name        string `json:"name"`
	Path        string `json:"path"`
	Method      string `json:"method"`
	Description string `json:"description,omitempty"`
}

// ArenaSchemaConfig is shared by OpenAPI and Smithy targets.
// Exactly one of Inline or S3URI should be set.
type ArenaSchemaConfig struct {
	Inline string `json:"inline,omitempty"`
	S3URI  string `json:"s3_uri,omitempty"`
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
