package agentcore

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/AltairaLabs/PromptKit/runtime/deploy"
)

// validDeployConfig is a minimal valid AgentCore deploy config for tests.
const validDeployConfig = `{"region":"us-west-2","runtime_role_arn":"arn:aws:iam::123456789012:role/test","runtime_binary_path":"/usr/local/bin/promptkit-runtime"}`

// validArenaConfigJSON is a minimal valid arena config for tests.
const validArenaConfigJSON = `{"tool_specs":{}}`

// singleAgentPackJSON returns a minimal single-agent pack JSON.
func singleAgentPackJSON() string {
	return `{"id":"mypack","version":"v1.0.0","prompts":{"default":{"id":"default","system_template":"hello"}}}`
}

// multiAgentPackJSON returns a multi-agent pack JSON with two members.
func multiAgentPackJSON() string {
	p := map[string]any{
		"id":      "multi-pack",
		"version": "v1.0.0",
		"prompts": map[string]any{
			"router": map[string]any{
				"id":              "router",
				"name":            "Router",
				"description":     "Routes requests",
				"system_template": "route",
			},
			"worker": map[string]any{
				"id":              "worker",
				"name":            "Worker",
				"description":     "Processes tasks",
				"system_template": "work",
			},
		},
		"agents": map[string]any{
			"entry": "router",
			"members": map[string]any{
				"router": map[string]any{
					"description": "Entry router agent",
				},
				"worker": map[string]any{
					"description": "Worker agent",
				},
			},
		},
	}
	b, _ := json.Marshal(p)
	return string(b)
}

