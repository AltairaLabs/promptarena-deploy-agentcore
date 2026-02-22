package agentcore

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/AltairaLabs/PromptKit/runtime/evals"
	"github.com/AltairaLabs/PromptKit/runtime/prompt"
)

func TestValidateAWSName(t *testing.T) {
	tests := []struct {
		name         string
		resourceName string
		resType      string
		wantErr      bool
	}{
		{"valid simple", "mypack", "agent_runtime", false},
		{"valid with underscore", "my_pack", "agent_runtime", false},
		{"valid with digits", "pack123", "agent_runtime", false},
		{"valid single char", "a", "agent_runtime", false},
		{"valid max length", strings.Repeat("a", 48), "agent_runtime", false},
		{"invalid hyphen", "my-pack", "agent_runtime", true},
		{"invalid starts with digit", "1pack", "agent_runtime", true},
		{"invalid starts with underscore", "_pack", "agent_runtime", true},
		{"invalid too long", strings.Repeat("a", 49), "agent_runtime", true},
		{"invalid empty", "", "agent_runtime", true},
		{"invalid spaces", "my pack", "agent_runtime", true},
		{"invalid dots", "my.pack", "agent_runtime", true},
		{"derived memory name", "mypack_memory", "memory", false},
		{"derived hyphenated memory", "research_team_memory", "memory", false},
		{"invalid hyphenated pack", "research-team_memory", "memory", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateAWSName(tt.resourceName, tt.resType)
			if tt.wantErr && err == nil {
				t.Errorf("expected error for name %q", tt.resourceName)
			}
			if !tt.wantErr && err != nil {
				t.Errorf("unexpected error for name %q: %v", tt.resourceName, err)
			}
			if err != nil {
				if !strings.Contains(err.Error(), tt.resType) {
					t.Errorf("error should mention resource type %q: %v", tt.resType, err)
				}
				if !strings.Contains(err.Error(), tt.resourceName) {
					t.Errorf("error should mention resource name %q: %v", tt.resourceName, err)
				}
			}
		})
	}
}

func TestCollectDerivedNames_SingleAgent(t *testing.T) {
	pack := &prompt.Pack{
		ID: "mypack",
		Prompts: map[string]*prompt.PackPrompt{
			"default": {ID: "default"},
		},
	}
	cfg := &Config{}

	names := collectDerivedNames(pack, cfg)
	if names["mypack"] != ResTypeAgentRuntime {
		t.Errorf("expected mypack -> agent_runtime, got %q", names["mypack"])
	}
	if len(names) != 1 {
		t.Errorf("expected 1 derived name, got %d: %v", len(names), names)
	}
}

func TestCollectDerivedNames_SingleAgentWithMemory(t *testing.T) {
	pack := &prompt.Pack{
		ID: "mypack",
	}
	cfg := &Config{
		Memory: MemoryConfig{Strategies: []string{"semantic"}},
	}

	names := collectDerivedNames(pack, cfg)
	if names["mypack_memory"] != ResTypeMemory {
		t.Errorf("expected mypack_memory -> memory, got %q", names["mypack_memory"])
	}
	if names["mypack"] != ResTypeAgentRuntime {
		t.Errorf("expected mypack -> agent_runtime, got %q", names["mypack"])
	}
}

func TestCollectDerivedNames_EmptyPackID(t *testing.T) {
	pack := &prompt.Pack{ID: ""}
	cfg := &Config{}

	names := collectDerivedNames(pack, cfg)
	if names["default"] != ResTypeAgentRuntime {
		t.Errorf("expected default -> agent_runtime, got %q", names["default"])
	}
}

func TestCollectDerivedNames_WithTools(t *testing.T) {
	pack := &prompt.Pack{
		ID: "toolpack",
		Tools: map[string]*prompt.PackTool{
			"search": {Name: "search"},
			"calc":   {Name: "calc"},
		},
	}
	cfg := &Config{}

	names := collectDerivedNames(pack, cfg)
	if names["search_tool_gw"] != ResTypeToolGateway {
		t.Errorf("expected search_tool_gw -> tool_gateway, got %q", names["search_tool_gw"])
	}
	if names["calc_tool_gw"] != ResTypeToolGateway {
		t.Errorf("expected calc_tool_gw -> tool_gateway, got %q", names["calc_tool_gw"])
	}
}

