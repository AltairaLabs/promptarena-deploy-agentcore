package agentcore

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/AltairaLabs/PromptKit/runtime/deploy"
)

// destroyOrder defines the reverse dependency order for teardown.
// Resources are grouped by type; each group is destroyed in sequence.
var destroyOrder = []string{
	ResTypeCedarPolicy,
	ResTypeEvaluator,
	ResTypeA2AEndpoint,
	ResTypeAgentRuntime,
	ResTypeToolGateway,
	ResTypeMemory,
}

// Destroy tears down deployed resources in reverse dependency order,
// streaming progress events via the callback.
func (p *Provider) Destroy(
	ctx context.Context, req *deploy.DestroyRequest, callback deploy.DestroyCallback,
) error {
	state, err := parseAdapterState(req.PriorState)
	if err != nil {
		return fmt.Errorf("agentcore: failed to parse prior state: %w", err)
	}

	if len(state.Resources) == 0 {
		emitDestroyEvent(callback, "progress", "No resources to destroy")
		emitDestroyEvent(callback, "complete", "Destroy complete (nothing to do)")
		return nil
	}

	cfg, err := parseConfig(req.DeployConfig)
	if err != nil {
		return fmt.Errorf("agentcore: failed to parse deploy config: %w", err)
	}

	destroyer, err := p.destroyerFunc(ctx, cfg)
	if err != nil {
		return fmt.Errorf("agentcore: failed to create destroyer: %w", err)
	}

	byType := groupByType(state.Resources)

	emitDestroyEvent(callback, "progress",
		fmt.Sprintf("Destroying %d resources", len(state.Resources)))

	for step, rtype := range destroyOrder {
		resources, ok := byType[rtype]
		if !ok {
			continue
		}
		emitDestroyEvent(callback, "progress",
			fmt.Sprintf("Step %d: deleting %s resources (%d)", step+1, rtype, len(resources)))
		destroyResourceGroup(ctx, destroyer, resources, callback)
	}

	destroyUnorderedResources(ctx, destroyer, state.Resources, callback)

	emitDestroyEvent(callback, "complete", "Destroy complete")
	return nil
}

// destroyResourceGroup deletes a slice of resources, emitting events for each.
func destroyResourceGroup(
	ctx context.Context, destroyer resourceDestroyer,
	resources []ResourceState, callback deploy.DestroyCallback,
) {
	for _, res := range resources {
		if err := destroyer.DeleteResource(ctx, res); err != nil {
			deployErr := newDeployError("delete", res.Type, res.Name, err)
			_ = callback(&deploy.DestroyEvent{
				Type:    "error",
				Message: deployErr.Error(),
				Resource: &deploy.ResourceResult{
					Type: res.Type, Name: res.Name,
					Action: deploy.ActionDelete, Status: "failed",
					Detail: deployErr.Error(),
				},
			})
			continue
		}
		_ = callback(&deploy.DestroyEvent{
			Type:    "resource",
			Message: fmt.Sprintf("Deleted %s %q", res.Type, res.Name),
			Resource: &deploy.ResourceResult{
				Type: res.Type, Name: res.Name,
				Action: deploy.ActionDelete, Status: "deleted",
			},
		})
	}
}

// destroyUnorderedResources handles resource types not in the standard
// destroy order.
func destroyUnorderedResources(
	ctx context.Context, destroyer resourceDestroyer,
	resources []ResourceState, callback deploy.DestroyCallback,
) {
	for _, res := range resources {
		if isInDestroyOrder(res.Type) {
			continue
		}
		err := destroyer.DeleteResource(ctx, res)
		status := "deleted"
		if err != nil {
			status = "failed"
		}
		_ = callback(&deploy.DestroyEvent{
			Type:    "resource",
			Message: fmt.Sprintf("Deleted %s %q", res.Type, res.Name),
			Resource: &deploy.ResourceResult{
				Type: res.Type, Name: res.Name,
				Action: deploy.ActionDelete, Status: status,
			},
		})
	}
}

// emitDestroyEvent is a helper to send a simple destroy event.
func emitDestroyEvent(callback deploy.DestroyCallback, eventType, message string) {
	_ = callback(&deploy.DestroyEvent{Type: eventType, Message: message})
}

// groupByType builds a lookup of resources indexed by type.
func groupByType(resources []ResourceState) map[string][]ResourceState {
	byType := make(map[string][]ResourceState)
	for _, r := range resources {
		byType[r.Type] = append(byType[r.Type], r)
	}
	return byType
}

// Status returns the current deployment status by checking each resource.
func (p *Provider) Status(
	ctx context.Context, req *deploy.StatusRequest,
) (*deploy.StatusResponse, error) {
	state, err := parseAdapterState(req.PriorState)
	if err != nil {
		return nil, fmt.Errorf("agentcore: failed to parse prior state: %w", err)
	}

	if len(state.Resources) == 0 {
		return &deploy.StatusResponse{
			Status: "not_deployed",
		}, nil
	}

	cfg, err := parseConfig(req.DeployConfig)
	if err != nil {
		return nil, fmt.Errorf("agentcore: failed to parse deploy config: %w", err)
	}

	checker, err := p.checkerFunc(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("agentcore: failed to create checker: %w", err)
	}

	var resources []deploy.ResourceStatus
	hasUnhealthy := false

	for _, res := range state.Resources {
		health, checkErr := checker.CheckResource(ctx, res)
		if checkErr != nil {
			health = StatusUnhealthy
		}
		if health != StatusHealthy {
			hasUnhealthy = true
		}
		resources = append(resources, deploy.ResourceStatus{
			Type:   res.Type,
			Name:   res.Name,
			Status: health,
		})
	}

	aggregateStatus := "deployed"
	if hasUnhealthy {
		aggregateStatus = "degraded"
	}

	stateJSON, _ := json.Marshal(state)

	return &deploy.StatusResponse{
		Status:    aggregateStatus,
		Resources: resources,
		State:     string(stateJSON),
	}, nil
}

// parseAdapterState deserializes the opaque prior_state JSON.
// An empty string is treated as no state (returns zero-value AdapterState).
func parseAdapterState(raw string) (*AdapterState, error) {
	if raw == "" {
		return &AdapterState{}, nil
	}
	var s AdapterState
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		return nil, fmt.Errorf("invalid state JSON: %w", err)
	}
	return &s, nil
}

// isInDestroyOrder returns true if the resource type appears in the
// standard destroy ordering.
func isInDestroyOrder(rtype string) bool {
	for _, t := range destroyOrder {
		if t == rtype {
			return true
		}
	}
	return false
}
