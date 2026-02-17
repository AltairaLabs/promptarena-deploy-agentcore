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

// updateFunc is the signature for awsClient Update* methods.
// It takes the prior ARN so the implementation can extract the resource ID.
type updateFunc func(ctx context.Context, arn string, name string, cfg *Config) (string, error)

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

	// Build runtime environment variables from config.
	cfg.RuntimeEnvVars = buildRuntimeEnvVars(cfg)

	// Parse prior state to distinguish create vs update.
	priorMap := parsePriorState(req.PriorState)

	var resources []ResourceState
	var applyErr, cbErr error

	// Step 1 — Tool Gateway entries (no update support yet).
	phase := applyPhase(ctx, reporter, client.CreateGatewayTool, nil, cfg,
		sortedKeys(pack.Tools), ResTypeToolGateway, stepTools, priorMap)
	resources, applyErr, cbErr = mergePhase(resources, applyErr, phase)
	if cbErr != nil {
		return "", cbErr
	}

	// Step 2 — Agent runtimes (supports update).
	phase = applyPhase(ctx, reporter, client.CreateRuntime, client.UpdateRuntime, cfg,
		agentRuntimeNames(pack), ResTypeAgentRuntime, stepRuntimes, priorMap)
	resources, applyErr, cbErr = mergePhase(resources, applyErr, phase)
	if cbErr != nil {
		return "", cbErr
	}

	// Step 3 — A2A wiring (only for multi-agent packs, no update support).
	if adaptersdk.IsMultiAgent(pack) {
		agents := adaptersdk.ExtractAgents(pack)
		wireNames := make([]string, len(agents))
		for i, ag := range agents {
			wireNames[i] = ag.Name + "_a2a"
		}
		phase = applyPhase(ctx, reporter, client.CreateA2AWiring, nil, cfg,
			wireNames, ResTypeA2AEndpoint, stepA2A, priorMap)
		resources, applyErr, cbErr = mergePhase(resources, applyErr, phase)
		if cbErr != nil {
			return "", cbErr
		}
	}

	// Step 4 — Evaluators (no update support yet).
	evalNames := evalResourceNames(pack)
	if len(evalNames) > 0 {
		phase = applyPhase(ctx, reporter, client.CreateEvaluator, nil, cfg,
			evalNames, ResTypeEvaluator, stepEvaluators, priorMap)
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

// applyPhase creates or updates resources of a single type, reporting progress.
// stepIndex (0–3) determines which quarter of the progress bar is used.
// If update is non-nil and the resource exists in priorMap, the update function
// is called instead of create.
func applyPhase(
	ctx context.Context,
	reporter *adaptersdk.ProgressReporter,
	create createFunc,
	update updateFunc,
	cfg *Config,
	names []string,
	resType string,
	stepIndex int,
	priorMap map[string]ResourceState,
) applyPhaseResult {
	var result applyPhaseResult
	baseProgress := float64(stepIndex) * progressStepSize

	for i, name := range names {
		pct := baseProgress + float64(i)/float64(len(names)+1)*progressStepSize
		op := resolveOp(resType, name, update, priorMap)

		if err := reporter.Progress(fmt.Sprintf("%s %s: %s", op.verb, resType, name), pct); err != nil {
			result.callbackErr = err
			return result
		}

		arn, opErr := execOp(ctx, &op, create, update, name, cfg)
		if opErr != nil {
			_ = reporter.Error(fmt.Errorf("failed to %s %s %s: %w", op.failVerb, resType, name, opErr))
			result.resources = append(result.resources, ResourceState{
				Type: resType, Name: name, Status: "failed",
			})
			result.err = combineErrors(result.err, opErr)
			continue
		}

		if err := reporter.Resource(&deploy.ResourceResult{
			Type: resType, Name: name, Action: op.action,
			Status: op.status, Detail: arn,
		}); err != nil {
			result.callbackErr = err
			return result
		}
		result.resources = append(result.resources, ResourceState{
			Type: resType, Name: name, ARN: arn, Status: op.status,
		})
	}
	return result
}

// resourceOp holds the resolved operation details for a single resource.
type resourceOp struct {
	isUpdate bool
	priorARN string
	verb     string // "Creating" or "Updating"
	failVerb string // "create" or "update"
	action   deploy.Action
	status   string
}

// resolveOp determines whether a resource should be created or updated.
func resolveOp(
	resType, name string,
	update updateFunc,
	priorMap map[string]ResourceState,
) resourceOp {
	prior, hasPrior := priorMap[resourceKey(resType, name)]
	if hasPrior && update != nil {
		return resourceOp{
			isUpdate: true, priorARN: prior.ARN,
			verb: "Updating", failVerb: "update",
			action: deploy.ActionUpdate, status: "updated",
		}
	}
	return resourceOp{
		verb: "Creating", failVerb: "create",
		action: deploy.ActionCreate, status: "created",
	}
}

// execOp runs the appropriate create or update function.
func execOp(
	ctx context.Context, op *resourceOp,
	create createFunc, update updateFunc,
	name string, cfg *Config,
) (string, error) {
	if op.isUpdate {
		return update(ctx, op.priorARN, name, cfg)
	}
	return create(ctx, name, cfg)
}

// parsePriorState deserializes the prior state string into a lookup map
// keyed by resourceKey(type, name). Returns an empty map if no prior state.
func parsePriorState(priorState string) map[string]ResourceState {
	priorMap := make(map[string]ResourceState)
	if priorState == "" {
		return priorMap
	}
	var state AdapterState
	if err := json.Unmarshal([]byte(priorState), &state); err != nil {
		return priorMap
	}
	for _, r := range state.Resources {
		priorMap[resourceKey(r.Type, r.Name)] = r
	}
	return priorMap
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
