//go:build integration

package agentcore

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/AltairaLabs/PromptKit/runtime/deploy"
	"github.com/AltairaLabs/PromptKit/runtime/deploy/adaptersdk"
)

// Integration tests for the full adapter lifecycle using the real compiled
// messaround pack and real AWS credentials.
//
// Required env vars:
//
//	AGENTCORE_TEST_REGION          — AWS region (e.g. us-west-2)
//	AGENTCORE_TEST_ROLE_ARN        — IAM role ARN for the AgentCore runtime
//	AGENTCORE_TEST_CONTAINER_IMAGE — ECR image URI for the runtime container
//
// Run with:
//
//	GOWORK=off go test -tags=integration -v -run TestIntegration_Messaround -timeout=30m ./internal/agentcore/

// integrationDeployConfig returns a deploy config JSON from env vars,
// skipping the test if required vars are missing.
func integrationDeployConfig(t *testing.T) string {
	t.Helper()
	region := os.Getenv("AGENTCORE_TEST_REGION")
	roleARN := os.Getenv("AGENTCORE_TEST_ROLE_ARN")
	image := os.Getenv("AGENTCORE_TEST_CONTAINER_IMAGE")
	if region == "" || roleARN == "" {
		t.Skip("AGENTCORE_TEST_REGION and AGENTCORE_TEST_ROLE_ARN must be set")
	}
	if image == "" {
		t.Skip("AGENTCORE_TEST_CONTAINER_IMAGE must be set")
	}
	cfg := map[string]string{
		"region":           region,
		"runtime_role_arn": roleARN,
		"container_image":  image,
		"memory_store":     "semantic",
	}
	b, _ := json.Marshal(cfg)
	return string(b)
}

// integrationArenaConfig returns an arena config JSON string. When
// AGENTCORE_TEST_LAMBDA_ARN is set, the "search" tool spec includes a
// lambda_arn so the gateway uses an McpLambdaTargetConfiguration with
// an inline tool schema (required for Cedar policy actions to resolve).
func integrationArenaConfig(t *testing.T) string {
	t.Helper()
	lambdaARN := os.Getenv("AGENTCORE_TEST_LAMBDA_ARN")
	if lambdaARN == "" {
		return `{"tool_specs":{}}`
	}
	return fmt.Sprintf(`{
		"tool_specs": {
			"search": {
				"name": "search",
				"description": "Search the web for information",
				"lambda_arn": %q
			}
		}
	}`, lambdaARN)
}

// loadMessaroundPack reads the compiled pack JSON from the sibling repo.
func loadMessaroundPack(t *testing.T) string {
	t.Helper()
	path := "../../../messaround/my-agent.pack.json"
	data, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("messaround pack not found at %s: %v", path, err)
	}
	return string(data)
}

// integrationTimeout is the context timeout for integration tests.
// AWS resource creation (especially runtimes) can take several minutes.
const integrationTimeout = 20 * time.Minute

// integrationContext returns a context with the standard integration timeout.
func integrationContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), integrationTimeout)
	t.Cleanup(cancel)
	return ctx
}

// callAdapterWithProvider sends a JSON-RPC request through the given provider.
func callAdapterWithProvider(t *testing.T, provider *Provider, input string) jsonRPCResponse {
	t.Helper()
	var out bytes.Buffer
	err := adaptersdk.ServeIO(provider, strings.NewReader(input), &out)
	if err != nil {
		t.Fatalf("ServeIO error: %v", err)
	}
	var resp jsonRPCResponse
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response %q: %v", out.String(), err)
	}
	return resp
}

// unmarshalAdapterState deserializes AdapterState from a JSON string.
func unmarshalAdapterState(t *testing.T, stateStr string) AdapterState {
	t.Helper()
	var state AdapterState
	if err := json.Unmarshal([]byte(stateStr), &state); err != nil {
		t.Fatalf("failed to unmarshal adapter state: %v", err)
	}
	return state
}

