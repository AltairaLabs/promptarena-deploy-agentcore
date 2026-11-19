package agentcore

import (
	"context"
	"fmt"
	"sort"

	"github.com/AltairaLabs/promptarena/v2/deploy"
)

// pruneRemovedResources deletes what the prior state records and this apply
// no longer produced — the resources Plan marks DELETE — and returns the state
// the apply should save.
//
// Without this a successful apply only ever created and updated. A resource
// dropped from the pack stayed in AWS, and fell out of the returned state, so
// destroy could no longer find it either.
//
// It runs only after every phase succeeded, so "not produced" means "no longer
// wanted" rather than "not reached". A resource that fails to delete stays in
// the returned state, with the error, so the next apply or a destroy can
// retry it.
func (p *Provider) pruneRemovedResources(
	ctx context.Context, ac *applyContext, kept []ResourceState,
) ([]ResourceState, error) {
	removed := removedResources(ac.priorMap, kept)
	if len(removed) == 0 {
		return kept, nil
	}
	if p.destroyerFunc == nil {
		return append(kept, removed...), fmt.Errorf("agentcore: no destroyer to delete removed resources")
	}
	destroyer, err := p.destroyerFunc(ctx, ac.cfg)
	if err != nil {
		return append(kept, removed...), fmt.Errorf("agentcore: failed to create destroyer: %w", err)
	}

	var pruneErr error
	for i, res := range removed {
		if err := deleteRemovedResource(ctx, destroyer, res, kept); err != nil {
			deployErr := newDeployError("delete", res.Type, res.Name, err)
			_ = ac.reporter.Error(deployErr)
			pruneErr = combineErrors(pruneErr, deployErr)
			kept = append(kept, res)
			continue
		}
		if err := ac.reporter.Resource(&deploy.ResourceResult{
			Type: res.Type, Name: res.Name,
			Action: deploy.ActionDelete, Status: ResStatusDeleted,
		}); err != nil {
			// The deletion happened; only what follows it is unknown.
			return append(kept, removed[i+1:]...), combineErrors(pruneErr, err)
		}
	}
	return kept, pruneErr
}

// removedResources returns the prior resources missing from kept, in destroy
// order. Entries that never described a real resource — a dry run's planned
// ones, or failed creates — are left out: there is nothing in AWS to delete.
func removedResources(prior map[string]ResourceState, kept []ResourceState) []ResourceState {
	keptKeys := make(map[string]bool, len(kept))
	for _, r := range kept {
		keptKeys[resourceKey(r.Type, r.Name)] = true
	}

	var removed []ResourceState
	for key, r := range prior {
		if keptKeys[key] || r.Status == ResStatusPlanned || r.Status == ResStatusFailed {
			continue
		}
		removed = append(removed, r)
	}
	sort.Slice(removed, func(i, j int) bool {
		oi, oj := destroyRank(removed[i].Type), destroyRank(removed[j].Type)
		if oi != oj {
			return oi < oj
		}
		return resourceKey(removed[i].Type, removed[i].Name) < resourceKey(removed[j].Type, removed[j].Name)
	})
	return removed
}

// destroyRank is a type's position in destroyOrder; unknown types go last, as
// Destroy handles them.
func destroyRank(rtype string) int {
	for i, t := range destroyOrder {
		if t == rtype {
			return i
		}
	}
	return len(destroyOrder)
}

// deleteRemovedResource deletes one removed resource, unless the AWS resource
// behind it is still in use by one the apply kept.
//
// Sharing an ARN happens two ways. Every tool_gateway entry records the one
// shared gateway, so a dropped tool must lose only its target. And a resource
// recorded under an earlier name and now recorded under a new one is the same
// AWS resource; deleting it would take away what the apply just kept, so only
// the stale entry goes.
func deleteRemovedResource(
	ctx context.Context, destroyer resourceDestroyer, res ResourceState, kept []ResourceState,
) error {
	sharers := resourcesWithARN(kept, res.ARN)
	if len(sharers) == 0 {
		return destroyer.DeleteResource(ctx, res)
	}
	if res.Type != ResTypeToolGateway {
		return nil
	}
	target := sanitizeGatewayTargetName(res.Name)
	for _, s := range sharers {
		if s.Type == ResTypeToolGateway && sanitizeGatewayTargetName(s.Name) == target {
			// A kept tool owns this very target under another spelling.
			return nil
		}
	}
	return destroyer.DeleteGatewayTarget(ctx, res.ARN, res.Name)
}

// resourcesWithARN returns the resources whose ARN is arn. An empty arn, or
// the placeholder an adopted Cedar policy records, matches nothing: neither
// identifies one AWS resource.
func resourcesWithARN(resources []ResourceState, arn string) []ResourceState {
	if arn == "" || arn == adoptedPlaceholder {
		return nil
	}
	var out []ResourceState
	for _, r := range resources {
		if r.ARN == arn {
			out = append(out, r)
		}
	}
	return out
}
