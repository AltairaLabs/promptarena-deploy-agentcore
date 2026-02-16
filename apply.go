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

// Apply executes a deployment plan, streaming progress events via the callback.
// Resources are created in dependency order:
//  1. Tool Gateway entries (from pack tools)
//  2. Agent runtimes (one per agent member, or single for non-multi-agent)
//  3. A2A wiring between agents
//  4. Evaluators
func (p *AgentCoreProvider) Apply(ctx context.Context, req *deploy.PlanRequest, callback deploy.ApplyCallback) (string, error) {
	// Parse inputs.
	pack, err := adaptersdk.ParsePack([]byte(req.PackJSON))
	if err != nil {
		return "", fmt.Errorf("agentcore: failed to parse pack: %w", err)
	}

	cfg, err := parseConfig(req.DeployConfig)
	if err != nil {
		return "", fmt.Errorf("agentcore: failed to parse deploy config: %w", err)
	}

	reporter := adaptersdk.NewProgressReporter(callback)
	client := p.awsClientFunc(cfg)

	var resources []ResourceState
	var applyErr error

	// Step 1 — Tool Gateway entries.
	toolNames := sortedKeys(pack.Tools)
	for i, name := range toolNames {
		pct := float64(i) / float64(len(toolNames)+1) * 0.25
		if err := reporter.Progress(fmt.Sprintf("Creating tool_gateway: %s", name), pct); err != nil {
			return "", err
		}

		arn, createErr := client.CreateGatewayTool(ctx, name, cfg)
		if createErr != nil {
			_ = reporter.Error(fmt.Errorf("failed to create tool_gateway %s: %w", name, createErr))
			resources = append(resources, ResourceState{
				Type: "tool_gateway", Name: name, Status: "failed",
			})
			applyErr = combineErrors(applyErr, createErr)
			continue
		}

		if err := reporter.Resource(&deploy.ResourceResult{
			Type: "tool_gateway", Name: name, Action: deploy.ActionCreate, Status: "created", Detail: arn,
		}); err != nil {
			return "", err
		}
		resources = append(resources, ResourceState{
			Type: "tool_gateway", Name: name, ARN: arn, Status: "created",
		})
	}

	// Step 2 — Agent runtimes.
	runtimeNames := agentRuntimeNames(pack)
	for i, name := range runtimeNames {
		pct := 0.25 + float64(i)/float64(len(runtimeNames)+1)*0.25
		if err := reporter.Progress(fmt.Sprintf("Creating agentcore_runtime: %s", name), pct); err != nil {
			return "", err
		}

		arn, createErr := client.CreateRuntime(ctx, name, cfg)
		if createErr != nil {
			_ = reporter.Error(fmt.Errorf("failed to create agentcore_runtime %s: %w", name, createErr))
			resources = append(resources, ResourceState{
				Type: "agentcore_runtime", Name: name, Status: "failed",
			})
			applyErr = combineErrors(applyErr, createErr)
			continue
		}

		if err := reporter.Resource(&deploy.ResourceResult{
			Type: "agentcore_runtime", Name: name, Action: deploy.ActionCreate, Status: "created", Detail: arn,
		}); err != nil {
			return "", err
		}
		resources = append(resources, ResourceState{
			Type: "agentcore_runtime", Name: name, ARN: arn, Status: "created",
		})
	}

	// Step 3 — A2A wiring (only for multi-agent packs).
	if adaptersdk.IsMultiAgent(pack) {
		agents := adaptersdk.ExtractAgents(pack)
		for i, ag := range agents {
			wireName := ag.Name + "_a2a"
			pct := 0.50 + float64(i)/float64(len(agents)+1)*0.25
			if err := reporter.Progress(fmt.Sprintf("Creating a2a_wiring: %s", wireName), pct); err != nil {
				return "", err
			}

			arn, createErr := client.CreateA2AWiring(ctx, wireName, cfg)
			if createErr != nil {
				_ = reporter.Error(fmt.Errorf("failed to create a2a_wiring %s: %w", wireName, createErr))
				resources = append(resources, ResourceState{
					Type: "a2a_wiring", Name: wireName, Status: "failed",
				})
				applyErr = combineErrors(applyErr, createErr)
				continue
			}

			if err := reporter.Resource(&deploy.ResourceResult{
				Type: "a2a_wiring", Name: wireName, Action: deploy.ActionCreate, Status: "created", Detail: arn,
			}); err != nil {
				return "", err
			}
			resources = append(resources, ResourceState{
				Type: "a2a_wiring", Name: wireName, ARN: arn, Status: "created",
			})
		}
	}

	// Step 4 — Evaluators.
	if len(pack.Evals) > 0 {
		for i, ev := range pack.Evals {
			evalName := ev.ID
			if evalName == "" {
				evalName = fmt.Sprintf("eval_%d", i)
			}
			pct := 0.75 + float64(i)/float64(len(pack.Evals)+1)*0.25
			if err := reporter.Progress(fmt.Sprintf("Creating evaluator: %s", evalName), pct); err != nil {
				return "", err
			}

			arn, createErr := client.CreateEvaluator(ctx, evalName, cfg)
			if createErr != nil {
				_ = reporter.Error(fmt.Errorf("failed to create evaluator %s: %w", evalName, createErr))
				resources = append(resources, ResourceState{
					Type: "evaluator", Name: evalName, Status: "failed",
				})
				applyErr = combineErrors(applyErr, createErr)
				continue
			}

			if err := reporter.Resource(&deploy.ResourceResult{
				Type: "evaluator", Name: evalName, Action: deploy.ActionCreate, Status: "created", Detail: arn,
			}); err != nil {
				return "", err
			}
			resources = append(resources, ResourceState{
				Type: "evaluator", Name: evalName, ARN: arn, Status: "created",
			})
		}
	}

	// Build state blob using the shared AdapterState type from state.go.
	state := AdapterState{
		Resources: resources,
		PackID:    pack.ID,
		Version:   pack.Version,
	}
	stateJSON, err := json.Marshal(state)
	if err != nil {
		return "", fmt.Errorf("agentcore: failed to marshal state: %w", err)
	}

	return string(stateJSON), applyErr
}

// agentRuntimeNames returns the sorted list of agent runtime names to create.
// For multi-agent packs each member gets its own runtime; for single-agent
// packs the pack ID is used.
func agentRuntimeNames(pack *prompt.Pack) []string {
	if adaptersdk.IsMultiAgent(pack) {
		agents := adaptersdk.ExtractAgents(pack)
		names := make([]string, len(agents))
		for i, a := range agents {
			names[i] = a.Name
		}
		return names // already sorted by ExtractAgents
	}
	return []string{pack.ID}
}

// sortedKeys returns the keys of a map in sorted order.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// combineErrors joins two errors, preferring the first non-nil.
func combineErrors(existing, new error) error {
	if existing == nil {
		return new
	}
	return fmt.Errorf("%w; %v", existing, new)
}
