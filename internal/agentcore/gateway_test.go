package agentcore

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/bedrockagentcorecontrol/types"

	"github.com/AltairaLabs/PromptKit/runtime/prompt"
)

func TestResolveToolEndpoint(t *testing.T) {
	tests := []struct {
		name     string
		toolName string
		spec     *ArenaToolSpec
		want     string
	}{
		{
			name:     "nil spec falls back to placeholder",
			toolName: "search",
			spec:     nil,
			want:     "https://search.mcp.local",
		},
		{
			name:     "spec with no HTTPConfig falls back to placeholder",
			toolName: "calc",
			spec:     &ArenaToolSpec{Mode: "mock"},
			want:     "https://calc.mcp.local",
		},
		{
			name:     "spec with empty URL falls back to placeholder",
			toolName: "calc",
			spec:     &ArenaToolSpec{HTTPConfig: &ArenaHTTPConfig{Method: "POST"}},
			want:     "https://calc.mcp.local",
		},
		{
			name:     "spec with HTTP URL uses real endpoint",
			toolName: "search",
			spec: &ArenaToolSpec{
				HTTPConfig: &ArenaHTTPConfig{URL: "https://api.example.com/search"},
			},
			want: "https://api.example.com/search",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveToolEndpoint(tt.toolName, tt.spec)
			if got != tt.want {
				t.Errorf("resolveToolEndpoint(%q) = %q, want %q", tt.toolName, got, tt.want)
			}
		})
	}
}

func TestBuildTargetConfig_MCPServer(t *testing.T) {
	tests := []struct {
		name         string
		toolName     string
		cfg          *Config
		wantEndpoint string
	}{
		{
			name:         "nil ArenaConfig falls back to placeholder",
			toolName:     "search",
			cfg:          &Config{},
			wantEndpoint: "https://search.mcp.local",
		},
		{
			name:     "no matching tool spec falls back to placeholder",
			toolName: "unknown",
			cfg: &Config{
				ArenaConfig: &ArenaConfig{
					ToolSpecs: map[string]*ArenaToolSpec{
						"other": {HTTPConfig: &ArenaHTTPConfig{URL: "https://other.example.com"}},
					},
				},
			},
			wantEndpoint: "https://unknown.mcp.local",
		},
		{
			name:     "matching tool spec with HTTP URL uses real endpoint",
			toolName: "search",
			cfg: &Config{
				ArenaConfig: &ArenaConfig{
					ToolSpecs: map[string]*ArenaToolSpec{
						"search": {HTTPConfig: &ArenaHTTPConfig{URL: "https://api.example.com/search"}},
					},
				},
			},
			wantEndpoint: "https://api.example.com/search",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildTargetConfig(tt.toolName, tt.cfg)

			mcpOuter, ok := got.(*types.TargetConfigurationMemberMcp)
			if !ok {
				t.Fatal("expected TargetConfigurationMemberMcp")
			}
			mcpServer, ok := mcpOuter.Value.(*types.McpTargetConfigurationMemberMcpServer)
			if !ok {
				t.Fatal("expected McpTargetConfigurationMemberMcpServer")
			}
			endpoint := *mcpServer.Value.Endpoint
			if endpoint != tt.wantEndpoint {
				t.Errorf("endpoint = %q, want %q", endpoint, tt.wantEndpoint)
			}
		})
	}
}

