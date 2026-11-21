// Package agentcore implements the AWS Bedrock AgentCore deploy adapter for PromptKit.
package agentcore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/AltairaLabs/PromptKit/runtime/v2/evals"
	"github.com/AltairaLabs/PromptKit/runtime/v2/prompt"
	"github.com/AltairaLabs/promptarena/v2/deploy"
	"github.com/AltairaLabs/promptarena/v2/deploy/adaptersdk"
)

// numApplyPhases is the total number of apply phases for progress tracking.
const numApplyPhases = 6

// progressStepSize is the fraction of the overall progress bar each of the
// six apply phases occupies (tools, policies, runtimes, a2a, evaluators,
// online_eval_config).
const progressStepSize = 1.0 / numApplyPhases

// Apply phase step indices for progress tracking.
const (
	stepTools         = 0
	stepPolicies      = 1
	stepRuntimes      = 2
	stepA2A           = 3
	stepEvaluators    = 4
	stepOnlineEvalCfg = 5
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
	// preVersions is each prior runtime's latest version before this apply
	// updated it, read only for a candidate deploy.
	preVersions map[string]string
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

	if stageErr := validateStageForPack(cfg, pack); stageErr != nil {
		return nil, fmt.Errorf("agentcore: %w", stageErr)
	}

	cfg.ArenaConfig, err = parseArenaConfig(req.ArenaConfig)
	if err != nil {
		return nil, fmt.Errorf("agentcore: %w", err)
	}
	mergeToolTargets(cfg.ArenaConfig, cfg.ToolTargets)

	client, err := p.awsClientFunc(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("agentcore: failed to create AWS client: %w", err)
	}

	cfg.PackJSON = req.PackJSON
	cfg.PackTools = pack.Tools
	cfg.PromptNames = extractPromptNames(pack)
	cfg.RuntimeEnvVars = buildRuntimeEnvVars(cfg, pack)
	cfg.ResourceTags = buildResourceTags(pack.ID, pack.Version, "", cfg.Tags)
	cfg.EvalDefs = buildEvalDefs(pack)
	injectMetricsConfig(cfg, pack)
	injectDashboardConfig(cfg, pack)

	// Pre-flight eval models before anything is uploaded or created: an
	// unavailable model otherwise fails at evaluator creation, several phases
	// and minutes later, with resources already live to tear down.
	if errs := preflightEvalModels(ctx, client, cfg); len(errs) > 0 {
		return nil, fmt.Errorf("agentcore: eval model not available: %s", strings.Join(errs, "; "))
	}
	if err := preflightEvalLogGroup(ctx, client, cfg, pack); err != nil {
		return nil, fmt.Errorf("agentcore: %w", err)
	}

	// Apply can run without Plan, and the override merge below relies on the
	// names having been checked, so check them here too, before anything exists.
	if errs := validateRuntimeOverride(cfg.RuntimeOverride); len(errs) > 0 {
		return nil, fmt.Errorf("agentcore: invalid runtime_override: %s", strings.Join(errs, "; "))
	}
	if err := deliverRuntime(ctx, client, cfg, req.PackJSON); err != nil {
		return nil, fmt.Errorf("agentcore: %w", err)
	}

	priorMap := parsePriorState(req.PriorState)
	cfg.GatewayName = packGatewayName(pack)
	cfg.PriorGatewayARN = priorGatewayARN(priorMap)

	return &applyContext{
		pack:     pack,
		cfg:      cfg,
		reporter: newReporter(callback),
		client:   client,
		priorMap: priorMap,
	}, nil
}

// deliverRuntime gets the runtime and its pack where AgentCore will read them:
// the code package uploaded to S3 for the adapter's own runtime, or, for a
// runtime_override image, the pack handed over under the deploy SDK's names
// along with the override's environment.
func deliverRuntime(ctx context.Context, client awsClient, cfg *Config, packJSON string) error {
	applyRuntimeOverrideEnv(cfg.RuntimeEnvVars, cfg.RuntimeOverride)
	if cfg.usesContainerImage() {
		return stageOverridePack(ctx, client, cfg, packJSON)
	}
	return uploadCodePackage(ctx, client, cfg, packJSON)
}

