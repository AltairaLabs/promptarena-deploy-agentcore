package agentcore

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/AltairaLabs/PromptKit/runtime/deploy"
)

func priorWith(res ...ResourceState) *AdapterState {
	return &AdapterState{PackID: "mypack", Version: "v1.0.0", Resources: res}
}

func rt(name string) ResourceState {
	return ResourceState{
		Type: ResTypeAgentRuntime, Name: name, Status: ResStatusCreated,
		ARN: "arn:aws:bedrock-agentcore:us-west-2:1:runtime/" + name,
	}
}

// --- reconcilePriorState: provider-agnostic, no AWS specifics ---

func TestReconcilePriorState_DropsResourcesThatNoLongerExist(t *testing.T) {
	checker := &simulatedChecker{statusByName: map[string]string{"gone": StatusMissing}}
	prior := priorWith(rt("kept"), rt("gone"))

	got, drifted := reconcilePriorState(context.Background(), checker, prior)

	if len(got.Resources) != 1 || got.Resources[0].Name != "kept" {
		t.Fatalf("expected only the surviving resource, got %+v", got.Resources)
	}
	if len(drifted) != 1 || !strings.Contains(drifted[0], "gone") {
		t.Errorf("expected drift naming the deleted resource, got %v", drifted)
	}
}

// "unhealthy" means it exists but is degraded — still a resource to update,
// not one to recreate.
func TestReconcilePriorState_KeepsUnhealthyResources(t *testing.T) {
	checker := &simulatedChecker{statusByName: map[string]string{"sick": StatusUnhealthy}}
	prior := priorWith(rt("sick"))

	got, drifted := reconcilePriorState(context.Background(), checker, prior)

	if len(got.Resources) != 1 {
		t.Errorf("an unhealthy resource still exists and must be kept, got %+v", got.Resources)
	}
	if len(drifted) != 0 {
		t.Errorf("unhealthy is not drift, got %v", drifted)
	}
}

// A failed check is not evidence of absence. Dropping on error would plan a
// CREATE for a resource that exists, and apply would then hit a conflict.
func TestReconcilePriorState_KeepsResourceWhenCheckFails(t *testing.T) {
	checker := &simulatedChecker{errByName: map[string]error{"unknown": errors.New("throttled")}}
	prior := priorWith(rt("unknown"))

	got, drifted := reconcilePriorState(context.Background(), checker, prior)

	if len(got.Resources) != 1 {
		t.Errorf("a resource whose check failed must be kept, got %+v", got.Resources)
	}
	if len(drifted) != 0 {
		t.Errorf("a failed check is not drift, got %v", drifted)
	}
}

func TestReconcilePriorState_NilPriorIsNoOp(t *testing.T) {
	checker := &simulatedChecker{}
	got, drifted := reconcilePriorState(context.Background(), checker, nil)
	if got != nil {
		t.Errorf("nil prior should stay nil, got %+v", got)
	}
	if len(drifted) != 0 || len(checker.calls) != 0 {
		t.Errorf("nothing to reconcile; checker should not be called")
	}
}

func TestReconcilePriorState_PreservesStateMetadata(t *testing.T) {
	checker := &simulatedChecker{}
	prior := priorWith(rt("a"))

	got, _ := reconcilePriorState(context.Background(), checker, prior)
	if got.PackID != "mypack" || got.Version != "v1.0.0" {
		t.Errorf("state metadata must survive reconcile, got %+v", got)
	}
}

// --- Plan integration ---

// The case that motivated this: someone deletes a runtime in the console.
// Without reconcile, Plan diffs against stale state, reports no change, and
// Apply then fails updating a resource that is not there.
func TestPlan_DriftedResourceIsPlannedForCreation(t *testing.T) {
	p := newSimulatedProvider()
	checker := &simulatedChecker{statusByName: map[string]string{"mypack": StatusMissing}}
	p.checkerFunc = func(_ context.Context, _ *Config) (resourceChecker, error) {
		return checker, nil
	}

	prior := priorWith(rt("mypack"))
	resp, err := p.Plan(context.Background(), &deploy.PlanRequest{
		PackJSON:     singleAgentPackJSON(),
		DeployConfig: validDeployConfig,
		ArenaConfig:  validArenaConfigJSON,
		PriorState:   mustJSON(t, prior),
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}

	var runtimeAction deploy.Action
	for _, c := range resp.Changes {
		if c.Type == ResTypeAgentRuntime {
			runtimeAction = c.Action
		}
	}
	if runtimeAction != deploy.ActionCreate {
		t.Errorf("a resource deleted out of band should be planned as CREATE, got %q", runtimeAction)
	}
	if len(checker.calls) == 0 {
		t.Error("Plan should have verified prior state against the live provider")
	}
}

// Dry-run is the offline mode: it must not reach out to the provider.
func TestPlan_DryRunDoesNotVerifyAgainstProvider(t *testing.T) {
	p := newSimulatedProvider()
	checker := &simulatedChecker{}
	p.checkerFunc = func(_ context.Context, _ *Config) (resourceChecker, error) {
		return checker, nil
	}

	prior := priorWith(rt("mypack"))
	_, err := p.Plan(context.Background(), &deploy.PlanRequest{
		PackJSON: singleAgentPackJSON(),
		DeployConfig: `{"region":"us-west-2","runtime_role_arn":"arn:aws:iam::123456789012:role/test",` +
			`"runtime_binary_path":"/usr/local/bin/promptkit-runtime","dry_run":true}`,
		ArenaConfig: validArenaConfigJSON,
		PriorState:  mustJSON(t, prior),
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(checker.calls) != 0 {
		t.Errorf("dry-run must not call the provider, but checked %v", checker.calls)
	}
}

// Verification is best-effort: if the provider cannot be reached, Plan still
// works from stored state rather than failing.
func TestPlan_FallsBackToStoredStateWhenCheckerUnavailable(t *testing.T) {
	p := newSimulatedProvider()
	p.checkerFunc = func(_ context.Context, _ *Config) (resourceChecker, error) {
		return nil, errors.New("no credentials")
	}

	prior := priorWith(rt("mypack"))
	resp, err := p.Plan(context.Background(), &deploy.PlanRequest{
		PackJSON:     singleAgentPackJSON(),
		DeployConfig: validDeployConfig,
		ArenaConfig:  validArenaConfigJSON,
		PriorState:   mustJSON(t, prior),
	})
	if err != nil {
		t.Fatalf("Plan must not fail when the provider is unreachable: %v", err)
	}
	for _, c := range resp.Changes {
		if c.Type == ResTypeAgentRuntime && c.Action == deploy.ActionCreate {
			t.Error("with verification unavailable, stored state should stand and the runtime should not be recreated")
		}
	}
}