func TestBuildTargetConfig_Lambda(t *testing.T) {
	cfg := &Config{
		ArenaConfig: &ArenaConfig{
			ToolSpecs: map[string]*ArenaToolSpec{
				"search": {
					LambdaARN:   "arn:aws:lambda:us-west-2:123456789012:function:search-tool",
					Description: "Search the web",
				},
			},
		},
		PackTools: map[string]*prompt.PackTool{
			"search": {
				Name:        "search",
				Description: "Search the web for information",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"query": map[string]any{
							"type":        "string",
							"description": "The search query",
						},
					},
					"required": []any{"query"},
				},
			},
		},
	}

	got := buildTargetConfig("search", cfg)

	mcpOuter, ok := got.(*types.TargetConfigurationMemberMcp)
	if !ok {
		t.Fatal("expected TargetConfigurationMemberMcp")
	}
	lambda, ok := mcpOuter.Value.(*types.McpTargetConfigurationMemberLambda)
	if !ok {
		t.Fatal("expected McpTargetConfigurationMemberLambda")
	}

	if *lambda.Value.LambdaArn != "arn:aws:lambda:us-west-2:123456789012:function:search-tool" {
		t.Errorf("LambdaArn = %q, want search-tool ARN", *lambda.Value.LambdaArn)
	}

	schema, ok := lambda.Value.ToolSchema.(*types.ToolSchemaMemberInlinePayload)
	if !ok {
		t.Fatal("expected ToolSchemaMemberInlinePayload")
	}
	if len(schema.Value) != 1 {
		t.Fatalf("expected 1 tool definition, got %d", len(schema.Value))
	}
	td := schema.Value[0]
	if *td.Name != "search" {
		t.Errorf("tool name = %q, want search", *td.Name)
	}
	if *td.Description != "Search the web for information" {
		t.Errorf("tool description = %q", *td.Description)
	}
	if td.InputSchema == nil {
		t.Fatal("expected InputSchema to be populated")
	}
	if td.InputSchema.Type != types.SchemaTypeObject {
		t.Errorf("InputSchema.Type = %q, want object", td.InputSchema.Type)
	}
	if _, ok := td.InputSchema.Properties["query"]; !ok {
		t.Error("expected 'query' property in InputSchema")
	}
}

func TestBuildTargetConfig_Lambda_FallbackToArenaSpec(t *testing.T) {
	cfg := &Config{
		ArenaConfig: &ArenaConfig{
			ToolSpecs: map[string]*ArenaToolSpec{
				"calc": {
					LambdaARN:   "arn:aws:lambda:us-west-2:123456789012:function:calc",
					Description: "Calculator tool",
					InputSchema: map[string]any{
						"type": "object",
						"properties": map[string]any{
							"expr": map[string]any{
								"type": "string",
							},
						},
					},
				},
			},
		},
		PackTools: map[string]*prompt.PackTool{}, // no pack tool for "calc"
	}

	got := buildTargetConfig("calc", cfg)

	mcpOuter, ok := got.(*types.TargetConfigurationMemberMcp)
	if !ok {
		t.Fatal("expected TargetConfigurationMemberMcp")
	}
	lambda, ok := mcpOuter.Value.(*types.McpTargetConfigurationMemberLambda)
	if !ok {
		t.Fatal("expected McpTargetConfigurationMemberLambda")
	}

	schema, ok := lambda.Value.ToolSchema.(*types.ToolSchemaMemberInlinePayload)
	if !ok {
		t.Fatal("expected ToolSchemaMemberInlinePayload")
	}
	td := schema.Value[0]
	if *td.Name != "calc" {
		t.Errorf("tool name = %q, want calc", *td.Name)
	}
	if *td.Description != "Calculator tool" {
		t.Errorf("tool description = %q", *td.Description)
	}
	if td.InputSchema == nil {
		t.Fatal("expected InputSchema from arena spec")
	}
}

func TestJsonSchemaToSchemaDefinition(t *testing.T) {
	tests := []struct {
		name     string
		schema   any
		wantType types.SchemaType
	}{
		{
			name:     "nil schema defaults to object",
			schema:   nil,
			wantType: types.SchemaTypeObject,
		},
		{
			name:     "empty map defaults to object",
			schema:   map[string]any{},
			wantType: types.SchemaTypeObject,
		},
		{
			name:     "string type",
			schema:   map[string]any{"type": "string"},
			wantType: types.SchemaTypeString,
		},
		{
			name:     "integer type",
			schema:   map[string]any{"type": "integer"},
			wantType: types.SchemaTypeInteger,
		},
		{
			name:     "number type",
			schema:   map[string]any{"type": "number"},
			wantType: types.SchemaTypeNumber,
		},
		{
			name:     "boolean type",
			schema:   map[string]any{"type": "boolean"},
			wantType: types.SchemaTypeBoolean,
		},
		{
			name:     "array type",
			schema:   map[string]any{"type": "array"},
			wantType: types.SchemaTypeArray,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := jsonSchemaToSchemaDefinition(tt.schema)
			if got == nil {
				t.Fatal("expected non-nil SchemaDefinition")
			}
			if got.Type != tt.wantType {
				t.Errorf("Type = %q, want %q", got.Type, tt.wantType)
			}
		})
	}
}

