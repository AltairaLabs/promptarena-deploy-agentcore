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
	removed = append(removed, supersededGateways(prior, kept, keptKeys)...)
	sort.Slice(removed, func(i, j int) bool {
		oi, oj := destroyRank(removed[i].Type), destroyRank(removed[j].Type)
		if oi != oj {
			return oi < oj
		}
		return resourceKey(removed[i].Type, removed[i].Name) < resourceKey(removed[j].Type, removed[j].Name)
	})
	return removed
}

// previousGatewaySuffix names the state entry for a parent gateway the apply
// moved its tools off.
const previousGatewaySuffix = "_previous_gateway"

// supersededGateways returns an entry for each parent gateway the prior
// state's kept tools were on that no kept resource uses any more.
//
// A kept tool keeps its state key while its ARN moves, so removedResources
// cannot see the gateway it left. That happens when the prior gateway was
// gone or unusable and apply made a new one, or when an older state had tools
// on two gateways and apply gathered them onto one. Each such gateway gets an
// entry under its own name, so it is deleted like any removed resource and,
// if that fails, stays recorded for the next apply or destroy.
func supersededGateways(
	prior map[string]ResourceState, kept []ResourceState, keptKeys map[string]bool,
) []ResourceState {
	// Compared by gateway id rather than whole ARN, so two spellings of one
	// gateway's ARN can never make a live gateway look abandoned.
	inUse := make(map[string]bool, len(kept))
	for _, r := range kept {
		if id := extractResourceID(r.ARN, "gateway"); id != "" {
			inUse[id] = true
		}
	}
	keys := make([]string, 0, len(prior))
	for k := range prior {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var out []ResourceState
	for _, k := range keys {
		r := prior[k]
		id := extractResourceID(r.ARN, "gateway")
		if r.Type != ResTypeToolGateway || !keptKeys[k] || id == "" || inUse[id] {
			continue
		}
		inUse[id] = true // one entry per gateway
		out = append(out, ResourceState{
			Type: ResTypeToolGateway, Name: r.Name + previousGatewaySuffix,
			ARN: r.ARN, Status: r.Status, Metadata: r.Metadata,
		})
	}
	return out
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
