// Package agentcore implements the AWS Bedrock AgentCore deploy adapter for PromptKit.
package agentcore

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/AltairaLabs/PromptKit/runtime/deploy"
	"github.com/AltairaLabs/PromptKit/runtime/deploy/adaptersdk"
	"github.com/AltairaLabs/PromptKit/runtime/prompt"
)

// progressStepSize is the fraction of the overall progress bar each of the
// five apply phases occupies (tools, policies, runtimes, a2a, evaluators).
const progressStepSize = 0.20

// Apply phase step indices for progress tracking.
const (
	stepTools      = 0
	stepPolicies   = 1
	stepRuntimes   = 2
	stepA2A        = 3
	stepEvaluators = 4
)

// progressA2ADiscovery is the progress percentage for the A2A discovery step.
const progressA2ADiscovery = 0.5

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

// applyContext holds parsed inputs for the Apply method.
type applyContext struct {
	pack     *prompt.Pack
	cfg      *Config
	reporter *adaptersdk.ProgressReporter
	client   awsClient
	priorMap map[string]ResourceState
}

// prepareApply parses the request and initializes the apply context.
func (p *Provider) prepareApply(
	ctx context.Context, req *deploy.PlanRequest, callback deploy.ApplyCallback,
) (*applyContext, error) {
	pack, err := adaptersdk.ParsePack([]byte(req.PackJSON))
	if err != nil {
		return nil, fmt.Errorf("agentcore: failed to parse pack: %w", err)
	}

	cfg, err := parseConfig(req.DeployConfig)
	if err != nil {
		return nil, fmt.Errorf("agentcore: failed to parse deploy config: %w", err)
	}

	client, err := p.awsClientFunc(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("agentcore: failed to create AWS client: %w", err)
	}

	cfg.RuntimeEnvVars = buildRuntimeEnvVars(cfg)

	return &applyContext{
		pack:     pack,
		cfg:      cfg,
		reporter: adaptersdk.NewProgressReporter(callback),
		client:   client,
		priorMap: parsePriorState(req.PriorState),
	}, nil
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
	ac, err := p.prepareApply(ctx, req, callback)
	if err != nil {
		return "", err
	}

	resources, applyErr := p.executeApplyPhases(ctx, ac)

	state := AdapterState{
		Resources: resources,
		PackID:    ac.pack.ID,
		Version:   ac.pack.Version,
	}
	stateJSON, err := json.Marshal(state)
	if err != nil {
		return "", fmt.Errorf("agentcore: failed to marshal state: %w", err)
	}

	return string(stateJSON), applyErr
}

// executeApplyPhases runs all deploy phases in dependency order.
func (p *Provider) executeApplyPhases(
	ctx context.Context, ac *applyContext,
) ([]ResourceState, error) {
	var resources []ResourceState
	var applyErr, cbErr error

	// Pre-step — Memory (if configured).
	resources, applyErr = applyMemoryPreStep(ctx, ac, resources, applyErr)

	// Step 1 — Tool Gateway entries (no update support yet).
	phase := applyPhase(ctx, ac.reporter, ac.client.CreateGatewayTool, nil, ac.cfg,
		sortedKeys(ac.pack.Tools), ResTypeToolGateway, stepTools, ac.priorMap)
	resources, applyErr, cbErr = mergePhase(resources, applyErr, phase)
	if cbErr != nil {
		return resources, cbErr
	}

	// Step 2 — Cedar Policies (policy engine + policy per prompt with validators/tool_policy).
	policyRes, policyErr, policyCbErr := applyPoliciesPhase(ctx, ac)
	resources = append(resources, policyRes...)
	applyErr = combineErrors(applyErr, policyErr)
	if policyCbErr != nil {
		return resources, policyCbErr
	}

	// Step 3 — Agent runtimes (supports update).
	phase = applyPhase(ctx, ac.reporter, ac.client.CreateRuntime, ac.client.UpdateRuntime, ac.cfg,
		agentRuntimeNames(ac.pack), ResTypeAgentRuntime, stepRuntimes, ac.priorMap)
	resources, applyErr, cbErr = mergePhase(resources, applyErr, phase)
	if cbErr != nil {
		return resources, cbErr
	}

	// Post-runtime step — A2A endpoint discovery (multi-agent only).
	// Injects PROMPTPACK_AGENTS env var on the entry agent so it knows
	// how to reach other members.
	if adaptersdk.IsMultiAgent(ac.pack) {
		if discoverErr := injectA2AEndpoints(ctx, ac, resources); discoverErr != nil {
			applyErr = combineErrors(applyErr, discoverErr)
		}
	}

	// Step 4 — A2A wiring (only for multi-agent packs, no update support).
	if adaptersdk.IsMultiAgent(ac.pack) {
		resources, applyErr, cbErr = applyA2AWiring(ctx, ac, resources, applyErr)
		if cbErr != nil {
			return resources, cbErr
		}
	}

	// Step 5 — Evaluators (no update support yet).
	evalNames := evalResourceNames(ac.pack)
	if len(evalNames) > 0 {
		phase = applyPhase(ctx, ac.reporter, ac.client.CreateEvaluator, nil, ac.cfg,
			evalNames, ResTypeEvaluator, stepEvaluators, ac.priorMap)
		resources, applyErr, cbErr = mergePhase(resources, applyErr, phase)
		if cbErr != nil {
			return resources, cbErr
		}
	}

	return resources, applyErr
}

