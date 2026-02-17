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
	"evaluator",
	"a2a_endpoint",
	"agent_runtime",
	"tool_gateway",
}

// Destroy tears down deployed resources in reverse dependency order,
// streaming progress events via the callback.
func (p *AgentCoreProvider) Destroy(ctx context.Context, req *deploy.DestroyRequest, callback deploy.DestroyCallback) error {
	state, err := parseAdapterState(req.PriorState)
	if err != nil {
		return fmt.Errorf("agentcore: failed to parse prior state: %w", err)
	}

	if len(state.Resources) == 0 {
		_ = callback(&deploy.DestroyEvent{
			Type:    "progress",
			Message: "No resources to destroy",
		})
		_ = callback(&deploy.DestroyEvent{
			Type:    "complete",
			Message: "Destroy complete (nothing to do)",
		})
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

	// Build a lookup of resources by type for ordered deletion.
	byType := make(map[string][]ResourceState)
	for _, r := range state.Resources {
		byType[r.Type] = append(byType[r.Type], r)
	}

	_ = callback(&deploy.DestroyEvent{
		Type:    "progress",
		Message: fmt.Sprintf("Destroying %d resources", len(state.Resources)),
	})

	for step, rtype := range destroyOrder {
		resources, ok := byType[rtype]
		if !ok {
			continue
		}

		_ = callback(&deploy.DestroyEvent{
			Type:    "progress",
			Message: fmt.Sprintf("Step %d: deleting %s resources (%d)", step+1, rtype, len(resources)),
		})

		for _, res := range resources {
			err := destroyer.DeleteResource(ctx, res)
			if err != nil {
				// Emit error event but continue — best-effort teardown.
				_ = callback(&deploy.DestroyEvent{
					Type:    "error",
					Message: fmt.Sprintf("Failed to delete %s %q: %v", res.Type, res.Name, err),
					Resource: &deploy.ResourceResult{
						Type:   res.Type,
						Name:   res.Name,
						Action: deploy.ActionDelete,
						Status: "failed",
						Detail: err.Error(),
					},
				})
				continue
			}

			_ = callback(&deploy.DestroyEvent{
				Type:    "resource",
				Message: fmt.Sprintf("Deleted %s %q", res.Type, res.Name),
				Resource: &deploy.ResourceResult{
					Type:   res.Type,
					Name:   res.Name,
					Action: deploy.ActionDelete,
					Status: "deleted",
				},
			})
		}
	}

	// Handle any resource types not in the standard destroy order.
	for _, res := range state.Resources {
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
				Type:   res.Type,
				Name:   res.Name,
				Action: deploy.ActionDelete,
				Status: status,
			},
		})
	}

	_ = callback(&deploy.DestroyEvent{
		Type:    "complete",
		Message: "Destroy complete",
	})

	return nil
}

// Status returns the current deployment status by checking each resource.
func (p *AgentCoreProvider) Status(ctx context.Context, req *deploy.StatusRequest) (*deploy.StatusResponse, error) {
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
		health, err := checker.CheckResource(ctx, res)
		if err != nil {
			health = "unhealthy"
		}
		if health != "healthy" {
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

	// Re-serialize state so it round-trips.
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
