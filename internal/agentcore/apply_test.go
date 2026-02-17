package agentcore

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/AltairaLabs/PromptKit/runtime/deploy"
)

// --- test helpers ---

// collectEvents runs Apply and collects all emitted events.
func collectEvents(t *testing.T, provider *Provider, req *deploy.PlanRequest) ([]deploy.ApplyEvent, string, error) {
	t.Helper()
	var events []deploy.ApplyEvent
	callback := func(ev *deploy.ApplyEvent) error {
		events = append(events, *ev)
		return nil
	}
	state, err := provider.Apply(context.Background(), req, callback)
	return events, state, err
}

// validConfig returns a valid deploy config JSON string.
func validConfig() string {
	return `{"region":"us-west-2","runtime_role_arn":"arn:aws:iam::123456789012:role/test"}`
}

// singleAgentPack returns a minimal single-agent pack JSON.
func singleAgentPack() string {
	p := map[string]any{
		"id":      "mypack",
		"version": "v1.0.0",
		"name":    "My Pack",
		"prompts": map[string]any{
			"chat": map[string]any{
				"id":              "chat",
				"name":            "Chat",
				"system_template": "You are a helpful assistant.",
				"version":         "v1.0.0",
			},
		},
		"template_engine": map[string]any{
			"version": "1.0",
			"syntax":  "handlebars",
		},
	}
	b, _ := json.Marshal(p)
	return string(b)
}

// singleAgentPackWithTools returns a pack JSON with two tools.
func singleAgentPackWithTools() string {
	p := map[string]any{
		"id":      "toolpack",
		"version": "v1.0.0",
		"name":    "Tool Pack",
		"prompts": map[string]any{
			"chat": map[string]any{
				"id":              "chat",
				"name":            "Chat",
				"system_template": "You help.",
				"version":         "v1.0.0",
				"tools":           []string{"search", "calc"},
			},
		},
		"tools": map[string]any{
			"search": map[string]any{"name": "search", "description": "search the web"},
			"calc":   map[string]any{"name": "calc", "description": "calculator"},
		},
		"template_engine": map[string]any{
			"version": "1.0",
			"syntax":  "handlebars",
		},
	}
	b, _ := json.Marshal(p)
	return string(b)
}

// multiAgentPack returns a pack JSON with two agent members.
func multiAgentPack() string {
	p := map[string]any{
		"id":      "multipack",
		"version": "v1.0.0",
		"name":    "Multi Pack",
		"prompts": map[string]any{
			"coordinator": map[string]any{
				"id":              "coordinator",
				"name":            "Coordinator",
				"system_template": "You coordinate.",
				"version":         "v1.0.0",
			},
			"worker": map[string]any{
				"id":              "worker",
				"name":            "Worker",
				"system_template": "You work.",
				"version":         "v1.0.0",
			},
		},
		"agents": map[string]any{
			"entry": "coordinator",
			"members": map[string]any{
				"coordinator": map[string]any{"description": "coordinates tasks"},
				"worker":      map[string]any{"description": "performs tasks"},
			},
		},
		"tools": map[string]any{
			"lookup": map[string]any{"name": "lookup", "description": "look things up"},
		},
		"template_engine": map[string]any{
			"version": "1.0",
			"syntax":  "handlebars",
		},
	}
	b, _ := json.Marshal(p)
	return string(b)
}

// multiAgentPackWithEvals returns a multi-agent pack with eval definitions.
func multiAgentPackWithEvals() string {
	p := map[string]any{
		"id":      "evalpack",
		"version": "v1.0.0",
		"name":    "Eval Pack",
		"prompts": map[string]any{
			"coordinator": map[string]any{
				"id": "coordinator", "name": "Coord",
				"system_template": "You coordinate.", "version": "v1.0.0",
			},
			"worker": map[string]any{
				"id": "worker", "name": "Worker",
				"system_template": "You work.", "version": "v1.0.0",
			},
		},
		"agents": map[string]any{
			"entry": "coordinator",
			"members": map[string]any{
				"coordinator": map[string]any{"description": "coord"},
				"worker":      map[string]any{"description": "work"},
			},
		},
		"evals": []map[string]any{
			{"id": "latency_check", "type": "latency", "trigger": "post_response", "params": map[string]any{}},
			{"id": "quality_check", "type": "quality", "trigger": "post_response", "params": map[string]any{}},
		},
		"template_engine": map[string]any{
			"version": "1.0",
			"syntax":  "handlebars",
		},
	}
	b, _ := json.Marshal(p)
	return string(b)
}

// failingAWSClient lets tests inject failures for specific resource types.
type failingAWSClient struct {
	simulatedAWSClient
	failOn map[string]bool // resource type -> should fail
}

func (c *failingAWSClient) CreateRuntime(ctx context.Context, name string, cfg *Config) (string, error) {
	if c.failOn["agent_runtime"] {
		return "", fmt.Errorf("simulated runtime creation failure for %s", name)
	}
	return c.simulatedAWSClient.CreateRuntime(ctx, name, cfg)
}