// applyMemoryPreStep creates a memory resource if configured.
func applyMemoryPreStep(
	ctx context.Context, ac *applyContext,
	resources []ResourceState, applyErr error,
) ([]ResourceState, error) {
	if ac.cfg.MemoryStore == "" {
		return resources, applyErr
	}
	memRes, memErr := createMemoryResource(ctx, ac.reporter, ac.client, ac.cfg, ac.pack)
	if memErr != nil {
		applyErr = combineErrors(applyErr, memErr)
	}
	if memRes != nil {
		resources = append(resources, *memRes)
	}
	return resources, applyErr
}

// applyPoliciesPhase creates Cedar policy engines and policies for prompts
// that have validators or tool_policy defined. It returns the created
// resources, any apply error, and any callback error.
func applyPoliciesPhase(
	ctx context.Context, ac *applyContext,
) ([]ResourceState, error, error) {
	names := policyResourceNames(ac.pack)
	if len(names) == 0 {
		return nil, nil, nil
	}

	var resources []ResourceState
	var applyErr error
	baseProgress := float64(stepPolicies) * progressStepSize

	for i, promptName := range names {
		pct := baseProgress + float64(i)/float64(len(names)+1)*progressStepSize
		engineName := promptName + "_policy_engine"

		if err := ac.reporter.Progress(
			fmt.Sprintf("Creating %s: %s", ResTypeCedarPolicy, promptName), pct,
		); err != nil {
			return resources, applyErr, err
		}

		res, err := createPolicyForPrompt(ctx, ac, promptName, engineName)
		if err != nil {
			_ = ac.reporter.Error(fmt.Errorf(
				"failed to create cedar_policy for %s: %w", promptName, err,
			))
			resources = append(resources, ResourceState{
				Type: ResTypeCedarPolicy, Name: promptName, Status: "failed",
			})
			applyErr = combineErrors(applyErr, err)
			continue
		}

		if err := ac.reporter.Resource(&deploy.ResourceResult{
			Type: ResTypeCedarPolicy, Name: promptName,
			Action: deploy.ActionCreate, Status: ResStatusCreated,
			Detail: res.ARN,
		}); err != nil {
			return resources, applyErr, err
		}
		resources = append(resources, *res)
	}

	injectPolicyEngineARNs(ac.cfg, resources)

	return resources, applyErr, nil
}

// createPolicyForPrompt creates a policy engine and a Cedar policy for a
// single prompt. Returns the resource state on success.
func createPolicyForPrompt(
	ctx context.Context, ac *applyContext,
	promptName, engineName string,
) (*ResourceState, error) {
	p := ac.pack.Prompts[promptName]
	statement := generateCedarStatement(p.Validators, p.ToolPolicy)
	if statement == "" {
		return nil, fmt.Errorf("no Cedar rules generated for prompt %s", promptName)
	}

	engineARN, engineID, err := ac.client.CreatePolicyEngine(ctx, engineName, ac.cfg)
	if err != nil {
		return nil, fmt.Errorf("policy engine: %w", err)
	}

	policyName := promptName + "_policy"
	policyARN, policyID, err := ac.client.CreateCedarPolicy(
		ctx, engineID, policyName, statement, ac.cfg,
	)
	if err != nil {
		return nil, fmt.Errorf("cedar policy: %w", err)
	}

	return &ResourceState{
		Type:   ResTypeCedarPolicy,
		Name:   promptName,
		ARN:    policyARN,
		Status: ResStatusCreated,
		Metadata: map[string]string{
			"policy_engine_id":  engineID,
			"policy_engine_arn": engineARN,
			"policy_id":         policyID,
		},
	}, nil
}

