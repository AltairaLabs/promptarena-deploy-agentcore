package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/AltairaLabs/PromptKit/runtime/deploy"
	"github.com/AltairaLabs/PromptKit/runtime/deploy/adaptersdk"
	"github.com/AltairaLabs/PromptKit/runtime/prompt"
)

// Plan generates a deployment plan for the given pack and config.
func (p *AgentCoreProvider) Plan(_ context.Context, req *deploy.PlanRequest) (*deploy.PlanResponse, error) {
	// 1. Parse the pack.
	pack, err := adaptersdk.ParsePack([]byte(req.PackJSON))
	if err != nil {
		return nil, fmt.Errorf("agentcore: failed to parse pack: %w", err)
	}

	// 2. Parse the deploy config.
	cfg, err := parseConfig(req.DeployConfig)
	if err != nil {
		return nil, fmt.Errorf("agentcore: invalid deploy config: %w", err)
	}
	if errs := cfg.validate(); len(errs) > 0 {
		return nil, fmt.Errorf("agentcore: config validation failed: %s", errs[0])
	}

	// 3. Parse prior state (if any).
	var prior *AdapterState
	if req.PriorState != "" {
		prior = &AdapterState{}
		if err := json.Unmarshal([]byte(req.PriorState), prior); err != nil {
			return nil, fmt.Errorf("agentcore: failed to parse prior state: %w", err)
		}
	}

	// 4. Generate desired resources.
	desired := generateDesiredResources(pack)

	// 5. Diff against prior state.
	changes := diffResources(desired, prior)

	// 6. Build summary.
	summary := buildSummary(changes)

	return &deploy.PlanResponse{
		Changes: changes,
		Summary: summary,
	}, nil
}

// resourceKey returns a unique key for a resource type+name pair.
func resourceKey(typ, name string) string {
	return typ + "/" + name
}

// generateDesiredResources builds the list of desired resources from the pack.
// For multi-agent packs it uses adaptersdk.GenerateAgentResourcePlan as a
// starting point and adds tool_gateway (for pack-level tools) and
// evaluator (for evals). For single-agent packs it generates a
// single agent_runtime resource.
func generateDesiredResources(pack *prompt.Pack) []deploy.ResourceChange {
	var desired []deploy.ResourceChange

	if adaptersdk.IsMultiAgent(pack) {
		// Start with the SDK-generated plan (agent_runtime + a2a_endpoint
		// per member, gateway for entry).
		desired = adaptersdk.GenerateAgentResourcePlan(pack)

		// Add tool_gateway for pack-level tools.
		if len(pack.Tools) > 0 {
			toolNames := make([]string, 0, len(pack.Tools))
			for name := range pack.Tools {
				toolNames = append(toolNames, name)
			}
			sort.Strings(toolNames)
			for _, name := range toolNames {
				desired = append(desired, deploy.ResourceChange{
					Type:   "tool_gateway",
					Name:   name + "_tool_gw",
					Action: deploy.ActionCreate,
					Detail: fmt.Sprintf("Create tool gateway for %s", name),
				})
			}
		}
	} else {
		// Single-agent pack: one agent_runtime.
		name := pack.ID
		if name == "" {
			name = "default"
		}
		desired = append(desired, deploy.ResourceChange{
			Type:   "agent_runtime",
			Name:   name,
			Action: deploy.ActionCreate,
			Detail: fmt.Sprintf("Create AgentCore runtime for %s", name),
		})
	}

	// Evals: add an evaluator for each eval definition.
	if len(pack.Evals) > 0 {
		for _, ev := range pack.Evals {
			desired = append(desired, deploy.ResourceChange{
				Type:   "evaluator",
				Name:   ev.ID + "_eval",
				Action: deploy.ActionCreate,
				Detail: fmt.Sprintf("Create evaluator for %s", ev.ID),
			})
		}
	}

	return desired
}

// diffResources compares desired resources against prior state and assigns
// the correct action (CREATE, UPDATE, DELETE, NO_CHANGE) to each change.
func diffResources(desired []deploy.ResourceChange, prior *AdapterState) []deploy.ResourceChange {
	if prior == nil || len(prior.Resources) == 0 {
		// No prior state — everything is CREATE (already set by generators).
		return desired
	}

	// Build lookup of prior resources.
	priorMap := make(map[string]ResourceState, len(prior.Resources))
	for _, r := range prior.Resources {
		priorMap[resourceKey(r.Type, r.Name)] = r
	}

	// Track which prior resources are still desired.
	seen := make(map[string]bool, len(desired))

	changes := make([]deploy.ResourceChange, 0, len(desired)+len(prior.Resources))
	for _, d := range desired {
		key := resourceKey(d.Type, d.Name)
		seen[key] = true

		if _, exists := priorMap[key]; exists {
			// Resource existed before — mark as UPDATE.
			changes = append(changes, deploy.ResourceChange{
				Type:   d.Type,
				Name:   d.Name,
				Action: deploy.ActionUpdate,
				Detail: fmt.Sprintf("Update %s %s", d.Type, d.Name),
			})
		} else {
			// New resource.
			changes = append(changes, d)
		}
	}

	// Any prior resource not in desired set should be deleted.
	// Collect and sort for deterministic output.
	var toDelete []ResourceState
	for _, r := range prior.Resources {
		if !seen[resourceKey(r.Type, r.Name)] {
			toDelete = append(toDelete, r)
		}
	}
	sort.Slice(toDelete, func(i, j int) bool {
		ki := resourceKey(toDelete[i].Type, toDelete[i].Name)
		kj := resourceKey(toDelete[j].Type, toDelete[j].Name)
		return ki < kj
	})
	for _, r := range toDelete {
		changes = append(changes, deploy.ResourceChange{
			Type:   r.Type,
			Name:   r.Name,
			Action: deploy.ActionDelete,
			Detail: fmt.Sprintf("Delete %s %s", r.Type, r.Name),
		})
	}

	return changes
}

// buildSummary produces a human-readable summary line such as
// "Plan: 3 to create, 1 to update, 0 to delete".
func buildSummary(changes []deploy.ResourceChange) string {
	var create, update, del int
	for _, c := range changes {
		switch c.Action {
		case deploy.ActionCreate:
			create++
		case deploy.ActionUpdate:
			update++
		case deploy.ActionDelete:
			del++
		case deploy.ActionNoChange:
			// counted but not shown
		}
	}
	return fmt.Sprintf("Plan: %d to create, %d to update, %d to delete", create, update, del)
}
