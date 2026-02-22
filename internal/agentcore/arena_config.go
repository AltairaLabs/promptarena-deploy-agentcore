package agentcore

import (
	"encoding/json"
	"fmt"
)

// ArenaConfig holds the subset of the PromptKit arena config that the
// adapter needs for infrastructure decisions.
type ArenaConfig struct {
	ToolSpecs       map[string]*ArenaToolSpec `json:"tool_specs,omitempty"`
	MCPServers      []ArenaMCPServer          `json:"mcp_servers,omitempty"`
	LoadedProviders map[string]*ArenaProvider `json:"loaded_providers,omitempty"`
	ProviderSpecs   map[string]*ArenaProvider `json:"provider_specs,omitempty"`
}

// ArenaProvider describes a provider from the arena config.
type ArenaProvider struct {
	ID    string `json:"id,omitempty"`
	Type  string `json:"type"`
	Model string `json:"model"`
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

// firstProvider returns the first provider found in the arena config,
// checking LoadedProviders first, then ProviderSpecs. Returns nil if none.
func (a *ArenaConfig) firstProvider() *ArenaProvider {
	if a == nil {
		return nil
	}
	for _, p := range a.LoadedProviders {
		return p
	}
	for _, p := range a.ProviderSpecs {
		return p
	}
	return nil
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

// mergeToolTargets copies provider-specific target fields from deploy
// config tool_targets into the ArenaConfig tool specs. This lets users
// keep AWS-specific fields (lambda_arn, api_gateway, etc.) in the deploy
// section rather than polluting the PromptKit arena config.
func mergeToolTargets(arena *ArenaConfig, targets map[string]*ArenaToolSpec) {
	if len(targets) == 0 || arena == nil {
		return
	}
	if arena.ToolSpecs == nil {
		arena.ToolSpecs = make(map[string]*ArenaToolSpec)
	}
	for name, target := range targets {
		existing := arena.ToolSpecs[name]
		if existing == nil {
			arena.ToolSpecs[name] = target
			continue
		}
		mergeTargetFields(existing, target)
	}
}

// mergeTargetFields copies non-zero target-specific fields from src into dst.
func mergeTargetFields(dst, src *ArenaToolSpec) {
	if src.LambdaARN != "" {
		dst.LambdaARN = src.LambdaARN
	}
	if src.APIGateway != nil {
		dst.APIGateway = src.APIGateway
	}
	if src.OpenAPI != nil {
		dst.OpenAPI = src.OpenAPI
	}
	if src.Smithy != nil {
		dst.Smithy = src.Smithy
	}
	if src.Credential != nil {
		dst.Credential = src.Credential
	}
	if src.HTTPConfig != nil {
		dst.HTTPConfig = src.HTTPConfig
	}
}