// extractAdapterState pulls the adapter_state string from an apply JSON-RPC response.
func extractAdapterState(t *testing.T, resp jsonRPCResponse) string {
	t.Helper()
	if resp.Error != nil {
		t.Fatalf("unexpected JSON-RPC error: %s", resp.Error.Message)
	}
	var result struct {
		AdapterState string `json:"adapter_state"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("failed to unmarshal apply result: %v", err)
	}
	return result.AdapterState
}

// collectApplyEvents runs Apply and collects all emitted events.
func collectApplyEvents(
	t *testing.T, ctx context.Context, provider *Provider, req *deploy.PlanRequest,
) ([]deploy.ApplyEvent, string) {
	t.Helper()
	var events []deploy.ApplyEvent
	callback := func(ev *deploy.ApplyEvent) error {
		t.Logf("  apply event: type=%s resource=%v", ev.Type, ev.Resource)
		events = append(events, *ev)
		return nil
	}
	state, err := provider.Apply(ctx, req, callback)
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	return events, state
}

// destroyAndLog calls Destroy, logging events as they arrive.
// It registers a t.Cleanup so resources are cleaned up even on test failure.
func destroyAndLog(
	t *testing.T, provider *Provider, deployConfig, stateStr string,
) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), integrationTimeout)
	defer cancel()

	var events []deploy.DestroyEvent
	err := provider.Destroy(ctx, &deploy.DestroyRequest{
		DeployConfig: deployConfig,
		PriorState:   stateStr,
	}, func(ev *deploy.DestroyEvent) error {
		t.Logf("  destroy event: type=%s message=%s", ev.Type, ev.Message)
		events = append(events, *ev)
		return nil
	})
	if err != nil {
		t.Errorf("Destroy returned error: %v", err)
	}

	for _, ev := range events {
		if ev.Type == "error" {
			t.Errorf("destroy error event: %s", ev.Message)
		}
	}
}

// TestIntegration_Messaround_FullLifecycle exercises the complete adapter
// lifecycle — plan → apply → status → destroy — against real AWS using
// the compiled messaround pack.
func TestIntegration_Messaround_FullLifecycle(t *testing.T) {
	packJSON := loadMessaroundPack(t)
	deployConfig := integrationDeployConfig(t)
	arenaConfig := integrationArenaConfig(t)

	provider := NewProvider()
	ctx := integrationContext(t)

	req := &deploy.PlanRequest{
		PackJSON:     packJSON,
		DeployConfig: deployConfig,
		ArenaConfig:  arenaConfig,
	}

	// --- Plan ---
	t.Log("=== Phase: Plan ===")
	planResp, err := provider.Plan(ctx, req)
	if err != nil {
		t.Fatalf("Plan error: %v", err)
	}
	t.Logf("Plan summary: %s", planResp.Summary)
	for _, c := range planResp.Changes {
		t.Logf("  %s %s/%s: %s", c.Action, c.Type, c.Name, c.Detail)
	}
	if len(planResp.Changes) == 0 {
		t.Fatal("Plan returned no changes")
	}
	// All changes should be CREATE for a fresh deploy.
	for _, c := range planResp.Changes {
		if c.Action != deploy.ActionCreate {
			t.Errorf("expected CREATE for fresh deploy, got %s for %s/%s", c.Action, c.Type, c.Name)
		}
	}

	// --- Apply ---
	t.Log("=== Phase: Apply ===")
	events, stateStr := collectApplyEvents(t, ctx, provider, req)

	// Register cleanup ASAP so resources are destroyed even if later assertions fail.
	t.Cleanup(func() {
		t.Log("=== Cleanup: Destroy ===")
		destroyAndLog(t, provider, deployConfig, stateStr)
	})

	state := unmarshalAdapterState(t, stateStr)
	t.Logf("Apply state: pack_id=%s, %d resources", state.PackID, len(state.Resources))
	for _, r := range state.Resources {
		t.Logf("  %s/%s arn=%s status=%s", r.Type, r.Name, r.ARN, r.Status)
	}

	if state.PackID != "messaround" {
		t.Errorf("state.PackID = %q, want messaround", state.PackID)
	}

	// Verify all resources were created (not failed).
	for _, r := range state.Resources {
		if r.Status == ResStatusFailed {
			t.Errorf("resource %s/%s has status=failed", r.Type, r.Name)
		}
		if r.ARN == "" {
			t.Errorf("resource %s/%s has empty ARN", r.Type, r.Name)
		}
	}

	// Verify we got resource events for the expected types.
	typeSet := make(map[string]bool)
	for _, ev := range events {
		if ev.Type == "resource" && ev.Resource != nil {
			typeSet[ev.Resource.Type] = true
		}
	}
	for _, expected := range []string{
		ResTypeMemory, ResTypeCedarPolicy, ResTypeToolGateway,
		ResTypeAgentRuntime, ResTypeEvaluator, ResTypeOnlineEvalConfig,
	} {
		if !typeSet[expected] {
			t.Errorf("missing resource event for type %s", expected)
		}
	}

	// --- Status ---
	t.Log("=== Phase: Status ===")
	statusResp, err := provider.Status(ctx, &deploy.StatusRequest{
		DeployConfig: deployConfig,
		PriorState:   stateStr,
	})
	if err != nil {
		t.Fatalf("Status error: %v", err)
	}
	t.Logf("Status: %s (%d resources)", statusResp.Status, len(statusResp.Resources))
	for _, r := range statusResp.Resources {
		t.Logf("  %s/%s = %s", r.Type, r.Name, r.Status)
	}

	if statusResp.Status != "deployed" {
		t.Errorf("status = %q, want deployed", statusResp.Status)
	}
	for _, r := range statusResp.Resources {
		if r.Status != StatusHealthy {
			t.Errorf("resource %s/%s health = %q, want healthy", r.Type, r.Name, r.Status)
		}
	}

	// --- Destroy is handled by t.Cleanup above ---
	// We also explicitly test it here for assertions.
	t.Log("=== Phase: Destroy (explicit) ===")
	var destroyEvents []deploy.DestroyEvent
	err = provider.Destroy(ctx, &deploy.DestroyRequest{
		DeployConfig: deployConfig,
		PriorState:   stateStr,
	}, func(ev *deploy.DestroyEvent) error {
		t.Logf("  destroy: type=%s message=%s", ev.Type, ev.Message)
		destroyEvents = append(destroyEvents, *ev)
		return nil
	})
	if err != nil {
		t.Fatalf("Destroy error: %v", err)
	}

	// Verify all resource deletions succeeded.
	for _, ev := range destroyEvents {
		if ev.Type == "error" {
			t.Errorf("destroy error: %s", ev.Message)
		}
	}

	// Verify resources were deleted.
	var deletedCount int
	for _, ev := range destroyEvents {
		if ev.Type == "resource" && ev.Resource != nil && ev.Resource.Status == "deleted" {
			deletedCount++
		}
	}
	if deletedCount == 0 {
		t.Error("expected at least one deleted resource event")
	}

	// Clear state so cleanup is a no-op (already destroyed).
	stateStr = ""
}

// TestIntegration_Messaround_Redeploy exercises apply → re-apply (update)
// to verify idempotent resource handling.
func TestIntegration_Messaround_Redeploy(t *testing.T) {
	packJSON := loadMessaroundPack(t)
	deployConfig := integrationDeployConfig(t)
	arenaConfig := integrationArenaConfig(t)

	provider := NewProvider()
	ctx := integrationContext(t)

	req := &deploy.PlanRequest{
		PackJSON:     packJSON,
		DeployConfig: deployConfig,
		ArenaConfig:  arenaConfig,
	}

	// First apply.
	t.Log("=== First Apply ===")
	_, stateStr := collectApplyEvents(t, ctx, provider, req)

	t.Cleanup(func() {
		t.Log("=== Cleanup: Destroy ===")
		destroyAndLog(t, provider, deployConfig, stateStr)
	})

	// Second apply with prior state (should trigger updates, not creates).
	t.Log("=== Second Apply (redeploy) ===")
	req.PriorState = stateStr
	events, stateStr2 := collectApplyEvents(t, ctx, provider, req)

	state2 := unmarshalAdapterState(t, stateStr2)
	t.Logf("Redeploy state: %d resources", len(state2.Resources))
	for _, r := range state2.Resources {
		t.Logf("  %s/%s status=%s", r.Type, r.Name, r.Status)
	}

	// Verify we got update events for resources that support it.
	var updateCount int
	for _, ev := range events {
		if ev.Type == "resource" && ev.Resource != nil &&
			ev.Resource.Action == deploy.ActionUpdate {
			updateCount++
		}
	}
	if updateCount == 0 {
		t.Error("expected at least one UPDATE resource event on redeploy")
	}

	// Update stateStr for cleanup.
	stateStr = stateStr2
}

// TestIntegration_Messaround_JSONRPC exercises the JSON-RPC protocol path
// with real AWS, verifying apply → status → destroy over stdio.
func TestIntegration_Messaround_JSONRPC(t *testing.T) {
	packJSON := loadMessaroundPack(t)
	deployConfig := integrationDeployConfig(t)
	arenaConfig := integrationArenaConfig(t)

	provider := NewProvider()

	// Apply via JSON-RPC.
	t.Log("=== Apply (JSON-RPC) ===")
	applyParams := map[string]string{
		"pack_json":     packJSON,
		"deploy_config": deployConfig,
		"arena_config":  arenaConfig,
	}
	applyResp := callAdapterWithProvider(t, provider, jsonRPCRequest("apply", 1, applyParams))
	stateStr := extractAdapterState(t, applyResp)
	state := unmarshalAdapterState(t, stateStr)
	t.Logf("Apply: pack_id=%s, %d resources", state.PackID, len(state.Resources))

	t.Cleanup(func() {
		t.Log("=== Cleanup: Destroy ===")
		destroyAndLog(t, provider, deployConfig, stateStr)
	})

	if state.PackID != "messaround" {
		t.Errorf("state.PackID = %q, want messaround", state.PackID)
	}
	for _, r := range state.Resources {
		if r.ARN == "" {
			t.Errorf("resource %s/%s has empty ARN", r.Type, r.Name)
		}
	}

	// Status via JSON-RPC.
	t.Log("=== Status (JSON-RPC) ===")
	statusParams := map[string]string{
		"deploy_config": deployConfig,
		"prior_state":   stateStr,
	}
	statusResp := callAdapterWithProvider(t, provider, jsonRPCRequest("status", 2, statusParams))
	if statusResp.Error != nil {
		t.Fatalf("Status JSON-RPC error: %s", statusResp.Error.Message)
	}
	var statusResult struct {
		Status    string `json:"status"`
		Resources []struct {
			Type   string `json:"type"`
			Status string `json:"status"`
		} `json:"resources"`
	}
	if err := json.Unmarshal(statusResp.Result, &statusResult); err != nil {
		t.Fatalf("failed to unmarshal status result: %v", err)
	}
	t.Logf("Status: %s, %d resources", statusResult.Status, len(statusResult.Resources))
	if statusResult.Status != "deployed" {
		t.Errorf("status = %q, want deployed", statusResult.Status)
	}

	// Destroy via JSON-RPC.
	t.Log("=== Destroy (JSON-RPC) ===")
	destroyParams := map[string]string{
		"deploy_config": deployConfig,
		"prior_state":   stateStr,
	}
	destroyResp := callAdapterWithProvider(t, provider, jsonRPCRequest("destroy", 3, destroyParams))
	if destroyResp.Error != nil {
		t.Fatalf("Destroy JSON-RPC error: %s", destroyResp.Error.Message)
	}

	// Clear state so cleanup is a no-op.
	stateStr = ""
}

// integrationMultiAgentPackJSON returns a minimal multi-agent pack JSON with
// two members (coordinator and worker). No tools or evals — those are covered
// by the messaround tests above. This exercises a2a_endpoint resources.
func integrationMultiAgentPackJSON() string {
	p := map[string]any{
		"id":      "multi_agent_test",
		"name":    "Multi-Agent Integration Test",
		"version": "v1.0.0",
		"prompts": map[string]any{
			"coordinator": map[string]any{
				"id":              "coordinator",
				"name":            "Coordinator",
				"description":     "Entry agent that coordinates workers",
				"system_template": "You are a coordinator agent.",
			},
			"worker": map[string]any{
				"id":              "worker",
				"name":            "Worker",
				"description":     "Worker agent that processes tasks",
				"system_template": "You are a worker agent.",
			},
		},
		"agents": map[string]any{
			"entry": "coordinator",
			"members": map[string]any{
				"coordinator": map[string]any{
					"description": "Entry coordinator agent",
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

// TestIntegration_MultiAgent_FullLifecycle exercises a multi-agent pack through
// apply → status → destroy. This covers a2a_endpoint and memory resource types
// not exercised by the messaround single-agent tests.
func TestIntegration_MultiAgent_FullLifecycle(t *testing.T) {
	packJSON := integrationMultiAgentPackJSON()
	deployConfig := integrationDeployConfig(t)
	arenaConfig := `{"tool_specs":{}}`

	provider := NewProvider()
	ctx := integrationContext(t)

	req := &deploy.PlanRequest{
		PackJSON:     packJSON,
		DeployConfig: deployConfig,
		ArenaConfig:  arenaConfig,
	}

	// --- Apply ---
	t.Log("=== Phase: Apply ===")
	events, stateStr := collectApplyEvents(t, ctx, provider, req)

	t.Cleanup(func() {
		t.Log("=== Cleanup: Destroy ===")
		destroyAndLog(t, provider, deployConfig, stateStr)
	})

	state := unmarshalAdapterState(t, stateStr)
	t.Logf("Apply state: pack_id=%s, %d resources", state.PackID, len(state.Resources))
	for _, r := range state.Resources {
		t.Logf("  %s/%s arn=%s status=%s", r.Type, r.Name, r.ARN, r.Status)
	}

	if state.PackID != "multi_agent_test" {
		t.Errorf("state.PackID = %q, want multi_agent_test", state.PackID)
	}

	// Verify all resources were created successfully.
	for _, r := range state.Resources {
		if r.Status == ResStatusFailed {
			t.Errorf("resource %s/%s has status=failed", r.Type, r.Name)
		}
		if r.ARN == "" {
			t.Errorf("resource %s/%s has empty ARN", r.Type, r.Name)
		}
	}

	// Verify we got resource events for the expected types (including a2a_endpoint
	// and memory, which are not covered by the messaround tests).
	typeSet := make(map[string]bool)
	for _, ev := range events {
		if ev.Type == "resource" && ev.Resource != nil {
			typeSet[ev.Resource.Type] = true
		}
	}
	for _, expected := range []string{
		ResTypeMemory, ResTypeAgentRuntime, ResTypeA2AEndpoint,
	} {
		if !typeSet[expected] {
			t.Errorf("missing resource event for type %s", expected)
		}
	}

	// --- Status ---
	t.Log("=== Phase: Status ===")
	statusResp, err := provider.Status(ctx, &deploy.StatusRequest{
		DeployConfig: deployConfig,
		PriorState:   stateStr,
	})
	if err != nil {
		t.Fatalf("Status error: %v", err)
	}
	t.Logf("Status: %s (%d resources)", statusResp.Status, len(statusResp.Resources))
	for _, r := range statusResp.Resources {
		t.Logf("  %s/%s = %s", r.Type, r.Name, r.Status)
	}

	if statusResp.Status != "deployed" {
		t.Errorf("status = %q, want deployed", statusResp.Status)
	}
	for _, r := range statusResp.Resources {
		if r.Status != StatusHealthy {
			t.Errorf("resource %s/%s health = %q, want healthy", r.Type, r.Name, r.Status)
		}
	}

	// --- Destroy ---
	t.Log("=== Phase: Destroy ===")
	var destroyEvents []deploy.DestroyEvent
	err = provider.Destroy(ctx, &deploy.DestroyRequest{
		DeployConfig: deployConfig,
		PriorState:   stateStr,
	}, func(ev *deploy.DestroyEvent) error {
		t.Logf("  destroy: type=%s message=%s", ev.Type, ev.Message)
		destroyEvents = append(destroyEvents, *ev)
		return nil
	})
	if err != nil {
		t.Fatalf("Destroy error: %v", err)
	}

	for _, ev := range destroyEvents {
		if ev.Type == "error" {
			t.Errorf("destroy error: %s", ev.Message)
		}
	}

	var deletedCount int
	for _, ev := range destroyEvents {
		if ev.Type == "resource" && ev.Resource != nil && ev.Resource.Status == "deleted" {
			deletedCount++
		}
	}
	if deletedCount == 0 {
		t.Error("expected at least one deleted resource event")
	}

	// Clear state so cleanup is a no-op.
	stateStr = ""
}
