package agentcore

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/AltairaLabs/PromptKit/runtime/deploy"
)

// failingDestroyer returns errors for specific resource types.
type failingDestroyer struct {
	failOn map[string]bool
}

func (d *failingDestroyer) DeleteResource(_ context.Context, res ResourceState) error {
	if d.failOn[res.Type] {
		return fmt.Errorf("simulated delete failure for %s %q", res.Type, res.Name)
	}
	return nil
}

// failingChecker returns unhealthy or errors for specific resource types.
type failingChecker struct {
	unhealthyTypes map[string]bool
	errorTypes     map[string]bool
}

func (c *failingChecker) CheckResource(_ context.Context, res ResourceState) (string, error) {
	if c.errorTypes[res.Type] {
		return "", fmt.Errorf("simulated check error for %s %q", res.Type, res.Name)
	}
	if c.unhealthyTypes[res.Type] {
		return "unhealthy", nil
	}
	return "healthy", nil
}

// sampleState builds an AdapterState with the four standard resource types.
func sampleState() *AdapterState {
	return &AdapterState{
		PackID:     "test-pack",
		Version:    "1",
		DeployedAt: "2026-02-16T00:00:00Z",
		Resources: []ResourceState{
			{Type: "tool_gateway", Name: "tg-1", ARN: "arn:aws:bedrock:us-west-2:123456789012:tool-gateway/tg-1"},
			{Type: "agent_runtime", Name: "rt-1", ARN: "arn:aws:bedrock:us-west-2:123456789012:agent-runtime/rt-1"},
			{Type: "a2a_endpoint", Name: "a2a-1", ARN: "arn:aws:bedrock:us-west-2:123456789012:a2a/a2a-1"},
			{Type: "evaluator", Name: "ev-1", ARN: "arn:aws:bedrock:us-west-2:123456789012:evaluator/ev-1"},
		},
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	return string(b)
}

// validDestroyConfig returns a valid config for destroy/status tests.
func validDestroyConfig() string {
	return `{"region":"us-west-2","runtime_role_arn":"arn:aws:iam::123456789012:role/test"}`
}

// ---------- Destroy tests ----------

func TestDestroy_ValidState_ReverseOrder(t *testing.T) {
	p := newSimulatedProvider()
	state := sampleState()

	var events []*deploy.DestroyEvent
	cb := func(e *deploy.DestroyEvent) error {
		events = append(events, e)
		return nil
	}

	err := p.Destroy(context.Background(), &deploy.DestroyRequest{
		DeployConfig: validDestroyConfig(),
		PriorState:   mustJSON(t, state),
	}, cb)
	if err != nil {
		t.Fatalf("Destroy returned error: %v", err)
	}

	// Collect resource deletion events in order.
	var deletedTypes []string
	for _, e := range events {
		if e.Type == "resource" && e.Resource != nil && e.Resource.Status == "deleted" {
			deletedTypes = append(deletedTypes, e.Resource.Type)
		}
	}

	// Expected reverse dependency order: evaluator, a2a_endpoint, agent_runtime, tool_gateway.
	expected := []string{"evaluator", "a2a_endpoint", "agent_runtime", "tool_gateway"}
	if len(deletedTypes) != len(expected) {
		t.Fatalf("deleted %d resources, want %d: %v", len(deletedTypes), len(expected), deletedTypes)
	}
	for i, want := range expected {
		if deletedTypes[i] != want {
			t.Errorf("deletion[%d] = %q, want %q", i, deletedTypes[i], want)
		}
	}

	// Last event should be "complete".
	last := events[len(events)-1]
	if last.Type != "complete" {
		t.Errorf("last event type = %q, want complete", last.Type)
	}
}

func TestDestroy_EmptyState(t *testing.T) {
	p := newSimulatedProvider()

	var events []*deploy.DestroyEvent
	cb := func(e *deploy.DestroyEvent) error {
		events = append(events, e)
		return nil
	}

	err := p.Destroy(context.Background(), &deploy.DestroyRequest{
		DeployConfig: validDestroyConfig(),
		PriorState:   "",
	}, cb)
	if err != nil {
		t.Fatalf("Destroy returned error: %v", err)
	}

	if len(events) < 1 {
		t.Fatal("expected at least one event")
	}
	// Should have a complete event.
	foundComplete := false
	for _, e := range events {
		if e.Type == "complete" {
			foundComplete = true
		}
	}
	if !foundComplete {
		t.Error("expected a complete event")
	}

	// No resource events should be emitted.
	for _, e := range events {
		if e.Type == "resource" {
			t.Errorf("unexpected resource event for empty state: %+v", e)
		}
	}
}

func TestDestroy_AlreadyDeletedResources(t *testing.T) {
	// The simulated destroyer always succeeds, which models the
	// "already deleted" case — no error, just a deletion event.
	p := newSimulatedProvider()
	state := &AdapterState{
		Resources: []ResourceState{
			{Type: "agent_runtime", Name: "gone-rt", ARN: ""},
		},
	}

	var events []*deploy.DestroyEvent
	cb := func(e *deploy.DestroyEvent) error {
		events = append(events, e)
		return nil
	}

	err := p.Destroy(context.Background(), &deploy.DestroyRequest{
		DeployConfig: validDestroyConfig(),
		PriorState:   mustJSON(t, state),
	}, cb)
	if err != nil {
		t.Fatalf("Destroy returned error: %v", err)
	}

	// Should emit a resource event with status "deleted" even for already-gone resources.
	foundResource := false
	for _, e := range events {
		if e.Type == "resource" && e.Resource != nil {
			foundResource = true
			if e.Resource.Status != "deleted" {
				t.Errorf("resource status = %q, want deleted", e.Resource.Status)
			}
			if e.Resource.Name != "gone-rt" {
				t.Errorf("resource name = %q, want gone-rt", e.Resource.Name)
			}
		}
	}
	if !foundResource {
		t.Error("expected at least one resource event")
	}
}

func TestDestroy_InvalidState(t *testing.T) {
	p := newSimulatedProvider()

	err := p.Destroy(context.Background(), &deploy.DestroyRequest{
		DeployConfig: validDestroyConfig(),
		PriorState:   `{bad json`,
	}, func(e *deploy.DestroyEvent) error { return nil })

	if err == nil {
		t.Fatal("expected error for invalid state JSON")
	}
}

// ---------- Status tests ----------

func TestStatus_ValidState_Deployed(t *testing.T) {
	p := newSimulatedProvider()
	state := sampleState()

	resp, err := p.Status(context.Background(), &deploy.StatusRequest{
		DeployConfig: validDestroyConfig(),
		PriorState:   mustJSON(t, state),
	})
	if err != nil {
		t.Fatalf("Status returned error: %v", err)
	}

	if resp.Status != "deployed" {
		t.Errorf("status = %q, want deployed", resp.Status)
	}
	if len(resp.Resources) != len(state.Resources) {
		t.Fatalf("got %d resources, want %d", len(resp.Resources), len(state.Resources))
	}

	// All should be healthy.
	for i, r := range resp.Resources {
		if r.Status != "healthy" {
			t.Errorf("resource[%d] status = %q, want healthy", i, r.Status)
		}
	}

	// State should round-trip.
	if resp.State == "" {
		t.Error("expected non-empty state in response")
	}
}

func TestStatus_EmptyState_NotDeployed(t *testing.T) {
	p := newSimulatedProvider()

	resp, err := p.Status(context.Background(), &deploy.StatusRequest{
		DeployConfig: validDestroyConfig(),
		PriorState:   "",
	})
	if err != nil {
		t.Fatalf("Status returned error: %v", err)
	}

	if resp.Status != "not_deployed" {
		t.Errorf("status = %q, want not_deployed", resp.Status)
	}
	if len(resp.Resources) != 0 {
		t.Errorf("expected 0 resources, got %d", len(resp.Resources))
	}
}

func TestStatus_ResourceListMatchesState(t *testing.T) {
	p := newSimulatedProvider()
	state := sampleState()

	resp, err := p.Status(context.Background(), &deploy.StatusRequest{
		DeployConfig: validDestroyConfig(),
		PriorState:   mustJSON(t, state),
	})
	if err != nil {
		t.Fatalf("Status returned error: %v", err)
	}

	if len(resp.Resources) != len(state.Resources) {
		t.Fatalf("resource count mismatch: got %d, want %d", len(resp.Resources), len(state.Resources))
	}

	for i, want := range state.Resources {
		got := resp.Resources[i]
		if got.Type != want.Type {
			t.Errorf("resource[%d].Type = %q, want %q", i, got.Type, want.Type)
		}
		if got.Name != want.Name {
			t.Errorf("resource[%d].Name = %q, want %q", i, got.Name, want.Name)
		}
	}
}

func TestStatus_InvalidState(t *testing.T) {
	p := newSimulatedProvider()

	_, err := p.Status(context.Background(), &deploy.StatusRequest{
		DeployConfig: validDestroyConfig(),
		PriorState:   `{bad json`,
	})
	if err == nil {
		t.Fatal("expected error for invalid state JSON")
	}
}

// ---------- parseAdapterState tests ----------

func TestParseAdapterState_EmptyString(t *testing.T) {
	s, err := parseAdapterState("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(s.Resources) != 0 {
		t.Errorf("expected 0 resources, got %d", len(s.Resources))
	}
}

func TestParseAdapterState_ValidJSON(t *testing.T) {
	state := sampleState()
	raw := mustJSON(t, state)

	s, err := parseAdapterState(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(s.Resources) != len(state.Resources) {
		t.Errorf("got %d resources, want %d", len(s.Resources), len(state.Resources))
	}
	if s.PackID != "test-pack" {
		t.Errorf("pack_id = %q, want test-pack", s.PackID)
	}
}

func TestParseAdapterState_InvalidJSON(t *testing.T) {
	_, err := parseAdapterState(`not json`)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

// ---------- Destroy error path tests ----------

func TestDestroy_FailingDestroyer_EmitsErrorEvents(t *testing.T) {
	p := &Provider{
		awsClientFunc: nil,
		destroyerFunc: func(_ context.Context, _ *Config) (resourceDestroyer, error) {
			return &failingDestroyer{failOn: map[string]bool{"agent_runtime": true}}, nil
		},
		checkerFunc: nil,
	}

	state := &AdapterState{
		Resources: []ResourceState{
			{Type: "agent_runtime", Name: "rt-1", ARN: "arn:test"},
			{Type: "tool_gateway", Name: "tg-1", ARN: "arn:test2"},
		},
	}

	var events []*deploy.DestroyEvent
	cb := func(e *deploy.DestroyEvent) error {
		events = append(events, e)
		return nil
	}

	err := p.Destroy(context.Background(), &deploy.DestroyRequest{
		DeployConfig: validDestroyConfig(),
		PriorState:   mustJSON(t, state),
	}, cb)
	if err != nil {
		t.Fatalf("Destroy returned error: %v", err)
	}

	// Should have an error event for agent_runtime and a resource event for tool_gateway.
	var errorCount, resourceCount int
	for _, e := range events {
		switch e.Type {
		case "error":
			errorCount++
		case "resource":
			resourceCount++
		}
	}
	if errorCount == 0 {
		t.Error("expected at least one error event for failing destroyer")
	}
	if resourceCount == 0 {
		t.Error("expected at least one resource event for successful delete")
	}
}

func TestDestroy_UnknownResourceType_StillDeleted(t *testing.T) {
	p := newSimulatedProvider()
	state := &AdapterState{
		Resources: []ResourceState{
			{Type: "custom_thing", Name: "c-1", ARN: "arn:custom"},
		},
	}

	var events []*deploy.DestroyEvent
	cb := func(e *deploy.DestroyEvent) error {
		events = append(events, e)
		return nil
	}

	err := p.Destroy(context.Background(), &deploy.DestroyRequest{
		DeployConfig: validDestroyConfig(),
		PriorState:   mustJSON(t, state),
	}, cb)
	if err != nil {
		t.Fatalf("Destroy returned error: %v", err)
	}

	// The unknown type should still get a resource event via the fallthrough loop.
	foundCustom := false
	for _, e := range events {
		if e.Type == "resource" && e.Resource != nil && e.Resource.Type == "custom_thing" {
			foundCustom = true
		}
	}
	if !foundCustom {
		t.Error("expected resource event for unknown type 'custom_thing'")
	}
}

func TestDestroy_InvalidConfig(t *testing.T) {
	p := newSimulatedProvider()
	state := sampleState()

	err := p.Destroy(context.Background(), &deploy.DestroyRequest{
		DeployConfig: `{invalid}`,
		PriorState:   mustJSON(t, state),
	}, func(e *deploy.DestroyEvent) error { return nil })
	if err == nil {
		t.Fatal("expected error for invalid config")
	}
}

func TestDestroy_DestroyerFactoryError(t *testing.T) {
	p := &Provider{
		destroyerFunc: func(_ context.Context, _ *Config) (resourceDestroyer, error) {
			return nil, fmt.Errorf("factory failed")
		},
	}
	state := sampleState()

	err := p.Destroy(context.Background(), &deploy.DestroyRequest{
		DeployConfig: validDestroyConfig(),
		PriorState:   mustJSON(t, state),
	}, func(e *deploy.DestroyEvent) error { return nil })
	if err == nil {
		t.Fatal("expected error for factory failure")
	}
}

// ---------- Status error path tests ----------

func TestStatus_UnhealthyResource_ReturnsDegraded(t *testing.T) {
	p := &Provider{
		checkerFunc: func(_ context.Context, _ *Config) (resourceChecker, error) {
			return &failingChecker{unhealthyTypes: map[string]bool{"agent_runtime": true}}, nil
		},
	}
	state := &AdapterState{
		Resources: []ResourceState{
			{Type: "agent_runtime", Name: "rt-1"},
			{Type: "tool_gateway", Name: "tg-1"},
		},
	}

	resp, err := p.Status(context.Background(), &deploy.StatusRequest{
		DeployConfig: validDestroyConfig(),
		PriorState:   mustJSON(t, state),
	})
	if err != nil {
		t.Fatalf("Status returned error: %v", err)
	}
	if resp.Status != "degraded" {
		t.Errorf("status = %q, want degraded", resp.Status)
	}
}

func TestStatus_CheckerReturnsError_MarksUnhealthy(t *testing.T) {
	p := &Provider{
		checkerFunc: func(_ context.Context, _ *Config) (resourceChecker, error) {
			return &failingChecker{errorTypes: map[string]bool{"evaluator": true}}, nil
		},
	}
	state := &AdapterState{
		Resources: []ResourceState{
			{Type: "evaluator", Name: "ev-1"},
		},
	}

	resp, err := p.Status(context.Background(), &deploy.StatusRequest{
		DeployConfig: validDestroyConfig(),
		PriorState:   mustJSON(t, state),
	})
	if err != nil {
		t.Fatalf("Status returned error: %v", err)
	}
	if resp.Status != "degraded" {
		t.Errorf("status = %q, want degraded", resp.Status)
	}
	if len(resp.Resources) != 1 || resp.Resources[0].Status != "unhealthy" {
		t.Errorf("expected unhealthy resource, got %+v", resp.Resources)
	}
}

func TestStatus_InvalidConfig(t *testing.T) {
	p := newSimulatedProvider()
	state := sampleState()

	_, err := p.Status(context.Background(), &deploy.StatusRequest{
		DeployConfig: `{invalid}`,
		PriorState:   mustJSON(t, state),
	})
	if err == nil {
		t.Fatal("expected error for invalid config")
	}
}

func TestStatus_CheckerFactoryError(t *testing.T) {
	p := &Provider{
		checkerFunc: func(_ context.Context, _ *Config) (resourceChecker, error) {
			return nil, fmt.Errorf("checker factory failed")
		},
	}
	state := sampleState()

	_, err := p.Status(context.Background(), &deploy.StatusRequest{
		DeployConfig: validDestroyConfig(),
		PriorState:   mustJSON(t, state),
	})
	if err == nil {
		t.Fatal("expected error for checker factory failure")
	}
}

// ---------- isInDestroyOrder tests ----------

func TestIsInDestroyOrder(t *testing.T) {
	knownTypes := []string{"evaluator", "a2a_endpoint", "agent_runtime", "tool_gateway"}
	for _, typ := range knownTypes {
		if !isInDestroyOrder(typ) {
			t.Errorf("isInDestroyOrder(%q) = false, want true", typ)
		}
	}

	unknownTypes := []string{"custom", "unknown", "", "gateway"}
	for _, typ := range unknownTypes {
		if isInDestroyOrder(typ) {
			t.Errorf("isInDestroyOrder(%q) = true, want false", typ)
		}
	}
}
