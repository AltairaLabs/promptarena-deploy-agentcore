package agentcore

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
func (p *Provider) Plan(_ context.Context, req *deploy.PlanRequest) (*deploy.PlanResponse, error) {
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
	desired := generateDesiredResources(pack, cfg)

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
func generateDesiredResources(pack *prompt.Pack, cfg *Config) []deploy.ResourceChange {
	var desired []deploy.ResourceChange

	// Memory resource (before tools/runtimes).
	if cfg.MemoryStore != "" {
		memName := pack.ID + "_memory"
		desired = append(desired, deploy.ResourceChange{
			Type:   ResTypeMemory,
			Name:   memName,
			Action: deploy.ActionCreate,
			Detail: fmt.Sprintf("Create memory store (%s) for %s", cfg.MemoryStore, pack.ID),
		})
	}

	// Cedar policy resources (per prompt with validators or tool_policy).
	for _, name := range policyResourceNames(pack) {
		desired = append(desired, deploy.ResourceChange{
			Type:   ResTypeCedarPolicy,
			Name:   name,
			Action: deploy.ActionCreate,
			Detail: fmt.Sprintf("Create Cedar policy for prompt %s", name),
		})
	}

	desired = append(desired, generateAgentResources(pack)...)
	desired = append(desired, generateEvalResources(pack)...)
	desired = append(desired, generateOnlineEvalConfigResources(pack)...)

	return desired
}

// generateAgentResources returns agent_runtime (and tool_gateway for multi-agent)
// resource changes for the pack.
func generateAgentResources(pack *prompt.Pack) []deploy.ResourceChange {
	if adaptersdk.IsMultiAgent(pack) {
		return generateMultiAgentResources(pack)
	}

	name := pack.ID
	if name == "" {
		name = "default"
	}
	return []deploy.ResourceChange{{
		Type:   ResTypeAgentRuntime,
		Name:   name,
		Action: deploy.ActionCreate,
		Detail: fmt.Sprintf("Create AgentCore runtime for %s", name),
	}}
}

// generateMultiAgentResources returns SDK-generated resources plus tool_gateways.
func generateMultiAgentResources(pack *prompt.Pack) []deploy.ResourceChange {
	desired := adaptersdk.GenerateAgentResourcePlan(pack)

	if len(pack.Tools) > 0 {
		toolNames := sortedKeys(pack.Tools)
		for _, name := range toolNames {
			desired = append(desired, deploy.ResourceChange{
				Type:   ResTypeToolGateway,
				Name:   name + "_tool_gw",
				Action: deploy.ActionCreate,
				Detail: fmt.Sprintf("Create tool gateway for %s", name),
			})
		}
	}

	return desired
}

// evalTypeLLMAsJudge is the eval type that maps to the SDK's LLM-as-a-Judge
// evaluator. Other eval types are local-only and don't create AWS resources.
const evalTypeLLMAsJudge = "llm_as_judge"

// generateEvalResources returns evaluator resource changes for llm_as_judge evals only.
func generateEvalResources(pack *prompt.Pack) []deploy.ResourceChange {
	var resources []deploy.ResourceChange
	for _, ev := range pack.Evals {
		if ev.Type != evalTypeLLMAsJudge {
			continue
		}
		resources = append(resources, deploy.ResourceChange{
			Type:   ResTypeEvaluator,
			Name:   ev.ID + "_eval",
			Action: deploy.ActionCreate,
			Detail: fmt.Sprintf("Create evaluator for %s", ev.ID),
		})
	}
	return resources
}

// generateOnlineEvalConfigResources returns a single online_eval_config resource
// if the pack has any llm_as_judge evals. The config wires evaluators to agent
// runtime traces via CloudWatch.
func generateOnlineEvalConfigResources(pack *prompt.Pack) []deploy.ResourceChange {
	for _, ev := range pack.Evals {
		if ev.Type == evalTypeLLMAsJudge {
			return []deploy.ResourceChange{{
				Type:   ResTypeOnlineEvalConfig,
				Name:   pack.ID + "_online_eval",
				Action: deploy.ActionCreate,
				Detail: fmt.Sprintf("Create online evaluation config for %s", pack.ID),
			}}
		}
	}
	return nil
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
