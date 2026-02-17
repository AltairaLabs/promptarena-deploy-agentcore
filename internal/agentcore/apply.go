// Package agentcore implements the AWS Bedrock AgentCore deploy adapter for PromptKit.
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

// progressStepSize is the fraction of the overall progress bar each of the
// four apply phases occupies (tools, runtimes, a2a, evaluators).
const progressStepSize = 0.25

// Apply phase step indices for progress tracking.
const (
	stepTools      = 0
	stepRuntimes   = 1
	stepA2A        = 2
	stepEvaluators = 3
)

// createFunc is the signature shared by all awsClient Create* methods.
type createFunc func(ctx context.Context, name string, cfg *Config) (string, error)

// applyPhaseResult holds the output of a single apply phase.
type applyPhaseResult struct {
	resources   []ResourceState
	err         error
	callbackErr error // non-nil if the callback itself returned an error
}

// Apply executes a deployment plan, streaming progress events via the callback.
// Resources are created in dependency order:
//  1. Tool Gateway entries (from pack tools)
//  2. Agent runtimes (one per agent member, or single for non-multi-agent)
//  3. A2A wiring between agents
//  4. Evaluators
func (p *Provider) Apply(
	ctx context.Context, req *deploy.PlanRequest, callback deploy.ApplyCallback,
) (string, error) {
	pack, err := adaptersdk.ParsePack([]byte(req.PackJSON))
	if err != nil {
		return "", fmt.Errorf("agentcore: failed to parse pack: %w", err)
	}

	cfg, err := parseConfig(req.DeployConfig)
	if err != nil {
		return "", fmt.Errorf("agentcore: failed to parse deploy config: %w", err)
	}

	reporter := adaptersdk.NewProgressReporter(callback)
	client, err := p.awsClientFunc(ctx, cfg)
	if err != nil {
		return "", fmt.Errorf("agentcore: failed to create AWS client: %w", err)
	}

	var resources []ResourceState
	var applyErr, cbErr error

	// Step 1 — Tool Gateway entries.
	phase := applyPhase(ctx, reporter, client.CreateGatewayTool, cfg,
		sortedKeys(pack.Tools), ResTypeToolGateway, stepTools)
	resources, applyErr, cbErr = mergePhase(resources, applyErr, phase)
	if cbErr != nil {
		return "", cbErr
	}

	// Step 2 — Agent runtimes.
	phase = applyPhase(ctx, reporter, client.CreateRuntime, cfg,
		agentRuntimeNames(pack), ResTypeAgentRuntime, stepRuntimes)
	resources, applyErr, cbErr = mergePhase(resources, applyErr, phase)
	if cbErr != nil {
		return "", cbErr
	}

	// Step 3 — A2A wiring (only for multi-agent packs).
	if adaptersdk.IsMultiAgent(pack) {
		agents := adaptersdk.ExtractAgents(pack)
		wireNames := make([]string, len(agents))
		for i, ag := range agents {
			wireNames[i] = ag.Name + "_a2a"
		}
		phase = applyPhase(ctx, reporter, client.CreateA2AWiring, cfg,
			wireNames, ResTypeA2AEndpoint, stepA2A)
		resources, applyErr, cbErr = mergePhase(resources, applyErr, phase)
		if cbErr != nil {
			return "", cbErr
		}
	}

	// Step 4 — Evaluators.
	evalNames := evalResourceNames(pack)
	if len(evalNames) > 0 {
		phase = applyPhase(ctx, reporter, client.CreateEvaluator, cfg,
			evalNames, ResTypeEvaluator, stepEvaluators)
		resources, applyErr, cbErr = mergePhase(resources, applyErr, phase)
		if cbErr != nil {
			return "", cbErr
		}
	}

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

// applyPhase creates resources of a single type, reporting progress.
// stepIndex (0–3) determines which quarter of the progress bar is used.
func applyPhase(
	ctx context.Context,
	reporter *adaptersdk.ProgressReporter,
	create createFunc,
	cfg *Config,
	names []string,
	resType string,
	stepIndex int,
) applyPhaseResult {
	var result applyPhaseResult
	baseProgress := float64(stepIndex) * progressStepSize

	for i, name := range names {
		pct := baseProgress + float64(i)/float64(len(names)+1)*progressStepSize
		if err := reporter.Progress(fmt.Sprintf("Creating %s: %s", resType, name), pct); err != nil {
			result.callbackErr = err
			return result
		}

		arn, createErr := create(ctx, name, cfg)
		if createErr != nil {
			_ = reporter.Error(fmt.Errorf("failed to create %s %s: %w", resType, name, createErr))
			result.resources = append(result.resources, ResourceState{
				Type: resType, Name: name, Status: "failed",
			})
			result.err = combineErrors(result.err, createErr)
			continue
		}

		if err := reporter.Resource(&deploy.ResourceResult{
			Type: resType, Name: name, Action: deploy.ActionCreate,
			Status: "created", Detail: arn,
		}); err != nil {
			result.callbackErr = err
			return result
		}
		result.resources = append(result.resources, ResourceState{
			Type: resType, Name: name, ARN: arn, Status: "created",
		})
	}
	return result
}

// mergePhase appends phase resources and combines errors into the running totals.
// It returns a non-nil callback error if the phase was aborted by the callback.
func mergePhase(
	resources []ResourceState, applyErr error, phase applyPhaseResult,
) ([]ResourceState, error, error) {
	return append(resources, phase.resources...),
		combineErrors(applyErr, phase.err),
		phase.callbackErr
}

// evalResourceNames returns the list of evaluator names from the pack.
func evalResourceNames(pack *prompt.Pack) []string {
	names := make([]string, 0, len(pack.Evals))
	for i, ev := range pack.Evals {
		name := ev.ID
		if name == "" {
			name = fmt.Sprintf("eval_%d", i)
		}
		names = append(names, name)
	}
	return names
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
func combineErrors(existing, additional error) error {
	if existing == nil {
		return additional
	}
	return fmt.Errorf("%w; %v", existing, additional)
}
