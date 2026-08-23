package agentcore

import (
	"context"
	"log"

	"github.com/AltairaLabs/promptarena/deploy"
	"github.com/AltairaLabs/promptarena/deploy/adaptersdk"
)

// resourceProbe answers the shared drift contract's existence question using
// this adapter's own resource checker.
//
// Only StatusMissing is evidence of absence. An unhealthy resource still
// exists, so it is an update rather than a recreate, and a failed check is
// returned as an error — which the shared reconciler treats as keep.
type resourceProbe struct {
	checker resourceChecker
	// byKey recovers the full ResourceState a check needs from the type and
	// name carried in the ref.
	byKey map[string]ResourceState
}

// Exists reports whether the resource behind ref is still present.
func (p resourceProbe) Exists(
	ctx context.Context, ref adaptersdk.ResourceRef,
) (adaptersdk.Existence, error) {
	res, ok := p.byKey[resourceKey(ref.Type, ref.Name)]
	if !ok {
		// Nothing to check against; keep it rather than guess.
		return adaptersdk.ExistsUnknown, nil
	}

	status, err := p.checker.CheckResource(ctx, res)
	if err != nil {
		// Logged here rather than swallowed: the shared reconciler keeps the
		// resource on error, and an operator reading a plan should still learn
		// that it was not actually verified.
		log.Printf("agentcore: could not verify %s %q (%v) — assuming it still exists",
			res.Type, res.Name, err)
		return adaptersdk.ExistsUnknown, err
	}
	if status == StatusMissing {
		return adaptersdk.ExistsNo, nil
	}
	return adaptersdk.ExistsYes, nil
}

// reconcilePriorState verifies stored state against what the provider actually
// has, and returns the state to plan against plus any drift.
//
// A plan built only from stored state cannot see changes made outside
// promptarena. If a resource was deleted in the console, the stored state still
// lists it, the plan reports no change (or an update), and Apply then fails
// trying to update something that is not there. Verifying first turns that into
// an honest CREATE, with a DRIFT change explaining why.
func reconcilePriorState(
	ctx context.Context, checker resourceChecker, prior *AdapterState,
) (reconciled *AdapterState, drift []deploy.ResourceChange) {
	if prior == nil || len(prior.Resources) == 0 {
		return prior, nil
	}

	byKey := make(map[string]ResourceState, len(prior.Resources))
	refs := make([]adaptersdk.ResourceRef, 0, len(prior.Resources))
	for _, res := range prior.Resources {
		byKey[resourceKey(res.Type, res.Name)] = res
		refs = append(refs, adaptersdk.ResourceRef{
			Type: res.Type,
			Name: res.Name,
			ID:   res.ARN,
		})
	}

	survivors, drift := adaptersdk.ReconcilePriorState(
		ctx, resourceProbe{checker: checker, byKey: byKey}, refs)

	kept := make([]ResourceState, 0, len(survivors))
	for _, ref := range survivors {
		kept = append(kept, byKey[resourceKey(ref.Type, ref.Name)])
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
) (verified *AdapterState, drift []deploy.ResourceChange) {
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