func (c *failingAWSClient) UpdateRuntime(ctx context.Context, arn string, name string, cfg *Config) (string, error) {
	if c.failOn["agent_runtime_update"] {
		return "", fmt.Errorf("simulated runtime update failure for %s", name)
	}
	return c.simulatedAWSClient.UpdateRuntime(ctx, arn, name, cfg)
}

func (c *failingAWSClient) CreateGatewayTool(ctx context.Context, name string, cfg *Config) (string, error) {
	if c.failOn["tool_gateway"] {
		return "", fmt.Errorf("simulated gateway tool failure for %s", name)
	}
	return c.simulatedAWSClient.CreateGatewayTool(ctx, name, cfg)
}

func (c *failingAWSClient) CreateA2AWiring(ctx context.Context, name string, cfg *Config) (string, error) {
	if c.failOn["a2a_endpoint"] {
		return "", fmt.Errorf("simulated a2a wiring failure for %s", name)
	}
	return c.simulatedAWSClient.CreateA2AWiring(ctx, name, cfg)
}

func (c *failingAWSClient) CreateEvaluator(ctx context.Context, name string, cfg *Config) (string, error) {
	if c.failOn["evaluator"] {
		return "", fmt.Errorf("simulated evaluator failure for %s", name)
	}
	return c.simulatedAWSClient.CreateEvaluator(ctx, name, cfg)
}

func (c *failingAWSClient) CreateMemory(ctx context.Context, name string, cfg *Config) (string, error) {
	if c.failOn["memory"] {
		return "", fmt.Errorf("simulated memory creation failure for %s", name)
	}
	return c.simulatedAWSClient.CreateMemory(ctx, name, cfg)
}

// --- tests ---

func TestApply_SingleAgent_StreamsCorrectEvents(t *testing.T) {
	provider := newSimulatedProvider()
	req := &deploy.PlanRequest{
		PackJSON:     singleAgentPack(),
		DeployConfig: validConfig(),
	}

	events, stateStr, err := collectEvents(t, provider, req)
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if stateStr == "" {
		t.Fatal("Apply returned empty state")
	}

	// Should have at least: 1 progress + 1 resource for the single runtime.
	var progressCount, resourceCount int
	for _, ev := range events {
		switch ev.Type {
		case "progress":
			progressCount++
		case "resource":
			resourceCount++
		}
	}
	if progressCount < 1 {
		t.Errorf("expected at least 1 progress event, got %d", progressCount)
	}
	if resourceCount < 1 {
		t.Errorf("expected at least 1 resource event, got %d", resourceCount)
	}

	// Verify the runtime resource event exists.
	found := false
	for _, ev := range events {
		if ev.Type == "resource" && ev.Resource != nil &&
			ev.Resource.Type == "agent_runtime" && ev.Resource.Status == "created" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected a 'resource' event for agent_runtime with status=created")
	}

	// Verify state.
	var state AdapterState
	if err := json.Unmarshal([]byte(stateStr), &state); err != nil {
		t.Fatalf("failed to unmarshal state: %v", err)
	}
	if len(state.Resources) != 1 {
		t.Errorf("expected 1 resource in state, got %d", len(state.Resources))
	}
	if state.PackID != "mypack" {
		t.Errorf("state.PackID = %q, want mypack", state.PackID)
	}
}

func TestApply_SingleAgentWithTools(t *testing.T) {
	provider := newSimulatedProvider()
	req := &deploy.PlanRequest{
		PackJSON:     singleAgentPackWithTools(),
		DeployConfig: validConfig(),
	}

	events, stateStr, err := collectEvents(t, provider, req)
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}

	// Should have tool_gateway events before agent_runtime events.
	var types []string
	for _, ev := range events {
		if ev.Type == "resource" && ev.Resource != nil {
			types = append(types, ev.Resource.Type)
		}
	}

	// Expect: calc, search (tool_gateway sorted), then toolpack (runtime).
	if len(types) != 3 {
		t.Fatalf("expected 3 resource events, got %d: %v", len(types), types)
	}
	if types[0] != "tool_gateway" || types[1] != "tool_gateway" {
		t.Errorf("first two resources should be tool_gateway, got %v", types[:2])
	}
	if types[2] != "agent_runtime" {
		t.Errorf("third resource should be agent_runtime, got %s", types[2])
	}

	// Check state has all 3 resources.
	var state AdapterState
	if err := json.Unmarshal([]byte(stateStr), &state); err != nil {
		t.Fatalf("failed to unmarshal state: %v", err)
	}
	if len(state.Resources) != 3 {
		t.Errorf("expected 3 resources in state, got %d", len(state.Resources))
	}
}

