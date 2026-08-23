package agentcore

import (
	"encoding/json"
	"fmt"

	sigsyaml "sigs.k8s.io/yaml"
)

// ArenaConfig holds the subset of the PromptKit arena config that the
// adapter needs for infrastructure decisions.
type ArenaConfig struct {
	ToolSpecs map[string]*ArenaToolSpec `json:"tool_specs,omitempty"`

	// LoadedTools carries tools declared as file references rather than
	// inline. The arena writes an inline tool_specs entry into both places,
	// but a `tools: [./x.tool.yaml]` reference lands only here — so reading
	// tool_specs alone makes those tools invisible, and a tool the adapter
	// cannot see is one the deployed agent cannot use.
	LoadedTools     []ArenaToolData           `json:"loaded_tools,omitempty"`
	MCPServers      []ArenaMCPServer          `json:"mcp_servers,omitempty"`
	LoadedProviders map[string]*ArenaProvider `json:"loaded_providers,omitempty"`
	ProviderSpecs   map[string]*ArenaProvider `json:"provider_specs,omitempty"`
}

// ArenaToolData is a tool manifest the arena loaded from a file.
type ArenaToolData struct {
	FilePath string `json:"file_path,omitempty"`
	Data     []byte `json:"data,omitempty"`
}

// arenaToolManifest is the envelope of a `kind: Tool` YAML manifest.
type arenaToolManifest struct {
	Metadata struct {
		Name string `json:"name"`
	} `json:"metadata"`
	Spec ArenaToolSpec `json:"spec"`
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

	// MockResult and MockTemplate hold what a mock tool answers with. They
	// are not in the compiled pack — it carries only a tool's schema — so
	// they reach a deployed agent through here or not at all.
	MockResult   any    `json:"mock_result,omitempty"`
	MockTemplate string `json:"mock_template,omitempty"`
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

// firstProvider returns the lowest-keyed provider found in the arena config,
// checking LoadedProviders first, then ProviderSpecs, along with its key.
// Returns ("", nil) if there are none.
//
// Selection is sorted rather than map-iteration order: arena configs routinely
// declare several providers, and picking whichever the map yielded made the
// deployed model vary run to run.
func (a *ArenaConfig) firstProvider() (string, *ArenaProvider) {
	if a == nil {
		return "", nil
	}
	for _, m := range []map[string]*ArenaProvider{a.LoadedProviders, a.ProviderSpecs} {
		if name := lowestKey(m); name != "" {
			return name, m[name]
		}
	}
	return "", nil
}

// providerByName looks a provider up by key in LoadedProviders, then
// ProviderSpecs. Returns nil for an empty name or no match.
func (a *ArenaConfig) providerByName(name string) *ArenaProvider {
	if a == nil || name == "" {
		return nil
	}
	if p, ok := a.LoadedProviders[name]; ok {
		return p
	}
	return a.ProviderSpecs[name]
}

// lowestKey returns the lexicographically smallest key of m whose value is
// non-nil, or "" if there is none.
func lowestKey(m map[string]*ArenaProvider) string {
	best := ""
	for k, v := range m {
		if v == nil {
			continue
		}
		if best == "" || k < best {
			best = k
		}
	}
	return best
}

// toolSpecForName returns the tool spec with the given name, or nil if
// not found.
func (a *ArenaConfig) toolSpecForName(name string) *ArenaToolSpec {
	if a == nil {
		return nil
	}
	if spec, ok := a.ToolSpecs[name]; ok {
		return spec
	}
	// Inline specs win: the arena copies them into loaded_tools as well, so
	// reaching here means the tool was declared only as a file reference.
	return a.loadedToolSpec(name)
}

// loadedToolSpec finds a file-declared tool by name.
func (a *ArenaConfig) loadedToolSpec(name string) *ArenaToolSpec {
	for i := range a.LoadedTools {
		toolName, spec := parseToolManifest(a.LoadedTools[i].Data)
		if toolName == name && spec != nil {
			return spec
		}
	}
	return nil
}

// parseToolManifest reads a `kind: Tool` YAML manifest.
//
// A manifest that does not parse, or that names no tool, is skipped rather
// than failing: one malformed tool file should not take a deploy with it.
func parseToolManifest(data []byte) (name string, spec *ArenaToolSpec) {
	if len(data) == 0 {
		return "", nil
	}
	asJSON, err := sigsyaml.YAMLToJSON(data)
	if err != nil {
		return "", nil
	}

	var manifest arenaToolManifest
	if json.Unmarshal(asJSON, &manifest) != nil {
		return "", nil
	}

	name = manifest.Spec.Name
	if name == "" {
		name = manifest.Metadata.Name
	}
	if name == "" {
		return "", nil
	}
	out := manifest.Spec
	if out.Name == "" {
		out.Name = name
	}
	return name, &out
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
