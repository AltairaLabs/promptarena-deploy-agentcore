package agentcore

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/AltairaLabs/promptarena/v2/deploy"
)

// destroyOrder defines the reverse dependency order for teardown.
// Resources are grouped by type; each group is destroyed in sequence.
var destroyOrder = []string{
	ResTypeOnlineEvalConfig,
	ResTypeToolGateway,
	ResTypeCedarPolicy,
	ResTypeEvaluator,
	ResTypeA2AEndpoint,
	ResTypeRuntimeEndpoint,
	ResTypeAgentRuntime,
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

	// The callback is optional, exactly as Apply's is, and a teardown run
	// without one is normal — it is how the integration suite cleans up and how
	// any non-interactive caller would use Destroy. Every emit below used to
	// dereference it, so such a run panicked partway through, leaving resources
	// behind and reporting a stack trace rather than which ones survived.
	//
	// Normalised once here rather than guarded at each of the four call sites:
	// guarding individually is exactly what let them drift out of step.
	callback = orDiscard(callback)

	byType := groupByType(state.Resources)

	emitDestroyEvent(callback, "progress",
		fmt.Sprintf("Destroying %d resources", len(state.Resources)))

	var failed []string
	for step, rtype := range destroyOrder {
		resources, ok := byType[rtype]
		if !ok {
			continue
		}
		emitDestroyEvent(callback, "progress",
			fmt.Sprintf("Step %d: deleting %s resources (%d)", step+1, rtype, len(resources)))
		if rtype == ResTypeRuntimeEndpoint {
			failed = append(failed, destroyConcurrently(ctx, destroyer, resources, callback)...)
			continue
		}
		failed = append(failed, destroyResourceGroup(ctx, destroyer, resources, callback)...)
	}

	failed = append(failed, destroyUnorderedResources(ctx, destroyer, state.Resources, callback)...)

	// A destroy that left resources behind must not report success: the
	// caller removes the deploy state on success, which would forget
	// resources that still exist (and still cost money). Every deletion is
	// still attempted first, so a re-run only has the survivors left to do.
	if len(failed) > 0 {
		return fmt.Errorf("agentcore: destroy left %d resource(s) undeleted: %s; run destroy again to retry",
			len(failed), strings.Join(failed, ", "))
	}

	emitDestroyEvent(callback, "complete", "Destroy complete")
	return nil
}

// destroyResourceGroup deletes a slice of resources one at a time, emitting
// events for each, and returns the resources it could not delete.
func destroyResourceGroup(
	ctx context.Context, destroyer resourceDestroyer,
	resources []ResourceState, callback deploy.DestroyCallback,
) []string {
	// Every tool_gateway entry records the ARN of the one gateway all the
	// pack's tools share, and deleting an entry deletes that gateway. Once one
	// entry has deleted it, the gateway is DELETING, and AWS rejects a second
	// DeleteGateway on it, so each later entry would report a failure for a
	// gateway that is going away.
	deletedGateways := map[string]bool{}
	var failed []string
	for _, res := range resources {
		// A resource recorded as failed was never created, so there is
		// nothing in AWS to delete. Asking anyway sends its bare name where
		// an AWS-generated ID is expected, which fails ID-format validation
		// and reports a confusing error for a resource that never existed.
		if res.Status == ResStatusFailed {
			continue
		}
		sharedGateway := res.Type == ResTypeToolGateway && res.ARN != ""
		if sharedGateway && deletedGateways[res.ARN] {
			emitDeleted(callback, res)
			continue
		}
		err := destroyer.DeleteResource(ctx, res)
		if !reportDelete(callback, res, err) {
			failed = append(failed, describeResource(res))
			continue
		}
		if sharedGateway {
			deletedGateways[res.ARN] = true
		}
	}
	return failed
}

