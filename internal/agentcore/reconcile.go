package agentcore

import (
	"context"
	"fmt"
	"log"
)

// reconcilePriorState verifies stored state against what the provider actually
// has, and returns the state to plan against plus a description of any drift.
//
// A plan built only from stored state cannot see changes made outside
// promptarena. If a resource was deleted in the console, the stored state still
// lists it, the plan reports no change (or an update), and Apply then fails
// trying to update something that is not there. Verifying first turns that into
// an honest CREATE.
//
// Resolution rules, chosen so verification can only ever improve the plan:
//
//   - missing        -> dropped, and reported as drift
//   - unhealthy      -> kept; it exists, it is just degraded, so it is still
//     a resource to update rather than recreate
//   - check failed   -> kept; a failed lookup is not evidence of absence, and
//     dropping would plan a CREATE that Apply would hit a
//     conflict on
//
// Nothing here is provider-specific: it depends only on resourceChecker, so
// the same logic serves any adapter that can answer "does this still exist?".
func reconcilePriorState(
	ctx context.Context, checker resourceChecker, prior *AdapterState,
) (reconciled *AdapterState, drift []string) {
	if prior == nil || len(prior.Resources) == 0 {
		return prior, nil
	}

	kept := make([]ResourceState, 0, len(prior.Resources))
	for _, res := range prior.Resources {
		status, err := checker.CheckResource(ctx, res)
		if err != nil {
			log.Printf("agentcore: could not verify %s %q (%v) — assuming it still exists",
				res.Type, res.Name, err)
			kept = append(kept, res)
			continue
		}
		if status == StatusMissing {
			drift = append(drift, fmt.Sprintf(
				"%s %q no longer exists and will be recreated", res.Type, res.Name))
			continue
		}
		kept = append(kept, res)
	}

	out := *prior
	out.Resources = kept
	return &out, drift
}

// verifiedPriorState returns the prior state Plan should diff against.
//
// Dry-run is the offline mode — it makes no provider calls at all — and any
// failure to reach the provider falls back to the stored state, so planning
// never becomes dependent on connectivity it did not previously need.
func (p *Provider) verifiedPriorState(
	ctx context.Context, cfg *Config, prior *AdapterState,
) (verified *AdapterState, drift []string) {
	if cfg.DryRun || prior == nil || len(prior.Resources) == 0 {
		return prior, nil
	}

	checker, err := p.checkerFunc(ctx, cfg)
	if err != nil {
		log.Printf("agentcore: could not verify deployed state (%v) — "+
			"planning against stored state, which may be out of date", err)
		return prior, nil
	}
	return reconcilePriorState(ctx, checker, prior)
}