func TestCollectDerivedNames_MultiAgent(t *testing.T) {
	pack := &prompt.Pack{
		ID: "multi",
		Agents: &prompt.AgentsConfig{
			Entry: "router",
			Members: map[string]*prompt.AgentDef{
				"router": {},
				"worker": {},
			},
		},
	}
	cfg := &Config{}

	names := collectDerivedNames(pack, cfg)

	// Runtime names.
	if names["router"] != ResTypeAgentRuntime {
		t.Errorf("expected router -> agent_runtime")
	}
	if names["worker"] != ResTypeAgentRuntime {
		t.Errorf("expected worker -> agent_runtime")
	}
	// A2A endpoints.
	if names["router_endpoint"] != ResTypeA2AEndpoint {
		t.Errorf("expected router_endpoint -> a2a_endpoint")
	}
	if names["worker_endpoint"] != ResTypeA2AEndpoint {
		t.Errorf("expected worker_endpoint -> a2a_endpoint")
	}
	// Gateway.
	if names["router_gateway"] != "gateway" {
		t.Errorf("expected router_gateway -> gateway")
	}
}

func TestCollectDerivedNames_WithEvals(t *testing.T) {
	pack := &prompt.Pack{
		ID: "evalpack",
		Evals: []evals.EvalDef{
			{ID: "quality", Type: "llm_as_judge"},
			{ID: "safety", Type: "llm_as_judge"},
			{ID: "localonly", Type: "exact_match"},
		},
	}
	cfg := &Config{}

	names := collectDerivedNames(pack, cfg)
	if names["quality_eval"] != ResTypeEvaluator {
		t.Errorf("expected quality_eval -> evaluator")
	}
	if names["safety_eval"] != ResTypeEvaluator {
		t.Errorf("expected safety_eval -> evaluator")
	}
	if names["evalpack_online_eval"] != ResTypeOnlineEvalConfig {
		t.Errorf("expected evalpack_online_eval -> online_eval_config")
	}
	if _, exists := names["localonly_eval"]; exists {
		t.Error("should not have evaluator for exact_match type")
	}
}

func TestValidateResourceNames_ValidPack(t *testing.T) {
	pack := &prompt.Pack{
		ID: "mypack",
		Tools: map[string]*prompt.PackTool{
			"search": {Name: "search"},
		},
	}
	cfg := &Config{
		Memory: MemoryConfig{Strategies: []string{"semantic"}},
	}

	errs := validateResourceNames(pack, cfg)
	if len(errs) != 0 {
		t.Errorf("expected no errors, got %v", errs)
	}
}

func TestValidateResourceNames_HyphenatedPackID(t *testing.T) {
	pack := &prompt.Pack{
		ID: "research-team",
	}
	cfg := &Config{
		Memory: MemoryConfig{Strategies: []string{"semantic"}},
	}

	errs := validateResourceNames(pack, cfg)
	if len(errs) == 0 {
		t.Fatal("expected errors for hyphenated pack ID")
	}

	// Should catch both the runtime name and the memory name.
	foundRuntime := false
	foundMemory := false
	for _, e := range errs {
		if strings.Contains(e, "research-team") && strings.Contains(e, "agent_runtime") {
			foundRuntime = true
		}
		if strings.Contains(e, "research-team_memory") && strings.Contains(e, "memory") {
			foundMemory = true
		}
	}
	if !foundRuntime {
		t.Errorf("expected runtime name error, got %v", errs)
	}
	if !foundMemory {
		t.Errorf("expected memory name error, got %v", errs)
	}
}

func TestValidateResourceNames_HyphenatedToolName(t *testing.T) {
	pack := &prompt.Pack{
		ID: "mypack",
		Tools: map[string]*prompt.PackTool{
			"web-search": {Name: "web-search"},
		},
	}
	cfg := &Config{}

	errs := validateResourceNames(pack, cfg)
	if len(errs) == 0 {
		t.Fatal("expected error for hyphenated tool name")
	}
	if !strings.Contains(errs[0], "web-search_tool_gw") {
		t.Errorf("expected error about web-search_tool_gw, got %v", errs)
	}
}

