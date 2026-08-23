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

	// The tool's own name, because that is what apply records in state. A
	// derived name here meant plan and state never matched, so every re-plan
	// after a deploy reported delete plus create for every tool.
	names := collectDerivedNames(pack, cfg)
	if names["search"] != ResTypeToolGateway {
		t.Errorf("expected search -> tool_gateway, got %q", names["search"])
	}
	if names["calc"] != ResTypeToolGateway {
		t.Errorf("expected calc -> tool_gateway, got %q", names["calc"])
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

// A hyphenated tool name is fine for a gateway: hyphens are the one separator
// the Gateway pattern allows. This test used to assert the opposite, because
// gateway names were validated against the underscore-permitting pattern every
// other AgentCore resource uses — which rejected the one style AWS accepts and
// waved through the one it does not.
func TestValidateResourceNames_HyphenatedToolName(t *testing.T) {
	pack := &prompt.Pack{
		ID: "mypack",
		Tools: map[string]*prompt.PackTool{
			"web-search": {Name: "web-search"},
		},
	}
	cfg := &Config{}

	if errs := validateResourceNames(pack, cfg); len(errs) != 0 {
		t.Errorf("validateResourceNames = %v, want none for a hyphenated tool name", errs)
	}
}

// An underscored tool name is the case that actually failed at the API, and it
// now passes because the gateway name is sanitized before it is sent.
func TestValidateResourceNames_UnderscoredToolName(t *testing.T) {
	pack := &prompt.Pack{
		ID: "mypack",
		Tools: map[string]*prompt.PackTool{
			"lookup_order": {Name: "lookup_order"},
		},
	}
	cfg := &Config{}

	if errs := validateResourceNames(pack, cfg); len(errs) != 0 {
		t.Errorf("validateResourceNames = %v, want none for an underscored tool name", errs)
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

// Hyphens and underscores both survive now: a hyphen is legal in a Gateway
// name, and an underscore is sanitized to one before the call. What cannot be
// salvaged is a name with no alphanumerics at all, which sanitises to nothing.
func TestValidateToolTargetNames_HyphensAndUnderscores(t *testing.T) {
	targets := map[string]*ArenaToolSpec{
		"web-search":   {LambdaARN: "arn:aws:lambda:us-west-2:123456789012:function:a"},
		"lookup_order": {LambdaARN: "arn:aws:lambda:us-west-2:123456789012:function:b"},
	}
	if errs := validateToolTargetNames(targets); len(errs) != 0 {
		t.Errorf("validateToolTargetNames = %v, want none", errs)
	}
}

func TestValidateToolTargetNames_Invalid(t *testing.T) {
	targets := map[string]*ArenaToolSpec{
		"___": {LambdaARN: "arn:aws:lambda:us-west-2:123456789012:function:search"},
	}
	errs := validateToolTargetNames(targets)
	if len(errs) != 1 {
		t.Fatalf("expected 1 error, got %d: %v", len(errs), errs)
	}
	if !strings.Contains(errs[0], "___") {
		t.Errorf("error should mention the tool name, got %q", errs[0])
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
		"search":               ResTypeToolGateway,
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

func TestSanitizeGatewayName(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		// The case that failed at the API: underscores are legal everywhere
		// else in AgentCore and illegal in a Gateway name.
		{"underscores become hyphens", "lookup_order", "lookup-order"},
		// Already valid, so unchanged. This is the property that makes the
		// change safe: no gateway that deploys today gets a new name.
		{"a valid name is untouched", "websearch", "websearch"},
		{"hyphens are already legal", "web-search", "web-search"},
		{"digits are fine", "tool2", "tool2"},
		// The pattern allows a hyphen only after an alphanumeric, so runs and
		// edges have to go.
		{"runs collapse", "a__b", "a-b"},
		{"leading separators are dropped", "_tool", "tool"},
		{"trailing separators are dropped", "tool_", "tool"},
		{"nothing salvageable yields nothing", "___", ""},
		{"empty stays empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sanitizeGatewayName(tt.in); got != tt.want {
				t.Errorf("sanitizeGatewayName(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// Whatever sanitizeGatewayName produces, with the suffix, has to satisfy the
// pattern AWS enforces — otherwise the sanitising is decorative.
func TestSanitizedGatewayNamesAreAccepted(t *testing.T) {
	for _, in := range []string{
		"lookup_order", "web-search", "tool2", "a__b", "_tool", "tool_",
		"averylongtoolnamethatgoesonandonandonandonandonandonandon",
	} {
		t.Run(in, func(t *testing.T) {
			got := sanitizeGatewayName(in) + gatewaySuffix
			if !gatewayNameRe.MatchString(got) {
				t.Errorf("sanitizeGatewayName(%q)+suffix = %q, which AWS would reject", in, got)
			}
		})
	}
}

// A gateway target takes the tool name directly and enforces the same
// character rule as the gateway, so an underscored tool was refused there too —
// one call after the parent gateway refused it. The limit differs: 100 rather
// than 48.
func TestSanitizeGatewayTargetName(t *testing.T) {
	if got := sanitizeGatewayTargetName("lookup_order"); got != "lookup-order" {
		t.Errorf("sanitizeGatewayTargetName = %q, want lookup-order", got)
	}
	if got := sanitizeGatewayTargetName("websearch"); got != "websearch" {
		t.Errorf("an already-valid name must be untouched, got %q", got)
	}
}

// Whatever either sanitizer produces must satisfy the pattern AWS enforces,
// at the limit that applies to it. The gateway case caught a real bug: a stem
// truncated to the full 48 plus "-gw" is 51 characters.
func TestSanitizedNamesFitTheirLimits(t *testing.T) {
	long := strings.Repeat("tool_name_", 30)

	t.Run("gateway", func(t *testing.T) {
		got := sanitizeGatewayName(long) + gatewaySuffix
		if len(got) > maxGatewayNameLen {
			t.Errorf("gateway name is %d characters, over the %d limit: %q",
				len(got), maxGatewayNameLen, got)
		}
		if !gatewayNameRe.MatchString(got) {
			t.Errorf("AWS would reject %q", got)
		}
	})

	t.Run("target", func(t *testing.T) {
		got := sanitizeGatewayTargetName(long)
		if len(got) > maxGatewayTargetNameLen {
			t.Errorf("target name is %d characters, over the %d limit: %q",
				len(got), maxGatewayTargetNameLen, got)
		}
	})
}
