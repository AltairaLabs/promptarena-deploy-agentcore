package agentcore

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/AltairaLabs/promptarena/v2/deploy"
)

const (
	priorCoordinatorARN = "arn:aws:bedrock-agentcore:us-west-2:123456789012:runtime/coordinator"
	priorWorkerARN      = "arn:aws:bedrock-agentcore:us-west-2:123456789012:runtime/worker"
	priorWiringARN      = "arn:aws:bedrock-agentcore:us-west-2:123456789012:runtime/coordinator_a2a"
	priorMemoryARN      = "arn:aws:bedrock-agentcore:us-west-2:123456789012:memory/multipack_memory"
	priorEvaluatorARN   = "arn:aws:bedrock-agentcore:us-west-2:123456789012:evaluator/retired_eval"
)

// failingProvider is a simulated provider whose AWS client fails the listed
// operations (see failingAWSClient).
func failingProvider(failOn map[string]bool) *Provider {
	sim := newSimulatedProvider()
	return &Provider{
		awsClientFunc: func(_ context.Context, cfg *Config) (awsClient, error) {
			return &failingAWSClient{
				simulatedAWSClient: *newSimulatedAWSClient(cfg.Region),
				failOn:             failOn,
			}, nil
		},
		destroyerFunc: sim.destroyerFunc,
		checkerFunc:   sim.checkerFunc,
	}
}