// injectPolicyEngineARNs adds the PROMPTPACK_POLICY_ENGINE_ARN env var to
// the runtime config so runtimes can reference their policy engines.
func injectPolicyEngineARNs(cfg *Config, resources []ResourceState) {
	var arns []string
	for _, r := range resources {
		if r.Type == ResTypeCedarPolicy && r.Status == ResStatusCreated {
			if arn, ok := r.Metadata["policy_engine_arn"]; ok {
				arns = append(arns, arn)
			}
		}
	}
	if len(arns) > 0 {
		cfg.RuntimeEnvVars[EnvPolicyEngineARN] = strings.Join(arns, ",")
	}
}

// applyA2AWiring runs the A2A wiring phase for multi-agent packs.
func applyA2AWiring(
	ctx context.Context, ac *applyContext,
	resources []ResourceState, applyErr error,
) ([]ResourceState, error, error) {
	agents := adaptersdk.ExtractAgents(ac.pack)
	wireNames := make([]string, len(agents))
	for i, ag := range agents {
		wireNames[i] = ag.Name + "_a2a"
	}
	phase := applyPhase(ctx, ac.reporter, ac.client.CreateA2AWiring, nil, ac.cfg,
		wireNames, ResTypeA2AEndpoint, stepA2A, ac.priorMap)
	return mergePhase(resources, applyErr, phase)
}

// injectA2AEndpoints updates the entry agent runtime with a PROMPTPACK_AGENTS
// env var containing a JSON map of {memberName: runtimeARN}. This allows the
// entry agent to discover and route to other members.
func injectA2AEndpoints(
	ctx context.Context, ac *applyContext, resources []ResourceState,
) error {
	endpointJSON := buildA2AEndpointMap(resources)
	if endpointJSON == "" {
		return nil
	}

	ac.cfg.RuntimeEnvVars[EnvA2AAgents] = endpointJSON

	// Find the entry agent name and its ARN.
	entryName := ac.pack.Agents.Entry
	var entryARN string
	for _, r := range resources {
		if r.Type == ResTypeAgentRuntime && r.Name == entryName && r.ARN != "" {
			entryARN = r.ARN
			break
		}
	}
	if entryARN == "" {
		return nil // entry agent wasn't successfully created; skip
	}

	msg := "Injecting A2A endpoint map on entry agent: " + entryName
	if err := ac.reporter.Progress(msg, progressA2ADiscovery); err != nil {
		return err
	}

	_, err := ac.client.UpdateRuntime(ctx, entryARN, entryName, ac.cfg)
	if err != nil {
		_ = ac.reporter.Error(fmt.Errorf("failed to inject A2A endpoints on %s: %w", entryName, err))
		return err
	}

	return nil
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

// createMemoryResource creates a memory resource if configured, injecting
// the resulting ARN into runtime env vars.
func createMemoryResource(
	ctx context.Context,
	reporter *adaptersdk.ProgressReporter,
	client awsClient,
	cfg *Config,
	pack *prompt.Pack,
) (*ResourceState, error) {
	memName := pack.ID + "_memory"

	if err := reporter.Progress("Creating memory: "+memName, 0); err != nil {
		return nil, err
	}

	arn, err := client.CreateMemory(ctx, memName, cfg)
	if err != nil {
		_ = reporter.Error(fmt.Errorf("failed to create memory %s: %w", memName, err))
		return &ResourceState{
			Type: ResTypeMemory, Name: memName, Status: "failed",
		}, err
	}

	// Inject memory ARN into runtime env vars so runtimes can discover it.
	cfg.RuntimeEnvVars[EnvMemoryID] = arn

	if err := reporter.Resource(&deploy.ResourceResult{
		Type: ResTypeMemory, Name: memName,
		Action: deploy.ActionCreate, Status: "created",
		Detail: arn,
	}); err != nil {
		return nil, err
	}

	return &ResourceState{
		Type: ResTypeMemory, Name: memName, ARN: arn, Status: "created",
	}, nil
}

// combineErrors joins two errors, preferring the first non-nil.
func combineErrors(existing, additional error) error {
	if existing == nil {
		return additional
	}
	return fmt.Errorf("%w; %v", existing, additional)
}
