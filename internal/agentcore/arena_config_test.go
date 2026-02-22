package agentcore

import (
	"testing"
)

func TestParseArenaConfig_Empty(t *testing.T) {
	_, err := parseArenaConfig("")
	if err == nil {
		t.Fatal("expected error for empty string")
	}
	if got := err.Error(); got != "arena_config is required" {
		t.Fatalf("unexpected error: %s", got)
	}
}

func TestParseArenaConfig_InvalidJSON(t *testing.T) {
	_, err := parseArenaConfig("{not-json")
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
	if got := err.Error(); got == "arena_config is required" {
		t.Fatal("got wrong error — should be a JSON parse error")
	}
}

func TestParseArenaConfig_MinimalValid(t *testing.T) {
	cfg, err := parseArenaConfig("{}")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}
}

func TestParseArenaConfig_WithToolSpecs(t *testing.T) {
	raw := `{
		"tool_specs": {
			"search": {
				"name": "search",
				"description": "Web search",
				"mode": "live",
				"http": {
					"url": "https://api.example.com/search",
					"method": "POST"
				}
			},
			"calculator": {
				"name": "calculator",
				"mode": "mock"
			}
		}
	}`
	cfg, err := parseArenaConfig(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.ToolSpecs) != 2 {
		t.Fatalf("expected 2 tool specs, got %d", len(cfg.ToolSpecs))
	}

	search := cfg.ToolSpecs["search"]
	if search.Name != "search" {
		t.Errorf("expected name 'search', got %q", search.Name)
	}
	if search.Mode != "live" {
		t.Errorf("expected mode 'live', got %q", search.Mode)
	}
	if search.HTTPConfig == nil {
		t.Fatal("expected non-nil HTTPConfig")
	}
	if search.HTTPConfig.URL != "https://api.example.com/search" {
		t.Errorf("unexpected URL: %s", search.HTTPConfig.URL)
	}

	calc := cfg.ToolSpecs["calculator"]
	if calc.Mode != "mock" {
		t.Errorf("expected mode 'mock', got %q", calc.Mode)
	}
	if calc.HTTPConfig != nil {
		t.Error("expected nil HTTPConfig for calculator")
	}
}

func TestParseArenaConfig_WithMCPServers(t *testing.T) {
	raw := `{
		"mcp_servers": [
			{
				"name": "my-server",
				"command": "npx",
				"args": ["-y", "@example/mcp-server"]
			}
		]
	}`
	cfg, err := parseArenaConfig(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.MCPServers) != 1 {
		t.Fatalf("expected 1 MCP server, got %d", len(cfg.MCPServers))
	}
	srv := cfg.MCPServers[0]
	if srv.Name != "my-server" {
		t.Errorf("expected name 'my-server', got %q", srv.Name)
	}
	if srv.Command != "npx" {
		t.Errorf("expected command 'npx', got %q", srv.Command)
	}
	if len(srv.Args) != 2 {
		t.Fatalf("expected 2 args, got %d", len(srv.Args))
	}
}

func TestArenaConfig_ToolSpecForName(t *testing.T) {
	cfg := &ArenaConfig{
		ToolSpecs: map[string]*ArenaToolSpec{
			"search": {Name: "search", Mode: "live"},
		},
	}

	got := cfg.toolSpecForName("search")
	if got == nil {
		t.Fatal("expected non-nil tool spec")
	}
	if got.Mode != "live" {
		t.Errorf("expected mode 'live', got %q", got.Mode)
	}

	if cfg.toolSpecForName("missing") != nil {
		t.Error("expected nil for missing tool")
	}
}

func TestArenaConfig_ToolSpecForName_NilReceiver(t *testing.T) {
	var cfg *ArenaConfig
	if cfg.toolSpecForName("anything") != nil {
		t.Error("expected nil from nil receiver")
	}
}

func TestArenaConfig_ToolSpecForName_NilToolSpecs(t *testing.T) {
	cfg := &ArenaConfig{}
	if cfg.toolSpecForName("anything") != nil {
		t.Error("expected nil when ToolSpecs is nil")
	}
}

func TestMergeToolTargets_NilArena(t *testing.T) {
	// Should not panic.
	mergeToolTargets(nil, map[string]*ArenaToolSpec{"x": {LambdaARN: "arn"}})
}

func TestMergeToolTargets_EmptyTargets(t *testing.T) {
	arena := &ArenaConfig{}
	mergeToolTargets(arena, nil)
	if arena.ToolSpecs != nil {
		t.Error("expected nil ToolSpecs after merge with nil targets")
	}
}

func TestMergeToolTargets_NewTool(t *testing.T) {
	arena := &ArenaConfig{}
	mergeToolTargets(arena, map[string]*ArenaToolSpec{
		"search": {LambdaARN: "arn:aws:lambda:us-west-2:123:function:search"},
	})
	if arena.ToolSpecs == nil {
		t.Fatal("expected ToolSpecs to be initialized")
	}
	spec := arena.ToolSpecs["search"]
	if spec == nil {
		t.Fatal("expected search spec")
	}
	if spec.LambdaARN != "arn:aws:lambda:us-west-2:123:function:search" {
		t.Errorf("unexpected lambda_arn: %s", spec.LambdaARN)
	}
}

func TestMergeToolTargets_MergeIntoExisting(t *testing.T) {
	arena := &ArenaConfig{
		ToolSpecs: map[string]*ArenaToolSpec{
			"search": {Name: "search", Description: "Web search", Mode: "live"},
		},
	}
	mergeToolTargets(arena, map[string]*ArenaToolSpec{
		"search": {LambdaARN: "arn:aws:lambda:us-west-2:123:function:search"},
	})
	spec := arena.ToolSpecs["search"]
	if spec.Name != "search" {
		t.Errorf("expected name preserved, got %q", spec.Name)
	}
	if spec.Description != "Web search" {
		t.Errorf("expected description preserved, got %q", spec.Description)
	}
	if spec.LambdaARN != "arn:aws:lambda:us-west-2:123:function:search" {
		t.Errorf("expected lambda_arn merged, got %q", spec.LambdaARN)
	}
}

func TestParseArenaConfig_UnknownFieldsIgnored(t *testing.T) {
	raw := `{"unknown_field": "value", "tool_specs": {}}`
	cfg, err := parseArenaConfig(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}
}