// multiAgentPriorState is a stored state from an earlier deploy of
// multiAgentPack: both runtimes, one A2A wiring, and a memory and an
// evaluator that the current config and pack no longer produce, so a later
// apply never reaches them.
func multiAgentPriorState(t *testing.T) string {
	t.Helper()
	b, err := json.Marshal(AdapterState{
		PackID: "multipack", Version: "v0.9.0",
		Resources: []ResourceState{
			{Type: ResTypeMemory, Name: "multipack_memory", ARN: priorMemoryARN, Status: ResStatusCreated},
			{Type: ResTypeAgentRuntime, Name: "coordinator", ARN: priorCoordinatorARN, Status: ResStatusCreated},
			{Type: ResTypeAgentRuntime, Name: "worker", ARN: priorWorkerARN, Status: ResStatusCreated},
			{Type: ResTypeA2AEndpoint, Name: "coordinator_a2a", ARN: priorWiringARN, Status: ResStatusCreated},
			{Type: ResTypeEvaluator, Name: "retired_eval", ARN: priorEvaluatorARN, Status: ResStatusCreated},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func stateByKey(state AdapterState) map[string]ResourceState {
	byKey := make(map[string]ResourceState, len(state.Resources))
	for _, r := range state.Resources {
		byKey[resourceKey(r.Type, r.Name)] = r
	}
	return byKey
}

// TestApply_PartialFailureKeepsUntouchedPriorResources pins the state a failed
// apply returns: the CLI saves it in place of the stored state, so the prior
// resources the apply never reached must survive, a resource whose create
// failed keeps its prior entry, and what the apply wrote is recorded.
func TestApply_PartialFailureKeepsUntouchedPriorResources(t *testing.T) {
	provider := failingProvider(map[string]bool{"a2a_endpoint": true})
	req := &deploy.PlanRequest{
		PackJSON:     multiAgentPack(),
		DeployConfig: validConfig(t),
		ArenaConfig:  validArenaConfigJSON,
		PriorState:   multiAgentPriorState(t),
	}

	_, stateStr, err := collectEvents(t, provider, req)
	if err == nil {
		t.Fatal("expected the a2a wiring failure to fail the apply")
	}

	state := decodeState(t, stateStr)
	byKey := stateByKey(state)
	for _, want := range []struct{ resType, name, arn string }{
		{ResTypeMemory, "multipack_memory", priorMemoryARN},
		{ResTypeEvaluator, "retired_eval", priorEvaluatorARN},
		{ResTypeA2AEndpoint, "coordinator_a2a", priorWiringARN},
	} {
		got, ok := byKey[resourceKey(want.resType, want.name)]
		if !ok {
			t.Errorf("prior %s %q was dropped from the state; got %+v", want.resType, want.name, state.Resources)
			continue
		}
		if got.ARN != want.arn || got.Status != ResStatusCreated {
			t.Errorf("%s %q = %+v, want the prior entry (ARN %q)", want.resType, want.name, got, want.arn)
		}
	}
	for _, name := range []string{"coordinator", "worker"} {
		if got := byKey[resourceKey(ResTypeAgentRuntime, name)]; got.Status != ResStatusUpdated {
			t.Errorf("runtime %q = %+v, want it recorded as updated", name, got)
		}
	}
	if got := byKey[resourceKey(ResTypeToolGateway, "lookup")]; got.Status != ResStatusCreated || got.ARN == "" {
		t.Errorf("tool gateway = %+v, want it recorded as created", got)
	}
	if _, ok := byKey[resourceKey(ResTypeA2AEndpoint, "worker_a2a")]; ok {
		t.Error("the a2a wiring whose create failed, with no prior entry, must not be recorded")
	}
	for _, r := range state.Resources {
		if r.Status == ResStatusFailed {
			t.Errorf("a failed write must not be recorded as a resource: %+v", r)
		}
	}
	if state.PackID != "multipack" || state.Version != "v1.0.0" {
		t.Errorf("state pack = %q@%q, want the pack being applied", state.PackID, state.Version)
	}
}

// TestApply_FailureOnFirstResourceReturnsPriorStateUnchanged pins an apply
// that fails without creating or updating anything: it returns the prior
// state verbatim, which the CLI recognizes and keeps.
func TestApply_FailureOnFirstResourceReturnsPriorStateUnchanged(t *testing.T) {
	provider := failingProvider(map[string]bool{
		"tool_gateway": true, "agent_runtime": true, "agent_runtime_update": true, "a2a_endpoint": true,
	})
	prior := multiAgentPriorState(t)
	req := &deploy.PlanRequest{
		PackJSON:     multiAgentPack(),
		DeployConfig: validConfig(t),
		ArenaConfig:  validArenaConfigJSON,
		PriorState:   prior,
	}

	_, stateStr, err := collectEvents(t, provider, req)
	if err == nil {
		t.Fatal("expected the apply to fail")
	}
	if stateStr != prior {
		t.Errorf("state = %s, want the prior state unchanged: %s", stateStr, prior)
	}
}

// TestApply_FreshDeployPartialFailureReturnsWhatItCreated covers a first
// deploy that fails partway: there is no prior state, so the state is exactly
// the resources the apply created, without the ones whose create failed.
func TestApply_FreshDeployPartialFailureReturnsWhatItCreated(t *testing.T) {
	provider := failingProvider(map[string]bool{"a2a_endpoint": true})
	req := &deploy.PlanRequest{
		PackJSON:     multiAgentPack(),
		DeployConfig: validConfig(t),
		ArenaConfig:  validArenaConfigJSON,
	}

	_, stateStr, err := collectEvents(t, provider, req)
	if err == nil {
		t.Fatal("expected the a2a wiring failure to fail the apply")
	}

	state := decodeState(t, stateStr)
	byKey := stateByKey(state)
	want := []string{
		resourceKey(ResTypeToolGateway, "lookup"),
		resourceKey(ResTypeAgentRuntime, "coordinator"),
		resourceKey(ResTypeAgentRuntime, "worker"),
	}
	if len(state.Resources) != len(want) {
		t.Fatalf("state = %+v, want exactly %v", state.Resources, want)
	}
	for _, key := range want {
		got, ok := byKey[key]
		if !ok || got.Status != ResStatusCreated || got.ARN == "" {
			t.Errorf("%s = %+v, want it recorded as created with its ARN", key, got)
		}
	}
}

func TestMergePartialResources(t *testing.T) {
	prior := []ResourceState{
		{Type: ResTypeMemory, Name: "m", ARN: "m0"},
		{Type: ResTypeAgentRuntime, Name: "a", ARN: "a0"},
		{Type: ResTypeAgentRuntime, Name: "a", ARN: "duplicate"},
		{Type: ResTypeToolGateway, Name: "g", ARN: "g0"},
		{Type: ResTypeEvaluator, Name: "dry-run", Status: ResStatusPlanned},
		{Type: ResTypeEvaluator, Name: "old-failure", Status: ResStatusFailed},
	}
	touched := []ResourceState{
		{Type: ResTypeAgentRuntime, Name: "a", ARN: "a1", Status: ResStatusUpdated},
		{Type: ResTypeToolGateway, Name: "g", Status: ResStatusFailed},
		{Type: ResTypeEvaluator, Name: "e", ARN: "e1", Status: ResStatusCreated},
		{Type: ResTypeCedarPolicy, Name: "never-created", Status: ResStatusFailed},
	}

	merged, wrote := mergePartialResources(prior, touched)
	if !wrote {
		t.Error("an update and a create were reported; wrote must be true")
	}
	want := []string{"m0", "a1", "g0", "e1"}
	if len(merged) != len(want) {
		t.Fatalf("merged = %+v, want ARNs %v", merged, want)
	}
	for i, arn := range want {
		if merged[i].ARN != arn {
			t.Errorf("merged[%d].ARN = %q, want %q (merged %+v)", i, merged[i].ARN, arn, merged)
		}
	}

	if _, wrote := mergePartialResources(prior, []ResourceState{
		{Type: ResTypeAgentRuntime, Name: "a", Status: ResStatusFailed},
	}); wrote {
		t.Error("nothing was created or updated; wrote must be false")
	}
}

func TestPartialApplyState_UnreadablePriorIsReturnedUnchanged(t *testing.T) {
	stateJSON, err := partialApplyState("not json", []ResourceState{
		{Type: ResTypeAgentRuntime, Name: "a", ARN: "a1", Status: ResStatusCreated},
	}, "pack", "v1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if stateJSON != "not json" {
		t.Errorf("state = %q, want the unreadable prior state unchanged, so the CLI keeps it", stateJSON)
	}
}

// notReadyRuntimeClient creates runtimes that never become ready: the create
// returns the new ARN together with an error, as the real client does.
type notReadyRuntimeClient struct {
	simulatedAWSClient
}

func (c *notReadyRuntimeClient) CreateRuntime(ctx context.Context, name string, cfg *Config) (string, error) {
	arn, err := c.simulatedAWSClient.CreateRuntime(ctx, name, cfg)
	if err != nil {
		return "", err
	}
	return arn, fmt.Errorf("runtime %q created but not ready", name)
}

// TestApply_CreatedButNotReadyIsRecorded covers a create that made the
// resource and then failed waiting for it: the resource exists, so a failed
// apply's state records it with its ARN for destroy to find.
func TestApply_CreatedButNotReadyIsRecorded(t *testing.T) {
	sim := newSimulatedProvider()
	provider := &Provider{
		awsClientFunc: func(_ context.Context, cfg *Config) (awsClient, error) {
			return &notReadyRuntimeClient{simulatedAWSClient: *newSimulatedAWSClient(cfg.Region)}, nil
		},
		destroyerFunc: sim.destroyerFunc,
		checkerFunc:   sim.checkerFunc,
	}
	req := &deploy.PlanRequest{
		PackJSON:     singleAgentPack(),
		DeployConfig: validConfig(t),
		ArenaConfig:  validArenaConfigJSON,
	}

	_, stateStr, err := collectEvents(t, provider, req)
	if err == nil {
		t.Fatal("expected the not-ready runtime to fail the apply")
	}
	state := decodeState(t, stateStr)
	if len(state.Resources) != 1 || state.Resources[0].ARN == "" || state.Resources[0].Status != ResStatusCreated {
		t.Errorf("want the runtime recorded as created with its ARN, got %+v", state.Resources)
	}
}

// TestApply_CedarFailureAfterEngineRecordsTheEngine covers a policy create
// that fails after its engine exists: the engine is recorded, so destroy can
// delete it, with the number of policies actually made.
func TestApply_CedarFailureAfterEngineRecordsTheEngine(t *testing.T) {
	provider := failingProvider(map[string]bool{"cedar_policy_create": true})
	req := &deploy.PlanRequest{
		PackJSON:     singleAgentPackWithToolPolicy(),
		DeployConfig: validConfig(t),
		ArenaConfig:  validArenaConfigJSON,
	}

	_, stateStr, err := collectEvents(t, provider, req)
	if err == nil {
		t.Fatal("expected the cedar policy failure to fail the apply")
	}
	state := decodeState(t, stateStr)
	var found bool
	for _, r := range state.Resources {
		if r.Type != ResTypeCedarPolicy {
			continue
		}
		found = true
		if r.Metadata["policy_engine_id"] == "" || r.Metadata["policy_count"] != "0" {
			t.Errorf("cedar_policy = %+v, want the engine id and a policy count of 0", r)
		}
	}
	if !found {
		t.Errorf("the policy engine that was created must be recorded, got %+v", state.Resources)
	}
}