// multiAgentPackWithToolsAndEvalsJSON returns a multi-agent pack with tools and evals.
func multiAgentPackWithToolsAndEvalsJSON() string {
	p := map[string]any{
		"id":      "multi-pack",
		"version": "v1.0.0",
		"prompts": map[string]any{
			"router": map[string]any{
				"id":              "router",
				"system_template": "route",
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
			"search": map[string]any{
				"name":        "search",
				"description": "Web search tool",
			},
		},
		"evals": []map[string]any{
			{
				"id":      "quality",
				"type":    "llm_as_judge",
				"trigger": "every_turn",
				"params":  map[string]any{"instructions": "Evaluate quality"},
			},
		},
	}
	b, _ := json.Marshal(p)
	return string(b)
}

func TestPlan_FirstDeploy_SingleAgent(t *testing.T) {
	provider := newSimulatedProvider()
	resp, err := provider.Plan(context.Background(), &deploy.PlanRequest{
		PackJSON:     singleAgentPackJSON(),
		DeployConfig: validDeployConfig,
		ArenaConfig:  validArenaConfigJSON,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(resp.Changes) != 1 {
		t.Fatalf("expected 1 change, got %d: %+v", len(resp.Changes), resp.Changes)
	}

	c := resp.Changes[0]
	if c.Type != "agent_runtime" {
		t.Errorf("type = %q, want agent_runtime", c.Type)
	}
	if c.Name != "mypack" {
		t.Errorf("name = %q, want mypack", c.Name)
	}
	if c.Action != deploy.ActionCreate {
		t.Errorf("action = %q, want CREATE", c.Action)
	}

	if resp.Summary != "Plan: 1 to create, 0 to update, 0 to delete" {
		t.Errorf("summary = %q", resp.Summary)
	}
}

func TestPlan_FirstDeploy_MultiAgent(t *testing.T) {
	provider := newSimulatedProvider()
	resp, err := provider.Plan(context.Background(), &deploy.PlanRequest{
		PackJSON:     multiAgentPackJSON(),
		DeployConfig: validDeployConfig,
		ArenaConfig:  validArenaConfigJSON,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Multi-agent with 2 members: 2 agent_runtime + 2 a2a_endpoint + 1 gateway = 5.
	if len(resp.Changes) != 5 {
		t.Fatalf("expected 5 changes, got %d: %+v", len(resp.Changes), resp.Changes)
	}

	// All should be CREATE.
	for _, c := range resp.Changes {
		if c.Action != deploy.ActionCreate {
			t.Errorf("expected CREATE for %s/%s, got %s", c.Type, c.Name, c.Action)
		}
	}

	// Check resource types.
	typeCounts := map[string]int{}
	for _, c := range resp.Changes {
		typeCounts[c.Type]++
	}
	if typeCounts["agent_runtime"] != 2 {
		t.Errorf("expected 2 agent_runtime, got %d", typeCounts["agent_runtime"])
	}
	if typeCounts["a2a_endpoint"] != 2 {
		t.Errorf("expected 2 a2a_endpoint, got %d", typeCounts["a2a_endpoint"])
	}
	if typeCounts["gateway"] != 1 {
		t.Errorf("expected 1 gateway, got %d", typeCounts["gateway"])
	}

	if resp.Summary != "Plan: 5 to create, 0 to update, 0 to delete" {
		t.Errorf("summary = %q", resp.Summary)
	}
}

func TestPlan_MultiAgent_WithToolsAndEvals(t *testing.T) {
	provider := newSimulatedProvider()
	resp, err := provider.Plan(context.Background(), &deploy.PlanRequest{
		PackJSON:     multiAgentPackWithToolsAndEvalsJSON(),
		DeployConfig: validDeployConfig,
		ArenaConfig:  validArenaConfigJSON,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// 2 agent_runtime + 2 a2a_endpoint + 1 gateway + 1 tool_gateway (tool) + 1 evaluator + 1 online_eval_config = 8.
	if len(resp.Changes) != 8 {
		t.Fatalf("expected 8 changes, got %d: %+v", len(resp.Changes), resp.Changes)
	}

	typeCounts := map[string]int{}
	for _, c := range resp.Changes {
		typeCounts[c.Type]++
	}
	if typeCounts["tool_gateway"] != 1 {
		t.Errorf("expected 1 tool_gateway, got %d", typeCounts["tool_gateway"])
	}
	if typeCounts["evaluator"] != 1 {
		t.Errorf("expected 1 evaluator, got %d", typeCounts["evaluator"])
	}
	if typeCounts["online_eval_config"] != 1 {
		t.Errorf("expected 1 online_eval_config, got %d", typeCounts["online_eval_config"])
	}
}

func TestPlan_UpdateScenario(t *testing.T) {
	provider := newSimulatedProvider()

	// Prior state has router runtime and an old_worker that no longer exists.
	priorState := AdapterState{
		Resources: []ResourceState{
			{Type: "agent_runtime", Name: "mypack"},
			{Type: "agent_runtime", Name: "old_resource"},
		},
	}
	priorJSON, _ := json.Marshal(priorState)

	resp, err := provider.Plan(context.Background(), &deploy.PlanRequest{
		PackJSON:     singleAgentPackJSON(),
		DeployConfig: validDeployConfig,
		PriorState:   string(priorJSON),
		ArenaConfig:  validArenaConfigJSON,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// mypack should be UPDATE, old_resource should be DELETE.
	if len(resp.Changes) != 2 {
		t.Fatalf("expected 2 changes, got %d: %+v", len(resp.Changes), resp.Changes)
	}

	actionMap := map[string]deploy.Action{}
	for _, c := range resp.Changes {
		actionMap[c.Name] = c.Action
	}

	if actionMap["mypack"] != deploy.ActionUpdate {
		t.Errorf("mypack action = %q, want UPDATE", actionMap["mypack"])
	}
	if actionMap["old_resource"] != deploy.ActionDelete {
		t.Errorf("old_resource action = %q, want DELETE", actionMap["old_resource"])
	}

	if resp.Summary != "Plan: 0 to create, 1 to update, 1 to delete" {
		t.Errorf("summary = %q", resp.Summary)
	}
}

func TestPlan_UpdateMixed(t *testing.T) {
	provider := newSimulatedProvider()

	// Prior state has router runtime. New pack adds worker too (multi-agent).
	priorState := AdapterState{
		Resources: []ResourceState{
			{Type: "agent_runtime", Name: "router"},
			{Type: "a2a_endpoint", Name: "router_endpoint"},
			{Type: "gateway", Name: "router_gateway"},
		},
	}
	priorJSON, _ := json.Marshal(priorState)

	resp, err := provider.Plan(context.Background(), &deploy.PlanRequest{
		PackJSON:     multiAgentPackJSON(),
		DeployConfig: validDeployConfig,
		PriorState:   string(priorJSON),
		ArenaConfig:  validArenaConfigJSON,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// router resources match prior -> UPDATE, worker resources are new -> CREATE.
	actionCounts := map[deploy.Action]int{}
	for _, c := range resp.Changes {
		actionCounts[c.Action]++
	}

	// router agent_runtime, router a2a_endpoint, router_gateway -> 3 UPDATE
	// worker agent_runtime, worker a2a_endpoint -> 2 CREATE
	if actionCounts[deploy.ActionUpdate] != 3 {
		t.Errorf("expected 3 updates, got %d", actionCounts[deploy.ActionUpdate])
	}
	if actionCounts[deploy.ActionCreate] != 2 {
		t.Errorf("expected 2 creates, got %d", actionCounts[deploy.ActionCreate])
	}
}

func TestPlan_Summary(t *testing.T) {
	changes := []deploy.ResourceChange{
		{Action: deploy.ActionCreate},
		{Action: deploy.ActionCreate},
		{Action: deploy.ActionCreate},
		{Action: deploy.ActionUpdate},
		{Action: deploy.ActionDelete},
		{Action: deploy.ActionDelete},
	}

	summary := buildSummary(changes)
	expected := "Plan: 3 to create, 1 to update, 2 to delete"
	if summary != expected {
		t.Errorf("buildSummary = %q, want %q", summary, expected)
	}
}

func TestPlan_InvalidPackJSON(t *testing.T) {
	provider := newSimulatedProvider()
	_, err := provider.Plan(context.Background(), &deploy.PlanRequest{
		PackJSON:     `{bad json}`,
		DeployConfig: validDeployConfig,
		ArenaConfig:  validArenaConfigJSON,
	})
	if err == nil {
		t.Fatal("expected error for invalid pack JSON")
	}
}

func TestPlan_InvalidConfig(t *testing.T) {
	provider := newSimulatedProvider()
	_, err := provider.Plan(context.Background(), &deploy.PlanRequest{
		PackJSON:     singleAgentPackJSON(),
		DeployConfig: `{"region":"","runtime_role_arn":""}`,
		ArenaConfig:  validArenaConfigJSON,
	})
	if err == nil {
		t.Fatal("expected error for invalid config")
	}
}

func TestPlan_InvalidPriorState(t *testing.T) {
	provider := newSimulatedProvider()
	_, err := provider.Plan(context.Background(), &deploy.PlanRequest{
		PackJSON:     singleAgentPackJSON(),
		DeployConfig: validDeployConfig,
		PriorState:   `{broken`,
		ArenaConfig:  validArenaConfigJSON,
	})
	if err == nil {
		t.Fatal("expected error for invalid prior state")
	}
}

func TestPlan_SingleAgent_EmptyPackID(t *testing.T) {
	provider := newSimulatedProvider()
	// Pack with empty ID should default to "default".
	packJSON := `{"id":"","version":"v1.0.0","prompts":{"x":{"id":"x","system_template":"hi"}}}`
	resp, err := provider.Plan(context.Background(), &deploy.PlanRequest{
		PackJSON:     packJSON,
		DeployConfig: validDeployConfig,
		ArenaConfig:  validArenaConfigJSON,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Changes) != 1 {
		t.Fatalf("expected 1 change, got %d", len(resp.Changes))
	}
	if resp.Changes[0].Name != "default" {
		t.Errorf("name = %q, want default", resp.Changes[0].Name)
	}
}

func TestDiffResources_NoPrior(t *testing.T) {
	desired := []deploy.ResourceChange{
		{Type: "agent_runtime", Name: "a", Action: deploy.ActionCreate},
		{Type: "tool_gateway", Name: "b", Action: deploy.ActionCreate},
	}
	result := diffResources(desired, nil)
	if len(result) != 2 {
		t.Fatalf("expected 2 changes, got %d", len(result))
	}
	for _, c := range result {
		if c.Action != deploy.ActionCreate {
			t.Errorf("expected CREATE for %s/%s, got %s", c.Type, c.Name, c.Action)
		}
	}
}

func TestDiffResources_MultipleDeletes_Sorted(t *testing.T) {
	// Prior state has 3 resources, desired has none -> 3 DELETEs sorted by key.
	prior := &AdapterState{
		Resources: []ResourceState{
			{Type: "agent_runtime", Name: "zebra"},
			{Type: "tool_gateway", Name: "alpha"},
			{Type: "a2a_endpoint", Name: "mid"},
		},
	}
	result := diffResources(nil, prior)
	if len(result) != 3 {
		t.Fatalf("expected 3 deletes, got %d", len(result))
	}
	for _, c := range result {
		if c.Action != deploy.ActionDelete {
			t.Errorf("expected DELETE for %s/%s, got %s", c.Type, c.Name, c.Action)
		}
	}
	// Verify sorted by resourceKey (type::name).
	expectedOrder := []string{"a2a_endpoint", "agent_runtime", "tool_gateway"}
	for i, c := range result {
		if c.Type != expectedOrder[i] {
			t.Errorf("delete[%d].Type = %q, want %q", i, c.Type, expectedOrder[i])
		}
	}
}

func TestDiffResources_MixedWithMultipleDeletes(t *testing.T) {
	desired := []deploy.ResourceChange{
		{Type: "agent_runtime", Name: "kept", Action: deploy.ActionCreate},
	}
	prior := &AdapterState{
		Resources: []ResourceState{
			{Type: "agent_runtime", Name: "kept"},
			{Type: "agent_runtime", Name: "old_b"},
			{Type: "agent_runtime", Name: "old_a"},
		},
	}
	result := diffResources(desired, prior)
	if len(result) != 3 {
		t.Fatalf("expected 3 changes (1 update + 2 deletes), got %d", len(result))
	}
	if result[0].Action != deploy.ActionUpdate {
		t.Errorf("first change should be UPDATE, got %s", result[0].Action)
	}
	// Deletes should be sorted.
	if result[1].Name != "old_a" || result[2].Name != "old_b" {
		t.Errorf("deletes not sorted: got %s, %s", result[1].Name, result[2].Name)
	}
}

func TestPlan_WithMemory_IncludesMemoryResource(t *testing.T) {
	provider := newSimulatedProvider()
	memConfig := `{"region":"us-west-2","runtime_role_arn":"arn:aws:iam::123456789012:role/test","runtime_binary_path":"/usr/local/bin/promptkit-runtime","memory_store":"session"}`
	resp, err := provider.Plan(context.Background(), &deploy.PlanRequest{
		PackJSON:     singleAgentPackJSON(),
		DeployConfig: memConfig,
		ArenaConfig:  validArenaConfigJSON,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should have memory + agent_runtime = 2 changes.
	if len(resp.Changes) != 2 {
		t.Fatalf("expected 2 changes, got %d: %+v", len(resp.Changes), resp.Changes)
	}

	// Memory should come first.
	if resp.Changes[0].Type != ResTypeMemory {
		t.Errorf("first change type = %q, want memory", resp.Changes[0].Type)
	}
	if resp.Changes[0].Name != "mypack_memory" {
		t.Errorf("memory name = %q, want mypack_memory", resp.Changes[0].Name)
	}
	if resp.Changes[1].Type != ResTypeAgentRuntime {
		t.Errorf("second change type = %q, want agent_runtime", resp.Changes[1].Type)
	}
}

func TestPlan_WithoutMemory_NoMemoryResource(t *testing.T) {
	provider := newSimulatedProvider()
	resp, err := provider.Plan(context.Background(), &deploy.PlanRequest{
		PackJSON:     singleAgentPackJSON(),
		DeployConfig: validDeployConfig,
		ArenaConfig:  validArenaConfigJSON,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, c := range resp.Changes {
		if c.Type == ResTypeMemory {
			t.Error("should not have memory resource when memory_store is not configured")
		}
	}
}

func TestPlan_SingleAgent_WithTools_IncludesToolGateway(t *testing.T) {
	provider := newSimulatedProvider()
	packJSON := `{
		"id": "toolpack", "version": "v1.0.0",
		"tools": {
			"search": {
				"name": "search",
				"description": "Search the web",
				"parameters": {"type": "object", "properties": {"query": {"type": "string"}}}
			}
		},
		"prompts": {
			"assistant": {
				"id": "assistant", "system_template": "hello",
				"tools": ["search"]
			}
		}
	}`
	resp, err := provider.Plan(context.Background(), &deploy.PlanRequest{
		PackJSON:     packJSON,
		DeployConfig: validDeployConfig,
		ArenaConfig:  validArenaConfigJSON,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should have agent_runtime + tool_gateway = 2 changes.
	if len(resp.Changes) != 2 {
		t.Fatalf("expected 2 changes, got %d: %+v", len(resp.Changes), resp.Changes)
	}

	typeCounts := map[string]int{}
	for _, c := range resp.Changes {
		typeCounts[c.Type]++
	}
	if typeCounts[ResTypeAgentRuntime] != 1 {
		t.Errorf("expected 1 agent_runtime, got %d", typeCounts[ResTypeAgentRuntime])
	}
	if typeCounts[ResTypeToolGateway] != 1 {
		t.Errorf("expected 1 tool_gateway, got %d", typeCounts[ResTypeToolGateway])
	}
}

func TestPlan_WithValidators_NoPolicyResources(t *testing.T) {
	// Validators are runtime-only and should NOT produce Cedar policy resources.
	provider := newSimulatedProvider()
	packJSON := `{
		"id": "valpack", "version": "v1.0.0",
		"prompts": {
			"chat": {
				"id": "chat", "system_template": "hello",
				"validators": [{"type": "banned_words", "params": {"words": ["bad"]}}]
			}
		}
	}`
	resp, err := provider.Plan(context.Background(), &deploy.PlanRequest{
		PackJSON:     packJSON,
		DeployConfig: validDeployConfig,
		ArenaConfig:  validArenaConfigJSON,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should have only agent_runtime = 1 change. No cedar_policy.
	if len(resp.Changes) != 1 {
		t.Fatalf("expected 1 change (agent_runtime only), got %d: %+v", len(resp.Changes), resp.Changes)
	}
	if resp.Changes[0].Type != ResTypeAgentRuntime {
		t.Errorf("expected agent_runtime, got %s", resp.Changes[0].Type)
	}
}

func TestPlan_NoValidators_NoPolicyResources(t *testing.T) {
	provider := newSimulatedProvider()
	resp, err := provider.Plan(context.Background(), &deploy.PlanRequest{
		PackJSON:     singleAgentPackJSON(),
		DeployConfig: validDeployConfig,
		ArenaConfig:  validArenaConfigJSON,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, c := range resp.Changes {
		if c.Type == ResTypeCedarPolicy {
			t.Error("should not have cedar_policy when no validators")
		}
	}
}

func TestBuildSummary_NoChanges(t *testing.T) {
	summary := buildSummary(nil)
	if summary != "Plan: 0 to create, 0 to update, 0 to delete" {
		t.Errorf("summary = %q", summary)
	}
}

func TestPlan_MissingArenaConfig(t *testing.T) {
	provider := newSimulatedProvider()
	_, err := provider.Plan(context.Background(), &deploy.PlanRequest{
		PackJSON:     singleAgentPackJSON(),
		DeployConfig: validDeployConfig,
	})
	if err == nil {
		t.Fatal("expected error for missing arena config")
	}
	if !strings.Contains(err.Error(), "arena_config is required") {
		t.Errorf("error = %q, want 'arena_config is required'", err.Error())
	}
}

func TestPlan_InvalidArenaConfig(t *testing.T) {
	provider := newSimulatedProvider()
	_, err := provider.Plan(context.Background(), &deploy.PlanRequest{
		PackJSON:     singleAgentPackJSON(),
		DeployConfig: validDeployConfig,
		ArenaConfig:  `{bad json`,
	})
	if err == nil {
		t.Fatal("expected error for invalid arena config JSON")
	}
	if !strings.Contains(err.Error(), "invalid arena_config JSON") {
		t.Errorf("error = %q, want 'invalid arena_config JSON'", err.Error())
	}
}