func TestJsonSchemaToSchemaDefinition_NestedProperties(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name": map[string]any{
				"type":        "string",
				"description": "The name",
			},
			"items": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "string",
				},
			},
		},
		"required": []any{"name"},
	}

	got := jsonSchemaToSchemaDefinition(schema)
	if got == nil {
		t.Fatal("expected non-nil SchemaDefinition")
	}

	if len(got.Properties) != 2 {
		t.Fatalf("expected 2 properties, got %d", len(got.Properties))
	}

	nameProp := got.Properties["name"]
	if nameProp.Type != types.SchemaTypeString {
		t.Errorf("name.Type = %q, want string", nameProp.Type)
	}
	if nameProp.Description == nil || *nameProp.Description != "The name" {
		t.Errorf("name.Description = %v, want 'The name'", nameProp.Description)
	}

	itemsProp := got.Properties["items"]
	if itemsProp.Type != types.SchemaTypeArray {
		t.Errorf("items.Type = %q, want array", itemsProp.Type)
	}
	if itemsProp.Items == nil {
		t.Fatal("expected items.Items to be populated")
	}
	if itemsProp.Items.Type != types.SchemaTypeString {
		t.Errorf("items.Items.Type = %q, want string", itemsProp.Items.Type)
	}

	if len(got.Required) != 1 || got.Required[0] != "name" {
		t.Errorf("Required = %v, want [name]", got.Required)
	}
}