// Apply executes a deployment plan, streaming progress events via the callback.
// Resources are created in dependency order:
//  1. Tool Gateway entries (from pack tools)
//  2. Agent runtimes (one per agent member, or single for non-multi-agent)
//  3. A2A wiring between agents
//  4. Evaluators
//
// When every phase succeeds, the resources the prior state records and this
// apply no longer produced are then deleted (see pruneRemovedResources).
//
// When DryRun is enabled in config, Apply emits planned resource events
// without calling any AWS APIs and returns a preview of the deployment.
func (p *Provider) Apply(
	ctx context.Context, req *deploy.PlanRequest, callback deploy.ApplyCallback,
) (string, error) {
	// Check for dry-run mode before full preparation (avoids AWS client creation).
	cfg, err := parseConfig(req.DeployConfig)
	if err != nil {
		return "", fmt.Errorf("agentcore: failed to parse deploy config: %w", err)
	}
	if cfg.DryRun {
		return p.applyDryRun(ctx, req, callback)
	}
	if errs := validateRollout(cfg); len(errs) > 0 {
		return "", fmt.Errorf("agentcore: %s", strings.Join(errs, "; "))
	}
	if cfg.stage() == StagePromote {
		return p.applyPromote(ctx, req, cfg, callback)
	}

	ac, err := p.prepareApply(ctx, req, callback)
	if err != nil {
		return "", err
	}

	resources, applyErr := p.executeApplyPhases(ctx, ac)
	if applyErr != nil {
		// The CLI replaces its stored state with this one, so carry forward the
		// prior resources this apply never reached.
		stateJSON, merr := partialApplyState(req.PriorState, resources, ac.pack.ID, ac.pack.Version)
		if merr != nil {
			return "", errors.Join(applyErr, fmt.Errorf("agentcore: failed to marshal state: %w", merr))
		}
		return stateJSON, applyErr
	}

	// Everything wanted now exists, so what the prior state still records
	// beyond it is what Plan marked for deletion.
	resources, pruneErr := p.pruneRemovedResources(ctx, ac, resources)

	state := AdapterState{
		Resources: resources,
		PackID:    ac.pack.ID,
		Version:   ac.pack.Version,
	}
	stateJSON, err := json.Marshal(state)
	if err != nil {
		return "", errors.Join(pruneErr, fmt.Errorf("agentcore: failed to marshal state: %w", err))
	}

	return string(stateJSON), pruneErr
}