func TestApply_MultiAgent_DependencyOrder(t *testing.T) {
	provider := newSimulatedProvider()
	req := &deploy.PlanRequest{
		PackJSON:     multiAgentPack(),
		DeployConfig: validConfig(),
	}

	events, stateStr, err := collectEvents(t, provider, req)
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}

	// Collect resource event types in order.
	var resourceTypes []string
	for _, ev := range events {
		if ev.Type == "resource" && ev.Resource != nil {
			resourceTypes = append(resourceTypes, ev.Resource.Type)
		}
	}

	// Expected order: tool_gateway(s) -> agent_runtime(s) -> a2a_endpoint(s).
	// Multi-agent pack has 1 tool, 2 runtimes, 2 a2a endpoints = 5 total.
	if len(resourceTypes) != 5 {
		t.Fatalf("expected 5 resource events, got %d: %v", len(resourceTypes), resourceTypes)
	}

	// Verify ordering: gateway before runtime before a2a.
	lastGateway := -1
	firstRuntime := len(resourceTypes)
	lastRuntime := -1
	firstA2A := len(resourceTypes)

	for i, rt := range resourceTypes {
		switch rt {
		case "tool_gateway":
			if i > lastGateway {
				lastGateway = i
			}
		case "agent_runtime":
			if i < firstRuntime {
				firstRuntime = i
			}
			if i > lastRuntime {
				lastRuntime = i
			}
		case "a2a_endpoint":
			if i < firstA2A {
				firstA2A = i
			}
		}
	}

	if lastGateway >= firstRuntime {
		t.Errorf("tool_gateway resources should come before agent_runtime: %v", resourceTypes)
	}
	if lastRuntime >= firstA2A {
		t.Errorf("agent_runtime resources should come before a2a_endpoint: %v", resourceTypes)
	}

	// Verify state blob.
	var state AdapterState
	if err := json.Unmarshal([]byte(stateStr), &state); err != nil {
		t.Fatalf("failed to unmarshal state: %v", err)
	}
	if len(state.Resources) != 5 {
		t.Errorf("expected 5 resources in state, got %d", len(state.Resources))
	}
	for _, r := range state.Resources {
		if r.ARN == "" {
			t.Errorf("resource %s/%s has empty ARN", r.Type, r.Name)
		}
		if r.Status != "created" {
			t.Errorf("resource %s/%s status = %q, want created", r.Type, r.Name, r.Status)
		}
	}
}

func TestApply_MultiAgentWithEvals(t *testing.T) {
	provider := newSimulatedProvider()
	req := &deploy.PlanRequest{
		PackJSON:     multiAgentPackWithEvals(),
		DeployConfig: validConfig(),
	}

	events, stateStr, err := collectEvents(t, provider, req)
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}

	// Collect resource types.
	var resourceTypes []string
	for _, ev := range events {
		if ev.Type == "resource" && ev.Resource != nil {
			resourceTypes = append(resourceTypes, ev.Resource.Type)
		}
	}

	// 2 runtimes + 2 a2a + 2 evaluators = 6 (no tools in this pack).
	if len(resourceTypes) != 6 {
		t.Fatalf("expected 6 resource events, got %d: %v", len(resourceTypes), resourceTypes)
	}

	// Evaluators should come last.
	lastA2A := -1
	firstEval := len(resourceTypes)
	for i, rt := range resourceTypes {
		if rt == "a2a_endpoint" && i > lastA2A {
			lastA2A = i
		}
		if rt == "evaluator" && i < firstEval {
			firstEval = i
		}
	}
	if lastA2A >= firstEval {
		t.Errorf("a2a_endpoint should come before evaluator: %v", resourceTypes)
	}

	// State should have all 6.
	var state AdapterState
	if err := json.Unmarshal([]byte(stateStr), &state); err != nil {
		t.Fatalf("failed to unmarshal state: %v", err)
	}
	if len(state.Resources) != 6 {
		t.Errorf("expected 6 resources in state, got %d", len(state.Resources))
	}
}

func TestApply_StateContainsAllResourceInfo(t *testing.T) {
	provider := newSimulatedProvider()
	req := &deploy.PlanRequest{
		PackJSON:     multiAgentPack(),
		DeployConfig: validConfig(),
	}

	_, stateStr, err := collectEvents(t, provider, req)
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}

	var state AdapterState
	if err := json.Unmarshal([]byte(stateStr), &state); err != nil {
		t.Fatalf("failed to unmarshal state: %v", err)
	}

	if state.PackID != "multipack" {
		t.Errorf("state.PackID = %q, want multipack", state.PackID)
	}
	if state.Version != "v1.0.0" {
		t.Errorf("state.Version = %q, want v1.0.0", state.Version)
	}

	// Check each resource has type, name, ARN, and status.
	for _, r := range state.Resources {
		if r.Type == "" {
			t.Error("resource has empty Type")
		}
		if r.Name == "" {
			t.Error("resource has empty Name")
		}
		if r.ARN == "" {
			t.Errorf("resource %s/%s has empty ARN", r.Type, r.Name)
		}
		if r.Status != "created" {
			t.Errorf("resource %s/%s status = %q, want created", r.Type, r.Name, r.Status)
		}
	}

	// Verify ARN format includes region from config.
	for _, r := range state.Resources {
		if !strings.Contains(r.ARN, "us-west-2") {
			t.Errorf("resource %s/%s ARN %q should contain region us-west-2", r.Type, r.Name, r.ARN)
		}
	}
}