// destroyConcurrently deletes every resource at once and reports them in
// order once all have finished, returning the ones it could not delete.
//
// Runtime endpoints are deleted this way: each delete waits for its endpoint
// to disappear, which can take minutes on AgentCore, and the endpoints of a
// runtime do not depend on one another, so waiting for them one after
// another only multiplies the wait.
func destroyConcurrently(
	ctx context.Context, destroyer resourceDestroyer,
	resources []ResourceState, callback deploy.DestroyCallback,
) []string {
	errs := make([]error, len(resources))
	var wg sync.WaitGroup
	for i, res := range resources {
		if res.Status == ResStatusFailed {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = destroyer.DeleteResource(ctx, res)
		}()
	}
	wg.Wait()

	var failed []string
	for i, res := range resources {
		if res.Status == ResStatusFailed {
			continue
		}
		if !reportDelete(callback, res, errs[i]) {
			failed = append(failed, describeResource(res))
		}
	}
	return failed
}

// reportDelete emits the outcome of one deletion and reports whether it
// succeeded.
func reportDelete(callback deploy.DestroyCallback, res ResourceState, err error) bool {
	if err == nil {
		emitDeleted(callback, res)
		return true
	}
	deployErr := newDeployError("delete", res.Type, res.Name, err)
	_ = callback(&deploy.DestroyEvent{
		Type:    "error",
		Message: deployErr.Error(),
		Resource: &deploy.ResourceResult{
			Type: res.Type, Name: res.Name,
			Action: deploy.ActionDelete, Status: ResStatusFailed,
			Detail: deployErr.Error(),
		},
	})
	return false
}

// describeResource names a resource for the destroy summary.
func describeResource(res ResourceState) string {
	return fmt.Sprintf("%s %q", res.Type, res.Name)
}

// emitDeleted reports one resource deleted.
func emitDeleted(callback deploy.DestroyCallback, res ResourceState) {
	_ = callback(&deploy.DestroyEvent{
		Type:    ErrCategoryResource,
		Message: fmt.Sprintf("Deleted %s %q", res.Type, res.Name),
		Resource: &deploy.ResourceResult{
			Type: res.Type, Name: res.Name,
			Action: deploy.ActionDelete, Status: ResStatusDeleted,
		},
	})
}

// destroyUnorderedResources handles resource types not in the standard
// destroy order, returning the ones it could not delete.
func destroyUnorderedResources(
	ctx context.Context, destroyer resourceDestroyer,
	resources []ResourceState, callback deploy.DestroyCallback,
) []string {
	var failed []string
	for _, res := range resources {
		if isInDestroyOrder(res.Type) {
			continue
		}
		// Same reasoning as destroyResourceGroup: a failed create left
		// nothing behind to delete.
		if res.Status == ResStatusFailed {
			continue
		}
		if !reportDelete(callback, res, destroyer.DeleteResource(ctx, res)) {
			failed = append(failed, describeResource(res))
		}
	}
	return failed
}

// emitDestroyEvent is a helper to send a simple destroy event.
func emitDestroyEvent(callback deploy.DestroyCallback, eventType, message string) {
	_ = orDiscard(callback)(&deploy.DestroyEvent{Type: eventType, Message: message})
}

// orDiscard returns callback, or one that discards events when it is nil.
func orDiscard(callback deploy.DestroyCallback) deploy.DestroyCallback {
	if callback != nil {
		return callback
	}
	return func(*deploy.DestroyEvent) error { return nil }
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

	for i := range state.Resources {
		res := &state.Resources[i]
		health, checkErr := checker.CheckResource(ctx, *res)
		if checkErr != nil {
			health = StatusUnhealthy
		}
		if health != StatusHealthy {
			hasUnhealthy = true
		}
		detail := detailWithInvokeHint(res.Type, res.ARN, cfg.liveEndpoint())
		if res.Type == ResTypeRuntimeEndpoint {
			detail = endpointStatusDetail(ctx, checker, res)
		}
		resources = append(resources, deploy.ResourceStatus{
			Type:   res.Type,
			Name:   res.Name,
			Status: health,
			Detail: detail,
			Links:  consoleLinks(cfg.Region),
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