// applyDryRun generates a deployment preview without calling AWS APIs.
// It emits resource events with status "planned" for each resource that
// would be created.
func (p *Provider) applyDryRun(
	_ context.Context, req *deploy.PlanRequest, callback deploy.ApplyCallback,
) (string, error) {
	pack, err := adaptersdk.ParsePack([]byte(req.PackJSON))
	if err != nil {
		return "", fmt.Errorf("agentcore: failed to parse pack: %w", err)
	}

	cfg, err := parseConfig(req.DeployConfig)
	if err != nil {
		return "", fmt.Errorf("agentcore: failed to parse deploy config: %w", err)
	}

	cfg.ArenaConfig, err = parseArenaConfig(req.ArenaConfig)
	if err != nil {
		return "", fmt.Errorf("agentcore: %w", err)
	}
	mergeToolTargets(cfg.ArenaConfig, cfg.ToolTargets)

	cfg.PackJSON = req.PackJSON
	cfg.PackTools = pack.Tools

	reporter := newReporter(callback)
	desired := planEndpoints(agentRuntimeNames(pack), cfg, nil)
	if cfg.stage() != StagePromote {
		desired = append(generateDesiredResources(pack, cfg), desired...)
	}
	annotateRuntimeOverride(desired, cfg)

	resources, cbErr := emitDryRunResources(reporter, desired)
	if cbErr != nil {
		return "", cbErr
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

	return string(stateJSON), nil
}

// emitDryRunResources iterates over desired resources, emitting progress and
// resource events with status "planned". Returns the resource list and any
// callback error that should abort processing.
func emitDryRunResources(
	reporter *adaptersdk.ProgressReporter,
	desired []deploy.ResourceChange,
) ([]ResourceState, error) {
	resources := make([]ResourceState, 0, len(desired))

	for i, d := range desired {
		pct := float64(i) / float64(len(desired)+1)
		msg := fmt.Sprintf("Planned %s: %s", d.Type, d.Name)

		if err := reporter.Progress(msg, pct); err != nil {
			return resources, err
		}

		if err := reporter.Resource(&deploy.ResourceResult{
			Type:   d.Type,
			Name:   d.Name,
			Action: d.Action,
			Status: ResStatusPlanned,
			Detail: d.Detail,
		}); err != nil {
			return resources, err
		}

		resources = append(resources, ResourceState{
			Type:   d.Type,
			Name:   d.Name,
			Status: ResStatusPlanned,
		})
	}

	return resources, nil
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
	phase := applyPhase(ctx, ac, phaseSpec{
		create:    ac.client.CreateGatewayTool,
		names:     gatewayToolNames(ac.pack, ac.cfg),
		resType:   ResTypeToolGateway,
		stepIndex: stepTools,
	})
	resources, applyErr, cbErr = mergePhase(resources, applyErr, phase)
	if cbErr != nil {
		return resources, cbErr
	}

	// Capture gateway ARN for Cedar tool policies that need a specific resource.
	ac.cfg.GatewayARN = findGatewayARN(resources)

	// The gateway's endpoint only exists once it has been created, and the
	// runtime is created after this point, so its environment is completed
	// here rather than up front.
	ac.cfg.GatewayURL, ac.cfg.GatewayAuth = ac.client.GatewayEndpoint()
	injectToolEnvVars(ac.cfg.RuntimeEnvVars, ac.pack, ac.cfg)

	// Step 2 — Cedar policies (the pack's policy engine, when a blocklist can be enforced at the gateway).
	policyRes, policyErr, policyCbErr := applyPoliciesPhase(ctx, ac)
	resources = append(resources, policyRes...)
	applyErr = combineErrors(applyErr, policyErr)
	if policyCbErr != nil {
		return resources, policyCbErr
	}

	// A candidate deploy leaves live on what callers had: read each runtime's
	// version before this apply moves it.
	ac.preVersions = priorRuntimeVersions(ctx, ac)

	// Step 3 — Agent runtimes (supports update).
	phase = applyPhase(ctx, ac, phaseSpec{
		create:    ac.client.CreateRuntime,
		update:    ac.client.UpdateRuntime,
		names:     agentRuntimeNames(ac.pack),
		resType:   ResTypeAgentRuntime,
		stepIndex: stepRuntimes,
	})
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

	// Steps 5–6 — Evaluators and Online Evaluation Config.
	resources, applyErr = applyEvalPhases(ctx, ac, resources, applyErr)

	// Last, and only when everything else succeeded: every update this apply
	// makes to a runtime is done, so each runtime's latest version is final.
	// Point the endpoint this deploy targets at it.
	resources, applyErr, _ = mergeEndpoints(ctx, ac, resources, applyErr)
	return resources, applyErr
}

// mergeEndpoints runs applyEndpointsPhase and folds its error into applyErr.
// After a failure it points nothing: a version an apply failed to finish is
// not one to serve, and leaving each endpoint where it was is what a named
// endpoint is for.
func mergeEndpoints(
	ctx context.Context, ac *applyContext, resources []ResourceState, applyErr error,
) ([]ResourceState, error, error) {
	if applyErr != nil {
		return resources, applyErr, nil
	}
	resources, epErr, cbErr := applyEndpointsPhase(ctx, ac, resources)
	return resources, combineErrors(applyErr, epErr), cbErr
}

// applyEvalPhases deploys evaluators and wires them to traces via an online
// evaluation config. Extracted from executeApplyPhases to reduce cognitive
// complexity.
func applyEvalPhases(
	ctx context.Context, ac *applyContext,
	resources []ResourceState, applyErr error,
) ([]ResourceState, error) {
	// Step 5 — Evaluators (no update support yet).
	ac.cfg.EvalDefs = buildEvalDefs(ac.pack)
	evalNames := evalResourceNames(ac.pack)
	if len(evalNames) > 0 {
		phase := applyPhase(ctx, ac, phaseSpec{
			create:    ac.client.CreateEvaluator,
			names:     evalNames,
			resType:   ResTypeEvaluator,
			stepIndex: stepEvaluators,
		})
		var cbErr error
		resources, applyErr, cbErr = mergePhase(resources, applyErr, phase)
		if cbErr != nil {
			return resources, cbErr
		}
	}

	// Step 6 — Online Evaluation Config (wires evaluators to traces).
	ac.cfg.EvalARNs = collectEvalARNs(resources)
	ac.cfg.BuiltinEvalIDs = collectBuiltinEvalIDs(ac.pack)
	if len(ac.cfg.EvalARNs) > 0 || len(ac.cfg.BuiltinEvalIDs) > 0 {
		oecName := packBaseName(ac.pack) + "_online_eval"
		// Updated rather than re-created when it exists, so it references
		// exactly this apply's evaluators before any removed one is deleted.
		phase := applyPhase(ctx, ac, phaseSpec{
			create:    ac.client.CreateOnlineEvalConfig,
			update:    ac.client.UpdateOnlineEvalConfig,
			names:     []string{oecName},
			resType:   ResTypeOnlineEvalConfig,
			stepIndex: stepOnlineEvalCfg,
		})
		recordOnlineEvalLogGroup(phase.resources, ac.cfg)
		var cbErr error
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
	if !ac.cfg.HasMemory() {
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

// applyPoliciesPhase creates the pack's Cedar policy engine and its policies
// when any prompt's tool blocklist can be enforced at the gateway. It returns
// the created resources, any apply error, and any callback error.
//
// One engine per pack, because a gateway takes one: an engine per prompt
// meant each association replaced the last, and only the last prompt's
// blocklist was enforced (see gatewayBlockedTools).
func applyPoliciesPhase(
	ctx context.Context, ac *applyContext,
) ([]ResourceState, error, error) {
	for _, tool := range gatewayRuntimeOnlyTools(ac.pack) {
		log.Printf("agentcore: tool %q is blocklisted by some prompts but callable by others; "+
			"the shared gateway cannot tell prompts apart, so only the runtime enforces it", tool)
	}
	if len(gatewayBlockedTools(ac.pack)) == 0 {
		return nil, releaseRemovedPolicyEngine(ctx, ac), nil
	}

	engineName := policyEngineName(ac.pack)
	pct := float64(stepPolicies) * progressStepSize
	if err := ac.reporter.Progress(
		fmt.Sprintf("Creating %s: %s", ResTypeCedarPolicy, engineName), pct,
	); err != nil {
		return nil, nil, err
	}

	res, err := createPackPolicy(ctx, ac, engineName)
	if err != nil {
		deployErr := newDeployError("create", ResTypeCedarPolicy, engineName, err)
		_ = ac.reporter.Error(deployErr)
		// res is the engine and the policies made before the failure, when
		// the engine was created; it exists and destroy needs its id.
		if res == nil {
			res = &ResourceState{Type: ResTypeCedarPolicy, Name: engineName, Status: ResStatusFailed}
		}
		return []ResourceState{*res}, deployErr, nil
	}

	if err := ac.reporter.Resource(&deploy.ResourceResult{
		Type: ResTypeCedarPolicy, Name: engineName,
		Action: deploy.ActionCreate, Status: ResStatusCreated,
		Detail: res.ARN,
	}); err != nil {
		return []ResourceState{*res}, nil, err
	}

	resources := []ResourceState{*res}
	injectPolicyEngineARNs(ac.cfg, resources)
	return resources, nil, nil
}

// releaseRemovedPolicyEngine detaches the gateway from the policy engine of a
// pack that no longer has any Cedar policy, so that engine can be deleted
// once the apply succeeds. AWS refuses to delete an engine a gateway still
// enforces. With no gateway, or no engine in the prior state, there is
// nothing to detach.
func releaseRemovedPolicyEngine(ctx context.Context, ac *applyContext) error {
	if gatewayURL, _ := ac.client.GatewayEndpoint(); gatewayURL == "" {
		return nil
	}
	hadEngine := false
	for _, r := range ac.priorMap {
		if r.Type == ResTypeCedarPolicy && r.Metadata["policy_engine_id"] != "" {
			hadEngine = true
			break
		}
	}
	if !hadEngine {
		return nil
	}
	if err := ac.client.DisassociatePolicyEngine(ctx, ac.cfg); err != nil {
		deployErr := newDeployError("update", ResTypeToolGateway, "policy engine", err)
		_ = ac.reporter.Error(deployErr)
		return deployErr
	}
	return nil
}

// createPackPolicy creates the pack's policy engine and one Cedar policy per
// statement. AWS CreatePolicy accepts only a single policy statement, so
// multiple rules are created as separate policies on the same engine. Returns
// the resource state on success. On a failure after the engine exists it
// returns the state of what was made (the engine and the policies created so
// far) together with the error.
func createPackPolicy(
	ctx context.Context, ac *applyContext, engineName string,
) (*ResourceState, error) {
	statements := packCedarStatements(ac.pack, ac.cfg.GatewayARN)
	if len(statements) == 0 {
		return nil, fmt.Errorf("no Cedar rules generated for pack %s", ac.pack.ID)
	}

	engineARN, engineID, err := ac.client.CreatePolicyEngine(ctx, engineName, ac.cfg)
	if err != nil {
		if engineID == "" {
			return nil, fmt.Errorf("policy engine: %w", err)
		}
		return cedarPolicyState(engineName, engineARN, engineID, "", "", 0), fmt.Errorf("policy engine: %w", err)
	}

	// Associate the policy engine with the gateway so the Cedar schema
	// includes the gateway's registered tool actions.
	//
	// Only when a gateway exists. A pack whose tools all run in the runtime
	// creates none, and a tool policy over them is still worth enforcing —
	// failing here would refuse the deploy and strand the engine just
	// created, since a failed resource records no id for destroy to use.
	if gatewayURL, _ := ac.client.GatewayEndpoint(); gatewayURL != "" {
		if err := ac.client.AssociatePolicyEngine(ctx, engineARN, ac.cfg); err != nil {
			return cedarPolicyState(engineName, engineARN, engineID, "", "", 0),
				fmt.Errorf("associate policy engine with gateway: %w", err)
		}
	}

	var lastPolicyARN, lastPolicyID string
	for i, stmt := range statements {
		policyName := packScopedName(ac.pack, fmt.Sprintf("policy_%d", i))
		arn, id, err := ac.client.CreateCedarPolicy(
			ctx, engineID, policyName, stmt, ac.cfg,
		)
		if err != nil {
			return cedarPolicyState(engineName, engineARN, engineID, lastPolicyARN, lastPolicyID, i),
				fmt.Errorf("cedar policy: %w", err)
		}
		lastPolicyARN = arn
		lastPolicyID = id
	}

	return cedarPolicyState(engineName, engineARN, engineID, lastPolicyARN, lastPolicyID, len(statements)), nil
}

// cedarPolicyState is the state of the pack's policy engine and the
// policyCount policies created on it, the last of which is policyARN/policyID.
func cedarPolicyState(
	engineName, engineARN, engineID, policyARN, policyID string, policyCount int,
) *ResourceState {
	return &ResourceState{
		Type:   ResTypeCedarPolicy,
		Name:   engineName,
		ARN:    policyARN,
		Status: ResStatusCreated,
		Metadata: map[string]string{
			"policy_engine_id":  engineID,
			"policy_engine_arn": engineARN,
			"policy_id":         policyID,
			"policy_count":      fmt.Sprintf("%d", policyCount),
		},
	}
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
		wireNames[i] = a2aWiringName(ag.Name)
	}
	phase := applyPhase(ctx, ac, phaseSpec{
		create:    ac.client.CreateA2AWiring,
		names:     wireNames,
		resType:   ResTypeA2AEndpoint,
		stepIndex: stepA2A,
	})
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
		deployErr := newDeployError(
			"update", ResTypeAgentRuntime, entryName,
			fmt.Errorf("A2A endpoint injection: %w", err),
		)
		_ = ac.reporter.Error(deployErr)
		return deployErr
	}

	return nil
}

// applyPhase creates or updates resources of a single type, reporting progress.
// If spec.update is non-nil and the resource exists in the prior state, the
// update function is called instead of create.
// phaseSpec describes one create/update pass over a single resource type.
// Everything the pass needs about the deploy as a whole — the reporter, the
// config and the prior state — comes off the applyContext instead.
type phaseSpec struct {
	create    createFunc
	update    updateFunc // nil when the resource type has no update path
	names     []string
	resType   string
	stepIndex int // 0-3, selecting the quarter of the progress bar to use
}

func applyPhase(ctx context.Context, ac *applyContext, spec phaseSpec) applyPhaseResult {
	var result applyPhaseResult
	reporter, cfg := ac.reporter, ac.cfg
	create, update, names, resType := spec.create, spec.update, spec.names, spec.resType
	baseProgress := float64(spec.stepIndex) * progressStepSize

	for i, name := range names {
		pct := baseProgress + float64(i)/float64(len(names)+1)*progressStepSize
		op := resolveOp(resType, name, update, ac.priorMap)

		if err := reporter.Progress(fmt.Sprintf("%s %s: %s", op.verb, resType, name), pct); err != nil {
			result.callbackErr = err
			return result
		}

		arn, opErr := execOp(ctx, &op, create, update, name, cfg)
		if opErr != nil {
			deployErr := newDeployError(op.failVerb, resType, name, opErr)
			_ = reporter.Error(deployErr)
			result.resources = append(result.resources, failedResource(resType, name, arn, op.isUpdate))
			result.err = combineErrors(result.err, deployErr)
			continue
		}

		if err := reporter.Resource(&deploy.ResourceResult{
			Type: resType, Name: name, Action: op.action,
			Status: op.status,
			Detail: detailWithInvokeHint(resType, arn, cfg.liveEndpoint()),
			Links:  consoleLinks(cfg.Region),
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

// failedResource records a resource whose create or update failed. A create
// that returns an ARN with its error made the resource and then failed after
// it (it never became ready), so it is recorded as created: it exists, and
// destroy needs its ARN to find it.
func failedResource(resType, name, arn string, isUpdate bool) ResourceState {
	if arn != "" && !isUpdate {
		return ResourceState{Type: resType, Name: name, ARN: arn, Status: ResStatusCreated}
	}
	return ResourceState{Type: resType, Name: name, Status: ResStatusFailed}
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
			action: deploy.ActionUpdate, status: ResStatusUpdated,
		}
	}
	return resourceOp{
		verb: "Creating", failVerb: "create",
		action: deploy.ActionCreate, status: ResStatusCreated,
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
// newReporter builds a progress reporter that is safe to call unconditionally.
//
// Apply's callback is optional and the adapter's own integration suite passes
// nil. Handing NewProgressReporter a nil callback yields a reporter that
// panics on first use — mid-apply, after resources exist — so the caller gets
// a stack trace instead of the state saying what needs cleaning up.
//
// A no-op callback rather than a nil reporter, deliberately. Returning nil
// would move the problem rather than fix it: this package calls Progress and
// Resource on the reporter in around a dozen places, and on the SDK version
// pinned here those methods dereference the receiver without a nil check, so a
// nil reporter panics just the same. A live reporter that discards events
// keeps every call site correct on any SDK version.
func newReporter(callback deploy.ApplyCallback) *adaptersdk.ProgressReporter {
	if callback == nil {
		callback = func(*deploy.ApplyEvent) error { return nil }
	}
	return adaptersdk.NewProgressReporter(callback)
}

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

// evalResourceNames returns the list of evaluator resource names from the pack,
// filtered to only llm_as_judge type evals that create AWS resources.
func evalResourceNames(pack *prompt.Pack) []string {
	names := make([]string, 0, len(pack.Evals))
	for i := range pack.Evals {
		if pack.Evals[i].Type != evalTypeLLMAsJudge {
			continue
		}
		names = append(names, evaluatorName(pack, i))
	}
	return names
}

// extractPromptNames returns a set of prompt names from the pack.
func extractPromptNames(pack *prompt.Pack) map[string]bool {
	names := make(map[string]bool, len(pack.Prompts))
	for name := range pack.Prompts {
		names[name] = true
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
	return []string{packBaseName(pack)}
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
	memName := packBaseName(pack) + "_memory"

	if err := reporter.Progress("Creating memory: "+memName, 0); err != nil {
		return nil, err
	}

	arn, err := client.CreateMemory(ctx, memName, cfg)
	if err != nil {
		deployErr := newDeployError("create", ResTypeMemory, memName, err)
		_ = reporter.Error(deployErr)
		res := failedResource(ResTypeMemory, memName, arn, false)
		return &res, deployErr
	}

	// Inject memory ARN into runtime env vars so runtimes can discover it.
	cfg.RuntimeEnvVars[EnvMemoryID] = arn

	if err := reporter.Resource(&deploy.ResourceResult{
		Type: ResTypeMemory, Name: memName,
		Action: deploy.ActionCreate, Status: ResStatusCreated,
		Detail: arn,
	}); err != nil {
		return nil, err
	}

	return &ResourceState{
		Type: ResTypeMemory, Name: memName, ARN: arn, Status: "created",
	}, nil
}

// buildEvalDefs builds the EvalDefs map from pack evals, keyed by
// the same resource name that evalResourceNames generates. Only
// llm_as_judge evals are included.
func buildEvalDefs(pack *prompt.Pack) map[string]evals.EvalDef {
	defs := make(map[string]evals.EvalDef)
	for i := range pack.Evals {
		if pack.Evals[i].Type != evalTypeLLMAsJudge {
			continue
		}
		defs[evaluatorName(pack, i)] = *pack.Evals[i]
	}
	return defs
}

// collectEvalARNs gathers evaluator resource ARNs from the apply results.
// Only resources with status "created" are included.
func collectEvalARNs(resources []ResourceState) map[string]string {
	arns := make(map[string]string)
	for _, r := range resources {
		if r.Type == ResTypeEvaluator && r.Status == ResStatusCreated && r.ARN != "" {
			arns[r.Name] = r.ARN
		}
	}
	return arns
}

// collectBuiltinEvalIDs extracts built-in evaluator IDs from the pack.
// Built-in evaluators (e.g. "Builtin.Helpfulness") don't need CreateEvaluator —
// they're referenced directly by ID in the online eval config.
func collectBuiltinEvalIDs(pack *prompt.Pack) []string {
	var ids []string
	for i := range pack.Evals {
		if pack.Evals[i].Type == evalTypeBuiltin && pack.Evals[i].ID != "" {
			ids = append(ids, pack.Evals[i].ID)
		}
	}
	return ids
}

// findGatewayARN returns the ARN of the first successfully created tool_gateway
// resource. Used to inject a specific resource reference into Cedar tool policies.
func findGatewayARN(resources []ResourceState) string {
	for _, r := range resources {
		if r.Type == ResTypeToolGateway && r.ARN != "" {
			return r.ARN
		}
	}
	return ""
}

// combineErrors joins two errors, preferring the first non-nil.
func combineErrors(existing, additional error) error {
	if existing == nil {
		return additional
	}
	return fmt.Errorf("%w; %v", existing, additional)
}