func TestApply_PartialFailure_ReturnsStateForSuccessfulResources(t *testing.T) {
	sim := newSimulatedProvider()
	provider := &Provider{
		awsClientFunc: func(_ context.Context, cfg *Config) (awsClient, error) {
			return &failingAWSClient{
				simulatedAWSClient: *newSimulatedAWSClient(cfg.Region),
				failOn:             map[string]bool{"a2a_endpoint": true},
			}, nil
		},
		destroyerFunc: sim.destroyerFunc,
		checkerFunc:   sim.checkerFunc,
	}

	req := &deploy.PlanRequest{
		PackJSON:     multiAgentPack(),
		DeployConfig: validConfig(),
	}

	events, stateStr, err := collectEvents(t, provider, req)
	if err == nil {
		t.Fatal("expected error for partial failure, got nil")
	}

	// Should have error events for a2a_endpoint failures.
	var errorCount int
	for _, ev := range events {
		if ev.Type == "error" {
			errorCount++
		}
	}
	if errorCount == 0 {
		t.Error("expected at least one error event")
	}

	// State should still contain successful resources.
	var state AdapterState
	if err := json.Unmarshal([]byte(stateStr), &state); err != nil {
		t.Fatalf("failed to unmarshal state: %v", err)
	}

	var createdCount, failedCount int
	for _, r := range state.Resources {
		switch r.Status {
		case "created":
			createdCount++
		case "failed":
			failedCount++
		}
	}
	if createdCount == 0 {
		t.Error("expected at least some created resources despite partial failure")
	}
	if failedCount == 0 {
		t.Error("expected at least some failed resources")
	}

	// The tool_gateway and agent_runtime should have succeeded.
	for _, r := range state.Resources {
		if r.Type == "tool_gateway" && r.Status != "created" {
			t.Errorf("tool_gateway %q should have status=created, got %s", r.Name, r.Status)
		}
		if r.Type == "agent_runtime" && r.Status != "created" {
			t.Errorf("agent_runtime %q should have status=created, got %s", r.Name, r.Status)
		}
		if r.Type == "a2a_endpoint" && r.Status != "failed" {
			t.Errorf("a2a_endpoint %q should have status=failed, got %s", r.Name, r.Status)
		}
	}
}

func TestApply_ProgressMessages(t *testing.T) {
	provider := newSimulatedProvider()
	req := &deploy.PlanRequest{
		PackJSON:     multiAgentPack(),
		DeployConfig: validConfig(),
	}

	events, _, err := collectEvents(t, provider, req)
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}

	var progressMessages []string
	for _, ev := range events {
		if ev.Type == "progress" {
			progressMessages = append(progressMessages, ev.Message)
		}
	}

	// Should have progress messages for each resource step.
	if len(progressMessages) < 5 {
		t.Errorf("expected at least 5 progress messages (1 tool + 2 runtimes + 2 a2a), got %d: %v",
			len(progressMessages), progressMessages)
	}

	// Verify progress messages contain the expected resource type prefixes.
	expectedPrefixes := []string{
		"Creating tool_gateway:",
		"Creating agent_runtime:",
		"Creating a2a_endpoint:",
	}
	for _, prefix := range expectedPrefixes {
		found := false
		for _, msg := range progressMessages {
			if strings.Contains(msg, prefix) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected a progress message containing %q", prefix)
		}
	}

	// Verify progress messages include percentage.
	for _, msg := range progressMessages {
		if !strings.Contains(msg, "%") {
			t.Errorf("progress message %q should contain a percentage", msg)
		}
	}
}

func TestApply_BadPackJSON(t *testing.T) {
	provider := newSimulatedProvider()
	req := &deploy.PlanRequest{
		PackJSON:     `{not valid}`,
		DeployConfig: validConfig(),
	}

	_, _, err := collectEvents(t, provider, req)
	if err == nil {
		t.Fatal("expected error for bad pack JSON")
	}
	if !strings.Contains(err.Error(), "failed to parse pack") {
		t.Errorf("error = %q, want 'failed to parse pack'", err.Error())
	}
}

func TestApply_BadDeployConfig(t *testing.T) {
	provider := newSimulatedProvider()
	req := &deploy.PlanRequest{
		PackJSON:     singleAgentPack(),
		DeployConfig: `{not valid}`,
	}

	_, _, err := collectEvents(t, provider, req)
	if err == nil {
		t.Fatal("expected error for bad deploy config")
	}
	if !strings.Contains(err.Error(), "failed to parse deploy config") {
		t.Errorf("error = %q, want 'failed to parse deploy config'", err.Error())
	}
}

func TestApply_EmptyPack_CreatesRuntimeOnly(t *testing.T) {
	provider := newSimulatedProvider()
	// Minimal pack with just an ID and no tools/agents/evals.
	pack := map[string]any{
		"id":      "empty",
		"version": "v1.0.0",
		"name":    "Empty Pack",
		"prompts": map[string]any{},
		"template_engine": map[string]any{
			"version": "1.0",
			"syntax":  "handlebars",
		},
	}
	packJSON, _ := json.Marshal(pack)

	req := &deploy.PlanRequest{
		PackJSON:     string(packJSON),
		DeployConfig: validConfig(),
	}

	events, stateStr, err := collectEvents(t, provider, req)
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}

	// Should only have 1 runtime resource.
	var resourceCount int
	for _, ev := range events {
		if ev.Type == "resource" {
			resourceCount++
		}
	}
	if resourceCount != 1 {
		t.Errorf("expected 1 resource event (runtime only), got %d", resourceCount)
	}

	var state AdapterState
	if err := json.Unmarshal([]byte(stateStr), &state); err != nil {
		t.Fatalf("failed to unmarshal state: %v", err)
	}
	if len(state.Resources) != 1 {
		t.Errorf("expected 1 resource in state, got %d", len(state.Resources))
	}
	if state.Resources[0].Type != "agent_runtime" {
		t.Errorf("expected agent_runtime, got %s", state.Resources[0].Type)
	}
}

