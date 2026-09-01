package agentcore

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockagentcorecontrol/types"

	"github.com/AltairaLabs/PromptKit/runtime/prompt"
)

// hasAWSTarget reports whether a tool declares somewhere in AWS for the Gateway
// to route to.
//
// These four are the only shapes a Gateway target can take. A tool declaring
// none of them — a mock, or a plain HTTP call — has nothing to point a target
// at, and asking AWS to create one fails the whole apply with "The MCP server
// endpoint URL is malformed or has no extractable host". Such tools run inside
// the runtime instead, so they are not the Gateway's business.
func hasAWSTarget(spec *ArenaToolSpec) bool {
	return spec != nil && (spec.LambdaARN != "" ||
		spec.APIGateway != nil ||
		spec.OpenAPI != nil ||
		spec.Smithy != nil)
}

// gatewayToolNames returns the pack's tools that belong in the Gateway, sorted.
//
// Plan and apply must both use this. Deriving the set twice is how the tool
// gateway resources came to disagree before (#116), and a mismatch here shows
// up as a delete-and-create for every tool on every re-apply.
func gatewayToolNames(pack *prompt.Pack, cfg *Config) []string {
	var names []string
	for name := range pack.Tools {
		if hasAWSTarget(cfg.ArenaConfig.toolSpecForName(name)) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// buildTargetConfig returns the SDK TargetConfiguration for a gateway tool.
// When the arena config provides a Lambda ARN for the tool, it builds a
// McpLambdaTargetConfiguration with an inline tool schema so the Cedar
// schema includes the tool's actions. Otherwise it falls back to a plain
// MCP server target.
func buildTargetConfig(name string, cfg *Config) types.TargetConfiguration {
	spec := cfg.ArenaConfig.toolSpecForName(name)

	if spec != nil && spec.LambdaARN != "" {
		return buildLambdaTargetConfig(name, spec, cfg.PackTools)
	}
	if spec != nil && spec.APIGateway != nil {
		return buildAPIGatewayTargetConfig(spec)
	}
	if spec != nil && spec.OpenAPI != nil {
		return buildOpenAPITargetConfig(spec.OpenAPI)
	}
	if spec != nil && spec.Smithy != nil {
		return buildSmithyTargetConfig(spec.Smithy)
	}

	return buildMCPServerTargetConfig(name, spec)
}

// buildLambdaTargetConfig constructs a Lambda-backed MCP target with an
// inline tool schema derived from the pack tool definition.
func buildLambdaTargetConfig(
	name string, spec *ArenaToolSpec, packTools map[string]*prompt.PackTool,
) types.TargetConfiguration {
	toolDefs := buildToolDefinitions(name, spec, packTools)

	return &types.TargetConfigurationMemberMcp{
		Value: &types.McpTargetConfigurationMemberLambda{
			Value: types.McpLambdaTargetConfiguration{
				LambdaArn: aws.String(spec.LambdaARN),
				ToolSchema: &types.ToolSchemaMemberInlinePayload{
					Value: toolDefs,
				},
			},
		},
	}
}

// buildToolDefinitions constructs the ToolDefinition slice for a gateway
// target. It uses the pack tool definition if available, falling back to
// the arena spec's input schema.
func buildToolDefinitions(
	name string, spec *ArenaToolSpec, packTools map[string]*prompt.PackTool,
) []types.ToolDefinition {
	if pt, ok := packTools[name]; ok && pt != nil {
		return []types.ToolDefinition{packToolToToolDefinition(pt)}
	}

	// Fall back to arena spec fields.
	desc := spec.Description
	if desc == "" {
		desc = "Tool " + name
	}
	td := types.ToolDefinition{
		Name:        aws.String(name),
		Description: aws.String(desc),
	}
	if spec.InputSchema != nil {
		td.InputSchema = jsonSchemaToSchemaDefinition(spec.InputSchema)
	}
	return []types.ToolDefinition{td}
}

// packToolToToolDefinition converts a PromptKit PackTool to an SDK
// ToolDefinition for use in a Lambda target's inline tool schema.
func packToolToToolDefinition(tool *prompt.PackTool) types.ToolDefinition {
	td := types.ToolDefinition{
		Name:        aws.String(tool.Name),
		Description: aws.String(tool.Description),
	}
	if tool.Parameters != nil {
		td.InputSchema = jsonSchemaToSchemaDefinition(tool.Parameters)
	}
	return td
}

// jsonSchemaToSchemaDefinition converts a JSON Schema (as any) to
// the SDK's SchemaDefinition type. It handles type, properties, required,
// description, and items fields.
func jsonSchemaToSchemaDefinition(schema any) *types.SchemaDefinition {
	m := toStringMap(schema)
	if m == nil {
		return &types.SchemaDefinition{Type: types.SchemaTypeObject}
	}

	sd := &types.SchemaDefinition{
		Type: mapJSONSchemaType(stringVal(m, "type")),
	}

	if desc := stringVal(m, "description"); desc != "" {
		sd.Description = aws.String(desc)
	}

	if props, ok := m["properties"]; ok {
		sd.Properties = convertProperties(props)
	}

	if req, ok := m["required"]; ok {
		sd.Required = toStringSlice(req)
	}

	if items, ok := m["items"]; ok {
		sd.Items = jsonSchemaToSchemaDefinition(items)
	}

	return sd
}

// convertProperties converts a JSON Schema "properties" map to SDK form.
func convertProperties(props any) map[string]types.SchemaDefinition {
	pm := toStringMap(props)
	if pm == nil {
		return nil
	}
	result := make(map[string]types.SchemaDefinition, len(pm))
	for k, v := range pm {
		if sd := jsonSchemaToSchemaDefinition(v); sd != nil {
			result[k] = *sd
		}
	}
	return result
}

// mapJSONSchemaType maps a JSON Schema type string to the SDK SchemaType enum.
func mapJSONSchemaType(t string) types.SchemaType {
	switch t {
	case "string":
		return types.SchemaTypeString
	case "number":
		return types.SchemaTypeNumber
	case "integer":
		return types.SchemaTypeInteger
	case "boolean":
		return types.SchemaTypeBoolean
	case "array":
		return types.SchemaTypeArray
	case "object", "":
		return types.SchemaTypeObject
	default:
		return types.SchemaTypeObject
	}
}

// buildMCPServerTargetConfig constructs a plain MCP server target.
func buildMCPServerTargetConfig(name string, spec *ArenaToolSpec) types.TargetConfiguration {
	endpoint := resolveToolEndpoint(name, spec)
	return &types.TargetConfigurationMemberMcp{
		Value: &types.McpTargetConfigurationMemberMcpServer{
			Value: types.McpServerTargetConfiguration{
				Endpoint: aws.String(endpoint),
			},
		},
	}
}

// resolveToolEndpoint returns the endpoint URL for a gateway tool target.
// If the tool spec has an HTTP URL, use that. Otherwise fall back to a
// placeholder.
func resolveToolEndpoint(name string, spec *ArenaToolSpec) string {
	if spec != nil && spec.HTTPConfig != nil && spec.HTTPConfig.URL != "" {
		return spec.HTTPConfig.URL
	}
	return fmt.Sprintf("https://%s.mcp.local", name)
}

// buildAPIGatewayTargetConfig constructs an API Gateway-backed target.
func buildAPIGatewayTargetConfig(spec *ArenaToolSpec) types.TargetConfiguration {
	gw := spec.APIGateway

	toolCfg := &types.ApiGatewayToolConfiguration{
		ToolFilters:   make([]types.ApiGatewayToolFilter, 0, len(gw.Filters)),
		ToolOverrides: make([]types.ApiGatewayToolOverride, 0, len(gw.Overrides)),
	}
	for _, f := range gw.Filters {
		methods := make([]types.RestApiMethod, 0, len(f.Methods))
		for _, m := range f.Methods {
			methods = append(methods, mapRestAPIMethod(m))
		}
		toolCfg.ToolFilters = append(toolCfg.ToolFilters, types.ApiGatewayToolFilter{
			FilterPath: aws.String(f.Path),
			Methods:    methods,
		})
	}
	for _, o := range gw.Overrides {
		override := types.ApiGatewayToolOverride{
			Name:   aws.String(o.Name),
			Path:   aws.String(o.Path),
			Method: mapRestAPIMethod(o.Method),
		}
		if o.Description != "" {
			override.Description = aws.String(o.Description)
		}
		toolCfg.ToolOverrides = append(toolCfg.ToolOverrides, override)
	}

	return &types.TargetConfigurationMemberMcp{
		Value: &types.McpTargetConfigurationMemberApiGateway{
			Value: types.ApiGatewayTargetConfiguration{
				RestApiId:                   aws.String(gw.RestAPIID),
				Stage:                       aws.String(gw.Stage),
				ApiGatewayToolConfiguration: toolCfg,
			},
		},
	}
}

// buildOpenAPITargetConfig constructs an OpenAPI schema-backed target.
func buildOpenAPITargetConfig(cfg *ArenaSchemaConfig) types.TargetConfiguration {
	return &types.TargetConfigurationMemberMcp{
		Value: &types.McpTargetConfigurationMemberOpenApiSchema{
			Value: buildAPISchemaConfig(cfg),
		},
	}
}

// buildSmithyTargetConfig constructs a Smithy model-backed target.
func buildSmithyTargetConfig(cfg *ArenaSchemaConfig) types.TargetConfiguration {
	return &types.TargetConfigurationMemberMcp{
		Value: &types.McpTargetConfigurationMemberSmithyModel{
			Value: buildAPISchemaConfig(cfg),
		},
	}
}

// buildAPISchemaConfig converts an ArenaSchemaConfig to the SDK's
// ApiSchemaConfiguration. Inline takes priority over S3.
func buildAPISchemaConfig(cfg *ArenaSchemaConfig) types.ApiSchemaConfiguration {
	if cfg.Inline != "" {
		return &types.ApiSchemaConfigurationMemberInlinePayload{
			Value: cfg.Inline,
		}
	}
	return &types.ApiSchemaConfigurationMemberS3{
		Value: types.S3Configuration{
			Uri: aws.String(cfg.S3URI),
		},
	}
}

// mapRestAPIMethod maps a string HTTP method to the SDK RestApiMethod enum.
func mapRestAPIMethod(method string) types.RestApiMethod {
	switch method {
	case "GET":
		return types.RestApiMethodGet
	case "POST":
		return types.RestApiMethodPost
	case "PUT":
		return types.RestApiMethodPut
	case "DELETE":
		return types.RestApiMethodDelete
	case "PATCH":
		return types.RestApiMethodPatch
	case "HEAD":
		return types.RestApiMethodHead
	case "OPTIONS":
		return types.RestApiMethodOptions
	default:
		return types.RestApiMethod(method)
	}
}

// buildCredentialProviderConfigs returns the credential provider
// configurations for the given tool target. The credential type is read
// from the arena config's Credential field. If not set, targets that
// require credentials (API Gateway, OpenAPI, Smithy) default to
// GATEWAY_IAM_ROLE.
func buildCredentialProviderConfigs(name string, cfg *Config) []types.CredentialProviderConfiguration {
	spec := cfg.ArenaConfig.toolSpecForName(name)
	if spec == nil {
		return nil
	}

	needsCreds := spec.LambdaARN != "" || spec.APIGateway != nil || spec.OpenAPI != nil || spec.Smithy != nil
	if !needsCreds && spec.Credential == nil {
		return nil
	}

	credType := types.CredentialProviderTypeGatewayIamRole
	if spec.Credential != nil {
		credType = types.CredentialProviderType(spec.Credential.Type)
	}

	return []types.CredentialProviderConfiguration{
		{CredentialProviderType: credType},
	}
}

// --- helper functions for JSON Schema conversion ---

// toStringMap converts an any to map[string]any. It handles map[string]any and
// json.RawMessage/[]byte directly, and falls back to a JSON round-trip for
// anything else that marshals to an object.
//
// The fallback is not decoration. PromptKit v1.9.0 made PackTool.Parameters a
// *packspec.ToolParameters, which matches none of the cases above: without it
// this returns nil, jsonSchemaToSchemaDefinition yields a bare object, and every
// gateway tool registers with no properties and no required fields — silently,
// since the type still satisfies the any parameter and compiles.
func toStringMap(v any) map[string]any {
	switch m := v.(type) {
	case nil:
		return nil
	case map[string]any:
		return m
	case json.RawMessage:
		var out map[string]any
		if json.Unmarshal(m, &out) == nil {
			return out
		}
		return nil
	case []byte:
		var out map[string]any
		if json.Unmarshal(m, &out) == nil {
			return out
		}
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var out map[string]any
	if json.Unmarshal(b, &out) == nil {
		return out
	}
	return nil
}

// stringVal extracts a string value from a map.
func stringVal(m map[string]any, key string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// toStringSlice converts an any to []string.
func toStringSlice(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	result := make([]string, 0, len(arr))
	for _, item := range arr {
		if s, ok := item.(string); ok {
			result = append(result, s)
		}
	}
	return result
}