func TestValidateResourceNames_MultiAgentHyphenated(t *testing.T) {
	pack := &prompt.Pack{
		ID: "multi",
		Agents: &prompt.AgentsConfig{
			Entry: "my-router",
			Members: map[string]*prompt.AgentDef{
				"my-router": {},
				"worker":    {},
			},
		},
	}
	cfg := &Config{}

	errs := validateResourceNames(pack, cfg)
	if len(errs) == 0 {
		t.Fatal("expected errors for hyphenated agent names")
	}

	// Should catch my-router runtime and related derived names.
	found := false
	for _, e := range errs {
		if strings.Contains(e, "my-router") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected error mentioning my-router, got %v", errs)
	}
}

func TestValidateToolTargetNames_Valid(t *testing.T) {
	targets := map[string]*ArenaToolSpec{
		"search": {LambdaARN: "arn:aws:lambda:us-west-2:123456789012:function:search"},
		"calc":   {LambdaARN: "arn:aws:lambda:us-west-2:123456789012:function:calc"},
	}
	errs := validateToolTargetNames(targets)
	if len(errs) != 0 {
		t.Errorf("expected no errors, got %v", errs)
	}
}

func TestValidateToolTargetNames_Invalid(t *testing.T) {
	targets := map[string]*ArenaToolSpec{
		"web-search": {LambdaARN: "arn:aws:lambda:us-west-2:123456789012:function:search"},
	}
	errs := validateToolTargetNames(targets)
	if len(errs) != 1 {
		t.Fatalf("expected 1 error, got %d: %v", len(errs), errs)
	}
	if !strings.Contains(errs[0], "web-search") {
		t.Errorf("error should mention tool name, got %q", errs[0])
	}
}

func TestValidateToolTargetNames_Nil(t *testing.T) {
	errs := validateToolTargetNames(nil)
	if len(errs) != 0 {
		t.Errorf("expected no errors for nil targets, got %v", errs)
	}
}

// TestCollectDerivedNames_FullPack tests name collection with a realistic pack
// containing multiple resource types.
func TestCollectDerivedNames_FullPack(t *testing.T) {
	packData := map[string]any{
		"id":      "fullpack",
		"version": "v1.0.0",
		"prompts": map[string]any{
			"router": map[string]any{
				"id":              "router",
				"system_template": "route",
				"tool_policy":     map[string]any{"blocklist": []string{"banned_tool"}},
			},
			"worker": map[string]any{
				"id":              "worker",
				"system_template": "work",
			},
		},
		"agents": map[string]any{
			"entry": "router",
			"members": map[string]any{
				"router": map[string]any{},
				"worker": map[string]any{},
			},
		},
		"tools": map[string]any{
			"search": map[string]any{"name": "search", "description": "web search"},
		},
		"evals": []map[string]any{
			{"id": "quality", "type": "llm_as_judge", "trigger": "every_turn", "params": map[string]any{"instructions": "eval"}},
		},
	}
	b, _ := json.Marshal(packData)

	var pack prompt.Pack
	if err := json.Unmarshal(b, &pack); err != nil {
		t.Fatalf("failed to parse pack: %v", err)
	}

	cfg := &Config{
		Memory: MemoryConfig{Strategies: []string{"semantic"}},
	}

	names := collectDerivedNames(&pack, cfg)

	expected := map[string]string{
		"fullpack_memory":      ResTypeMemory,
		"fullpack_online_eval": ResTypeOnlineEvalConfig,
		"quality_eval":         ResTypeEvaluator,
		"router":               ResTypeAgentRuntime,
		"worker":               ResTypeAgentRuntime,
		"router_endpoint":      ResTypeA2AEndpoint,
		"worker_endpoint":      ResTypeA2AEndpoint,
		"router_gateway":       "gateway",
		"search_tool_gw":       ResTypeToolGateway,
		"router_policy_engine": ResTypeCedarPolicy,
	}

	for name, typ := range expected {
		if got, ok := names[name]; !ok {
			t.Errorf("missing expected name %q (%s)", name, typ)
		} else if got != typ {
			t.Errorf("name %q: got type %q, want %q", name, got, typ)
		}
	}

	if len(names) != len(expected) {
		t.Errorf("expected %d names, got %d: %v", len(expected), len(names), names)
	}
}

func TestFormatNameErrors(t *testing.T) {
	errs := []string{"error one", "error two"}
	result := formatNameErrors(errs)
	if result != "error one; error two" {
		t.Errorf("formatNameErrors = %q, want %q", result, "error one; error two")
	}
}