func TestApply_AWSClientFactoryError(t *testing.T) {
	provider := &Provider{
		awsClientFunc: func(_ context.Context, _ *Config) (awsClient, error) {
			return nil, fmt.Errorf("simulated client factory failure")
		},
	}

	req := &deploy.PlanRequest{
		PackJSON:     singleAgentPack(),
		DeployConfig: validConfig(),
	}

	_, _, err := collectEvents(t, provider, req)
	if err == nil {
		t.Fatal("expected error for client factory failure")
	}
	if !strings.Contains(err.Error(), "failed to create AWS client") {
		t.Errorf("error = %q, want 'failed to create AWS client'", err.Error())
	}
}

func TestApply_ToolFailure_ContinuesToRuntime(t *testing.T) {
	sim := newSimulatedProvider()
	provider := &Provider{
		awsClientFunc: func(_ context.Context, cfg *Config) (awsClient, error) {
			return &failingAWSClient{
				simulatedAWSClient: *newSimulatedAWSClient(cfg.Region),
				failOn:             map[string]bool{"tool_gateway": true},
			}, nil
		},
		destroyerFunc: sim.destroyerFunc,
		checkerFunc:   sim.checkerFunc,
	}

	req := &deploy.PlanRequest{
		PackJSON:     singleAgentPackWithTools(),
		DeployConfig: validConfig(),
	}

	events, stateStr, err := collectEvents(t, provider, req)
	if err == nil {
		t.Fatal("expected error for tool failure")
	}

	// Should have error events for tool failures but resource events for runtime.
	var errorCount int
	var runtimeCreated bool
	for _, ev := range events {
		if ev.Type == "error" {
			errorCount++
		}
		if ev.Type == "resource" && ev.Resource != nil &&
			ev.Resource.Type == "agent_runtime" && ev.Resource.Status == "created" {
			runtimeCreated = true
		}
	}
	if errorCount == 0 {
		t.Error("expected error events for tool failures")
	}
	if !runtimeCreated {
		t.Error("expected agent_runtime to still be created despite tool failures")
	}

	// State should have failed tools and successful runtime.
	var state AdapterState
	if err := json.Unmarshal([]byte(stateStr), &state); err != nil {
		t.Fatalf("failed to unmarshal state: %v", err)
	}
	for _, r := range state.Resources {
		if r.Type == "tool_gateway" && r.Status != "failed" {
			t.Errorf("tool_gateway %q status = %q, want failed", r.Name, r.Status)
		}
		if r.Type == "agent_runtime" && r.Status != "created" {
			t.Errorf("agent_runtime %q status = %q, want created", r.Name, r.Status)
		}
	}
}

func TestApply_RuntimeFailure(t *testing.T) {
	sim := newSimulatedProvider()
	provider := &Provider{
		awsClientFunc: func(_ context.Context, cfg *Config) (awsClient, error) {
			return &failingAWSClient{
				simulatedAWSClient: *newSimulatedAWSClient(cfg.Region),
				failOn:             map[string]bool{"agent_runtime": true},
			}, nil
		},
		destroyerFunc: sim.destroyerFunc,
		checkerFunc:   sim.checkerFunc,
	}

	req := &deploy.PlanRequest{
		PackJSON:     singleAgentPack(),
		DeployConfig: validConfig(),
	}

	_, stateStr, err := collectEvents(t, provider, req)
	if err == nil {
		t.Fatal("expected error for runtime failure")
	}

	var state AdapterState
	if err := json.Unmarshal([]byte(stateStr), &state); err != nil {
		t.Fatalf("failed to unmarshal state: %v", err)
	}
	if len(state.Resources) != 1 {
		t.Fatalf("expected 1 resource, got %d", len(state.Resources))
	}
	if state.Resources[0].Status != "failed" {
		t.Errorf("runtime status = %q, want failed", state.Resources[0].Status)
	}
}

func TestApply_CallbackError_AbortsEarly(t *testing.T) {
	provider := newSimulatedProvider()
	req := &deploy.PlanRequest{
		PackJSON:     singleAgentPack(),
		DeployConfig: validConfig(),
	}

	callCount := 0
	callback := func(_ *deploy.ApplyEvent) error {
		callCount++
		if callCount >= 2 {
			return fmt.Errorf("callback abort")
		}
		return nil
	}
	_, err := provider.Apply(context.Background(), req, callback)
	if err == nil {
		t.Fatal("expected error from callback abort")
	}
	if !strings.Contains(err.Error(), "callback abort") {
		t.Errorf("error = %q, want callback abort", err.Error())
	}
}