func TestMapJSONSchemaType(t *testing.T) {
	tests := []struct {
		input string
		want  types.SchemaType
	}{
		{"string", types.SchemaTypeString},
		{"number", types.SchemaTypeNumber},
		{"integer", types.SchemaTypeInteger},
		{"boolean", types.SchemaTypeBoolean},
		{"array", types.SchemaTypeArray},
		{"object", types.SchemaTypeObject},
		{"", types.SchemaTypeObject},
		{"unknown", types.SchemaTypeObject},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := mapJSONSchemaType(tt.input); got != tt.want {
				t.Errorf("mapJSONSchemaType(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestBuildTargetConfig_APIGateway(t *testing.T) {
	cfg := &Config{
		ArenaConfig: &ArenaConfig{
			ToolSpecs: map[string]*ArenaToolSpec{
				"petstore": {
					APIGateway: &ArenaAPIGatewayConfig{
						RestAPIID: "abc123",
						Stage:     "prod",
						Filters: []ArenaAPIGatewayFilter{
							{Path: "/pets/*", Methods: []string{"GET", "POST"}},
						},
						Overrides: []ArenaAPIGatewayOverride{
							{
								Name:        "list-pets",
								Path:        "/pets",
								Method:      "GET",
								Description: "List all pets",
							},
						},
					},
				},
			},
		},
	}

	got := buildTargetConfig("petstore", cfg)

	mcpOuter, ok := got.(*types.TargetConfigurationMemberMcp)
	if !ok {
		t.Fatal("expected TargetConfigurationMemberMcp")
	}
	apigw, ok := mcpOuter.Value.(*types.McpTargetConfigurationMemberApiGateway)
	if !ok {
		t.Fatal("expected McpTargetConfigurationMemberApiGateway")
	}

	if *apigw.Value.RestApiId != "abc123" {
		t.Errorf("RestApiId = %q, want abc123", *apigw.Value.RestApiId)
	}
	if *apigw.Value.Stage != "prod" {
		t.Errorf("Stage = %q, want prod", *apigw.Value.Stage)
	}

	toolCfg := apigw.Value.ApiGatewayToolConfiguration
	if toolCfg == nil {
		t.Fatal("expected ApiGatewayToolConfiguration")
	}
	if len(toolCfg.ToolFilters) != 1 {
		t.Fatalf("expected 1 filter, got %d", len(toolCfg.ToolFilters))
	}
	f := toolCfg.ToolFilters[0]
	if *f.FilterPath != "/pets/*" {
		t.Errorf("FilterPath = %q, want /pets/*", *f.FilterPath)
	}
	if len(f.Methods) != 2 {
		t.Fatalf("expected 2 methods, got %d", len(f.Methods))
	}
	if f.Methods[0] != types.RestApiMethodGet {
		t.Errorf("Methods[0] = %q, want GET", f.Methods[0])
	}
	if f.Methods[1] != types.RestApiMethodPost {
		t.Errorf("Methods[1] = %q, want POST", f.Methods[1])
	}

	if len(toolCfg.ToolOverrides) != 1 {
		t.Fatalf("expected 1 override, got %d", len(toolCfg.ToolOverrides))
	}
	o := toolCfg.ToolOverrides[0]
	if *o.Name != "list-pets" {
		t.Errorf("Name = %q, want list-pets", *o.Name)
	}
	if *o.Path != "/pets" {
		t.Errorf("Path = %q, want /pets", *o.Path)
	}
	if o.Method != types.RestApiMethodGet {
		t.Errorf("Method = %q, want GET", o.Method)
	}
	if *o.Description != "List all pets" {
		t.Errorf("Description = %q, want 'List all pets'", *o.Description)
	}
}

func TestBuildTargetConfig_OpenAPI_Inline(t *testing.T) {
	payload := `{"openapi":"3.0.0","info":{"title":"Test","version":"1.0"}}`
	cfg := &Config{
		ArenaConfig: &ArenaConfig{
			ToolSpecs: map[string]*ArenaToolSpec{
				"api-tool": {
					OpenAPI: &ArenaSchemaConfig{Inline: payload},
				},
			},
		},
	}

	got := buildTargetConfig("api-tool", cfg)

	mcpOuter, ok := got.(*types.TargetConfigurationMemberMcp)
	if !ok {
		t.Fatal("expected TargetConfigurationMemberMcp")
	}
	openapi, ok := mcpOuter.Value.(*types.McpTargetConfigurationMemberOpenApiSchema)
	if !ok {
		t.Fatal("expected McpTargetConfigurationMemberOpenApiSchema")
	}
	inline, ok := openapi.Value.(*types.ApiSchemaConfigurationMemberInlinePayload)
	if !ok {
		t.Fatal("expected ApiSchemaConfigurationMemberInlinePayload")
	}
	if inline.Value != payload {
		t.Errorf("inline payload = %q, want %q", inline.Value, payload)
	}
}

func TestBuildTargetConfig_OpenAPI_S3(t *testing.T) {
	cfg := &Config{
		ArenaConfig: &ArenaConfig{
			ToolSpecs: map[string]*ArenaToolSpec{
				"api-tool": {
					OpenAPI: &ArenaSchemaConfig{S3URI: "s3://bucket/openapi.json"},
				},
			},
		},
	}

	got := buildTargetConfig("api-tool", cfg)

	mcpOuter, ok := got.(*types.TargetConfigurationMemberMcp)
	if !ok {
		t.Fatal("expected TargetConfigurationMemberMcp")
	}
	openapi, ok := mcpOuter.Value.(*types.McpTargetConfigurationMemberOpenApiSchema)
	if !ok {
		t.Fatal("expected McpTargetConfigurationMemberOpenApiSchema")
	}
	s3, ok := openapi.Value.(*types.ApiSchemaConfigurationMemberS3)
	if !ok {
		t.Fatal("expected ApiSchemaConfigurationMemberS3")
	}
	if *s3.Value.Uri != "s3://bucket/openapi.json" {
		t.Errorf("S3 URI = %q, want s3://bucket/openapi.json", *s3.Value.Uri)
	}
}

func TestBuildTargetConfig_Smithy_Inline(t *testing.T) {
	payload := `namespace example\nservice PetStore {}`
	cfg := &Config{
		ArenaConfig: &ArenaConfig{
			ToolSpecs: map[string]*ArenaToolSpec{
				"smithy-tool": {
					Smithy: &ArenaSchemaConfig{Inline: payload},
				},
			},
		},
	}

	got := buildTargetConfig("smithy-tool", cfg)

	mcpOuter, ok := got.(*types.TargetConfigurationMemberMcp)
	if !ok {
		t.Fatal("expected TargetConfigurationMemberMcp")
	}
	smithy, ok := mcpOuter.Value.(*types.McpTargetConfigurationMemberSmithyModel)
	if !ok {
		t.Fatal("expected McpTargetConfigurationMemberSmithyModel")
	}
	inline, ok := smithy.Value.(*types.ApiSchemaConfigurationMemberInlinePayload)
	if !ok {
		t.Fatal("expected ApiSchemaConfigurationMemberInlinePayload")
	}
	if inline.Value != payload {
		t.Errorf("inline payload = %q, want %q", inline.Value, payload)
	}
}

func TestBuildTargetConfig_Smithy_S3(t *testing.T) {
	cfg := &Config{
		ArenaConfig: &ArenaConfig{
			ToolSpecs: map[string]*ArenaToolSpec{
				"smithy-tool": {
					Smithy: &ArenaSchemaConfig{S3URI: "s3://bucket/model.smithy"},
				},
			},
		},
	}

	got := buildTargetConfig("smithy-tool", cfg)

	mcpOuter, ok := got.(*types.TargetConfigurationMemberMcp)
	if !ok {
		t.Fatal("expected TargetConfigurationMemberMcp")
	}
	smithy, ok := mcpOuter.Value.(*types.McpTargetConfigurationMemberSmithyModel)
	if !ok {
		t.Fatal("expected McpTargetConfigurationMemberSmithyModel")
	}
	s3, ok := smithy.Value.(*types.ApiSchemaConfigurationMemberS3)
	if !ok {
		t.Fatal("expected ApiSchemaConfigurationMemberS3")
	}
	if *s3.Value.Uri != "s3://bucket/model.smithy" {
		t.Errorf("S3 URI = %q, want s3://bucket/model.smithy", *s3.Value.Uri)
	}
}

func TestBuildAPISchemaConfig(t *testing.T) {
	t.Run("inline takes priority", func(t *testing.T) {
		cfg := &ArenaSchemaConfig{Inline: "payload", S3URI: "s3://bucket/file"}
		got := buildAPISchemaConfig(cfg)
		if _, ok := got.(*types.ApiSchemaConfigurationMemberInlinePayload); !ok {
			t.Fatal("expected inline when both are set")
		}
	})

	t.Run("s3 when no inline", func(t *testing.T) {
		cfg := &ArenaSchemaConfig{S3URI: "s3://bucket/file"}
		got := buildAPISchemaConfig(cfg)
		s3, ok := got.(*types.ApiSchemaConfigurationMemberS3)
		if !ok {
			t.Fatal("expected S3")
		}
		if *s3.Value.Uri != "s3://bucket/file" {
			t.Errorf("URI = %q, want s3://bucket/file", *s3.Value.Uri)
		}
	})
}

func TestMapRestAPIMethod(t *testing.T) {
	tests := []struct {
		input string
		want  types.RestApiMethod
	}{
		{"GET", types.RestApiMethodGet},
		{"POST", types.RestApiMethodPost},
		{"PUT", types.RestApiMethodPut},
		{"DELETE", types.RestApiMethodDelete},
		{"PATCH", types.RestApiMethodPatch},
		{"HEAD", types.RestApiMethodHead},
		{"OPTIONS", types.RestApiMethodOptions},
		{"CUSTOM", types.RestApiMethod("CUSTOM")},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := mapRestAPIMethod(tt.input); got != tt.want {
				t.Errorf("mapRestAPIMethod(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