func TestApply_EvalWithEmptyID(t *testing.T) {
	// Pack with evals that have empty IDs should use "eval_0", "eval_1" etc.
	p := map[string]any{
		"id":      "evalpack",
		"version": "v1.0.0",
		"name":    "Eval Pack",
		"prompts": map[string]any{
			"coordinator": map[string]any{
				"id": "coordinator", "name": "Coord",
				"system_template": "You coordinate.", "version": "v1.0.0",
			},
			"worker": map[string]any{
				"id": "worker", "name": "Worker",
				"system_template": "You work.", "version": "v1.0.0",
			},
		},
		"agents": map[string]any{
			"entry": "coordinator",
			"members": map[string]any{
				"coordinator": map[string]any{"description": "coord"},
				"worker":      map[string]any{"description": "work"},
			},
		},
		"evals": []map[string]any{
			{"id": "", "type": "latency"},
		},
		"template_engine": map[string]any{"version": "1.0", "syntax": "handlebars"},
	}
	packJSON, _ := json.Marshal(p)

	provider := newSimulatedProvider()
	req := &deploy.PlanRequest{
		PackJSON:     string(packJSON),
		DeployConfig: validConfig(),
	}

	events, _, err := collectEvents(t, provider, req)
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}

	// Find the evaluator resource event and check its name.
	for _, ev := range events {
		if ev.Type == "resource" && ev.Resource != nil && ev.Resource.Type == "evaluator" {
			if ev.Resource.Name != "eval_0" {
				t.Errorf("evaluator name = %q, want eval_0", ev.Resource.Name)
			}
			return
		}
	}
	t.Error("expected evaluator resource event")
}

func TestApply_EvalFailure(t *testing.T) {
	sim := newSimulatedProvider()
	provider := &Provider{
		awsClientFunc: func(_ context.Context, cfg *Config) (awsClient, error) {
			return &failingAWSClient{
				simulatedAWSClient: *newSimulatedAWSClient(cfg.Region),
				failOn:             map[string]bool{"evaluator": true},
			}, nil
		},
		destroyerFunc: sim.destroyerFunc,
		checkerFunc:   sim.checkerFunc,
	}

	req := &deploy.PlanRequest{
		PackJSON:     multiAgentPackWithEvals(),
		DeployConfig: validConfig(),
	}

	_, stateStr, err := collectEvents(t, provider, req)
	if err == nil {
		t.Fatal("expected error for evaluator failure")
	}

	var state AdapterState
	if err := json.Unmarshal([]byte(stateStr), &state); err != nil {
		t.Fatalf("failed to unmarshal state: %v", err)
	}

	for _, r := range state.Resources {
		if r.Type == "evaluator" && r.Status != "failed" {
			t.Errorf("evaluator %q status = %q, want failed", r.Name, r.Status)
		}
	}
}

// priorStateWithRuntime returns a PriorState JSON containing one agent_runtime.
func priorStateWithRuntime(name, arn string) string {
	state := AdapterState{
		PackID:  "mypack",
		Version: "v1.0.0",
		Resources: []ResourceState{
			{Type: ResTypeAgentRuntime, Name: name, ARN: arn, Status: "created"},
		},
	}
	b, _ := json.Marshal(state)
	return string(b)
}

func TestApply_SingleAgent_Update(t *testing.T) {
	provider := newSimulatedProvider()
	priorARN := "arn:aws:bedrock:us-west-2:123456789012:agent-runtime/mypack"
	req := &deploy.PlanRequest{
		PackJSON:     singleAgentPack(),
		DeployConfig: validConfig(),
		PriorState:   priorStateWithRuntime("mypack", priorARN),
	}

	events, stateStr, err := collectEvents(t, provider, req)
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}

	// Should have an update resource event, not a create.
	var foundUpdate bool
	for _, ev := range events {
		if ev.Type == "resource" && ev.Resource != nil &&
			ev.Resource.Type == "agent_runtime" &&
			ev.Resource.Action == deploy.ActionUpdate &&
			ev.Resource.Status == "updated" {
			foundUpdate = true
		}
		// Should NOT have a create event for the runtime.
		if ev.Type == "resource" && ev.Resource != nil &&
			ev.Resource.Type == "agent_runtime" &&
			ev.Resource.Action == deploy.ActionCreate {
			t.Error("expected ActionUpdate for existing runtime, got ActionCreate")
		}
	}
	if !foundUpdate {
		t.Error("expected a resource event with ActionUpdate and status=updated")
	}

	// Progress message should say "Updating" not "Creating".
	for _, ev := range events {
		if ev.Type == "progress" && strings.Contains(ev.Message, "agent_runtime") {
			if !strings.Contains(ev.Message, "Updating") {
				t.Errorf("progress message should say Updating, got %q", ev.Message)
			}
		}
	}

	// State should have the resource with status=updated and the prior ARN.
	var state AdapterState
	if err := json.Unmarshal([]byte(stateStr), &state); err != nil {
		t.Fatalf("failed to unmarshal state: %v", err)
	}
	if len(state.Resources) != 1 {
		t.Fatalf("expected 1 resource, got %d", len(state.Resources))
	}
	r := state.Resources[0]
	if r.Status != "updated" {
		t.Errorf("resource status = %q, want updated", r.Status)
	}
	if r.ARN != priorARN {
		t.Errorf("resource ARN = %q, want %q", r.ARN, priorARN)
	}
}

func TestApply_MixedCreateAndUpdate(t *testing.T) {
	provider := newSimulatedProvider()

	// Prior state has only "coordinator" runtime — "worker" is new.
	coordARN := "arn:aws:bedrock:us-west-2:123456789012:agent-runtime/coordinator"
	priorState := AdapterState{
		PackID:  "multipack",
		Version: "v1.0.0",
		Resources: []ResourceState{
			{Type: ResTypeAgentRuntime, Name: "coordinator", ARN: coordARN, Status: "created"},
			{Type: ResTypeToolGateway, Name: "lookup", ARN: "arn:aws:bedrock:us-west-2:123456789012:gateway-tool/lookup", Status: "created"},
		},
	}
	priorJSON, _ := json.Marshal(priorState)

	req := &deploy.PlanRequest{
		PackJSON:     multiAgentPack(),
		DeployConfig: validConfig(),
		PriorState:   string(priorJSON),
	}

	events, stateStr, err := collectEvents(t, provider, req)
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}

	// Collect resource events by name.
	actionByName := make(map[string]deploy.Action)
	statusByName := make(map[string]string)
	for _, ev := range events {
		if ev.Type == "resource" && ev.Resource != nil {
			actionByName[ev.Resource.Name] = ev.Resource.Action
			statusByName[ev.Resource.Name] = ev.Resource.Status
		}
	}

	// coordinator should be updated, worker should be created.
	if actionByName["coordinator"] != deploy.ActionUpdate {
		t.Errorf("coordinator action = %q, want %q", actionByName["coordinator"], deploy.ActionUpdate)
	}
	if statusByName["coordinator"] != "updated" {
		t.Errorf("coordinator status = %q, want updated", statusByName["coordinator"])
	}
	if actionByName["worker"] != deploy.ActionCreate {
		t.Errorf("worker action = %q, want %q", actionByName["worker"], deploy.ActionCreate)
	}
	if statusByName["worker"] != "created" {
		t.Errorf("worker status = %q, want created", statusByName["worker"])
	}

	// Verify state.
	var state AdapterState
	if err := json.Unmarshal([]byte(stateStr), &state); err != nil {
		t.Fatalf("failed to unmarshal state: %v", err)
	}
	for _, r := range state.Resources {
		if r.Type == ResTypeAgentRuntime && r.Name == "coordinator" {
			if r.Status != "updated" {
				t.Errorf("coordinator state status = %q, want updated", r.Status)
			}
			if r.ARN != coordARN {
				t.Errorf("coordinator ARN = %q, want %q (preserved from prior)", r.ARN, coordARN)
			}
		}
		if r.Type == ResTypeAgentRuntime && r.Name == "worker" {
			if r.Status != "created" {
				t.Errorf("worker state status = %q, want created", r.Status)
			}
		}
	}
}

func TestApply_UpdateRuntime_Failure(t *testing.T) {
	sim := newSimulatedProvider()
	provider := &Provider{
		awsClientFunc: func(_ context.Context, cfg *Config) (awsClient, error) {
			return &failingAWSClient{
				simulatedAWSClient: *newSimulatedAWSClient(cfg.Region),
				failOn:             map[string]bool{"agent_runtime_update": true},
			}, nil
		},
		destroyerFunc: sim.destroyerFunc,
		checkerFunc:   sim.checkerFunc,
	}

	priorARN := "arn:aws:bedrock:us-west-2:123456789012:agent-runtime/mypack"
	req := &deploy.PlanRequest{
		PackJSON:     singleAgentPack(),
		DeployConfig: validConfig(),
		PriorState:   priorStateWithRuntime("mypack", priorARN),
	}

	_, stateStr, err := collectEvents(t, provider, req)
	if err == nil {
		t.Fatal("expected error for update failure")
	}

	var state AdapterState
	if err := json.Unmarshal([]byte(stateStr), &state); err != nil {
		t.Fatalf("failed to unmarshal state: %v", err)
	}
	if len(state.Resources) != 1 {
		t.Fatalf("expected 1 resource, got %d", len(state.Resources))
	}
	if state.Resources[0].Status != "failed" {
		t.Errorf("runtime status = %q, want failed", state.Resources[0].Status)
	}
}

// validConfigWithMemory returns a deploy config with memory_store set.
func validConfigWithMemory() string {
	return `{"region":"us-west-2","runtime_role_arn":"arn:aws:iam::123456789012:role/test","memory_store":"session"}`
}

func TestApply_WithMemory_CreatesMemoryResource(t *testing.T) {
	provider := newSimulatedProvider()
	req := &deploy.PlanRequest{
		PackJSON:     singleAgentPack(),
		DeployConfig: validConfigWithMemory(),
	}

	events, stateStr, err := collectEvents(t, provider, req)
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}

	// Should have memory + runtime resource events.
	var resourceTypes []string
	for _, ev := range events {
		if ev.Type == "resource" && ev.Resource != nil {
			resourceTypes = append(resourceTypes, ev.Resource.Type)
		}
	}

	if len(resourceTypes) != 2 {
		t.Fatalf("expected 2 resource events (memory + runtime), got %d: %v",
			len(resourceTypes), resourceTypes)
	}
	if resourceTypes[0] != ResTypeMemory {
		t.Errorf("first resource should be memory, got %s", resourceTypes[0])
	}
	if resourceTypes[1] != ResTypeAgentRuntime {
		t.Errorf("second resource should be agent_runtime, got %s", resourceTypes[1])
	}

	// Verify state.
	var state AdapterState
	if err := json.Unmarshal([]byte(stateStr), &state); err != nil {
		t.Fatalf("failed to unmarshal state: %v", err)
	}
	if len(state.Resources) != 2 {
		t.Fatalf("expected 2 resources in state, got %d", len(state.Resources))
	}

	// Memory resource should have a valid ARN.
	memRes := state.Resources[0]
	if memRes.Type != ResTypeMemory {
		t.Errorf("first state resource type = %q, want memory", memRes.Type)
	}
	if memRes.ARN == "" {
		t.Error("memory resource has empty ARN")
	}
	if memRes.Status != "created" {
		t.Errorf("memory status = %q, want created", memRes.Status)
	}

	// Verify progress event for memory.
	var memoryProgress bool
	for _, ev := range events {
		if ev.Type == "progress" && strings.Contains(ev.Message, "memory") {
			memoryProgress = true
		}
	}
	if !memoryProgress {
		t.Error("expected a progress event mentioning memory")
	}
}

func TestApply_WithMemory_Failure(t *testing.T) {
	sim := newSimulatedProvider()
	provider := &Provider{
		awsClientFunc: func(_ context.Context, cfg *Config) (awsClient, error) {
			return &failingAWSClient{
				simulatedAWSClient: *newSimulatedAWSClient(cfg.Region),
				failOn:             map[string]bool{"memory": true},
			}, nil
		},
		destroyerFunc: sim.destroyerFunc,
		checkerFunc:   sim.checkerFunc,
	}

	req := &deploy.PlanRequest{
		PackJSON:     singleAgentPack(),
		DeployConfig: validConfigWithMemory(),
	}

	_, stateStr, err := collectEvents(t, provider, req)
	if err == nil {
		t.Fatal("expected error for memory failure")
	}

	// State should still contain the failed memory and the successful runtime.
	var state AdapterState
	if err := json.Unmarshal([]byte(stateStr), &state); err != nil {
		t.Fatalf("failed to unmarshal state: %v", err)
	}

	var memoryFailed, runtimeCreated bool
	for _, r := range state.Resources {
		if r.Type == ResTypeMemory && r.Status == "failed" {
			memoryFailed = true
		}
		if r.Type == ResTypeAgentRuntime && r.Status == "created" {
			runtimeCreated = true
		}
	}
	if !memoryFailed {
		t.Error("expected memory resource with status=failed")
	}
	if !runtimeCreated {
		t.Error("expected agent_runtime to still be created despite memory failure")
	}
}

func TestApply_WithoutMemory_NoMemoryResource(t *testing.T) {
	provider := newSimulatedProvider()
	req := &deploy.PlanRequest{
		PackJSON:     singleAgentPack(),
		DeployConfig: validConfig(),
	}

	events, _, err := collectEvents(t, provider, req)
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}

	for _, ev := range events {
		if ev.Type == "resource" && ev.Resource != nil && ev.Resource.Type == ResTypeMemory {
			t.Error("should not have memory resource when memory_store is not configured")
		}
	}
}

func TestApply_MultiAgent_EntryAgentGetsEndpoints(t *testing.T) {
	// Track UpdateRuntime calls to verify A2A discovery injection.
	var updateCalls []string
	sim := newSimulatedAWSClient("us-west-2")
	trackingClient := &trackingAWSClient{
		simulatedAWSClient: *sim,
		onUpdate: func(arn, name string) {
			updateCalls = append(updateCalls, name)
		},
	}

	provider := &Provider{
		awsClientFunc: func(_ context.Context, _ *Config) (awsClient, error) {
			return trackingClient, nil
		},
		destroyerFunc: newSimulatedProvider().destroyerFunc,
		checkerFunc:   newSimulatedProvider().checkerFunc,
	}

	req := &deploy.PlanRequest{
		PackJSON:     multiAgentPack(),
		DeployConfig: validConfig(),
	}

	events, _, err := collectEvents(t, provider, req)
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}

	// The entry agent "coordinator" should have been updated with A2A endpoints.
	found := false
	for _, name := range updateCalls {
		if name == "coordinator" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected UpdateRuntime call for entry agent 'coordinator', got: %v", updateCalls)
	}

	// Verify a progress event for A2A injection exists.
	for _, ev := range events {
		if ev.Type == "progress" && strings.Contains(ev.Message, "A2A endpoint map") {
			return
		}
	}
	t.Error("expected a progress event for A2A endpoint map injection")
}

// trackingAWSClient wraps simulatedAWSClient to record UpdateRuntime calls.
type trackingAWSClient struct {
	simulatedAWSClient
	onUpdate func(arn, name string)
}

func (c *trackingAWSClient) UpdateRuntime(
	ctx context.Context, arn string, name string, cfg *Config,
) (string, error) {
	if c.onUpdate != nil {
		c.onUpdate(arn, name)
	}
	return c.simulatedAWSClient.UpdateRuntime(ctx, arn, name, cfg)
}
