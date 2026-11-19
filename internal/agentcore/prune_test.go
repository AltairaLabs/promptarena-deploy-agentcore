package agentcore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/AltairaLabs/promptarena/v2/deploy"
)

// sharedGatewayARN is what every tool_gateway entry records with the real
// client: the one parent gateway all the targets live behind.
const sharedGatewayARN = "arn:aws:bedrock-agentcore:us-west-2:123456789012:gateway/shared-gw"

// sharedGatewayClient records the shared gateway's ARN for every tool, as the
// real client does.
type sharedGatewayClient struct {
	simulatedAWSClient
}

func (c *sharedGatewayClient) CreateGatewayTool(context.Context, string, *Config) (string, error) {
	return sharedGatewayARN, nil
}

// recordingDestroyer records what it was asked to delete, and fails the
// resource types listed in failOn.
type recordingDestroyer struct {
	deleted []string // resourceKey of each DeleteResource call
	targets []string // tool name of each DeleteGatewayTarget call
	failOn  map[string]bool
}

func (d *recordingDestroyer) DeleteResource(_ context.Context, res ResourceState) error {
	if d.failOn[res.Type] {
		return fmt.Errorf("simulated delete failure for %s %q", res.Type, res.Name)
	}
	d.deleted = append(d.deleted, resourceKey(res.Type, res.Name))
	return nil
}

func (d *recordingDestroyer) DeleteGatewayTarget(_ context.Context, gatewayARN, toolName string) error {
	if d.failOn[ResTypeToolGateway] {
		return fmt.Errorf("simulated target delete failure for %q", toolName)
	}
	d.targets = append(d.targets, toolName)
	return nil
}

// pruneProvider is a simulated provider whose tools share one gateway and
// whose deletions are recorded by d.
func pruneProvider(d *recordingDestroyer) *Provider {
	sim := newSimulatedProvider()
	return &Provider{
		awsClientFunc: func(_ context.Context, cfg *Config) (awsClient, error) {
			return &sharedGatewayClient{simulatedAWSClient: *newSimulatedAWSClient(cfg.Region)}, nil
		},
		destroyerFunc: func(context.Context, *Config) (resourceDestroyer, error) { return d, nil },
		checkerFunc:   sim.checkerFunc,
	}
}

// evalToolPack is a single-agent pack with the given llm_as_judge evals and
// tools (each tool must have a Lambda in validArenaConfigJSON).
func evalToolPack(evalIDs, tools []string) string {
	toolDefs := map[string]any{}
	for _, name := range tools {
		toolDefs[name] = map[string]any{"name": name, "description": name}
	}
	evalDefs := make([]map[string]any, 0, len(evalIDs))
	for _, id := range evalIDs {
		evalDefs = append(evalDefs, map[string]any{
			"id": id, "type": "llm_as_judge", "trigger": "every_turn",
			"params": map[string]any{"instructions": "judge " + id},
		})
	}
	p := map[string]any{
		"id": "evalpack", "version": "v1.0.0", "name": "Eval Pack",
		"prompts": map[string]any{
			"chat": map[string]any{
				"id": "chat", "name": "Chat", "system_template": "You help.",
				"version": "v1.0.0", "tools": tools,
			},
		},
		"tools":           toolDefs,
		"evals":           evalDefs,
		"template_engine": map[string]any{"version": "1.0", "syntax": "handlebars"},
	}
	b, _ := json.Marshal(p)
	return string(b)
}

func applyRequest(t *testing.T, pack, prior string) *deploy.PlanRequest {
	t.Helper()
	return &deploy.PlanRequest{
		PackJSON: pack, DeployConfig: validConfig(t),
		ArenaConfig: validArenaConfigJSON, PriorState: prior,
	}
}

// mustApply runs a successful apply and returns its state.
func mustApply(t *testing.T, provider *Provider, req *deploy.PlanRequest) ([]deploy.ApplyEvent, string) {
	t.Helper()
	events, state, err := collectEvents(t, provider, req)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	return events, state
}

// planActions returns the plan's action per resource key. Plan checks the
// runtime binary is a Linux executable, which the apply fixtures' dummy file
// is not, so it plans with the plan tests' config instead.
func planActions(t *testing.T, provider *Provider, req *deploy.PlanRequest) map[string]deploy.Action {
	t.Helper()
	planReq := *req
	planReq.DeployConfig = validDeployConfig
	resp, err := provider.Plan(context.Background(), &planReq)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	actions := make(map[string]deploy.Action, len(resp.Changes))
	for _, c := range resp.Changes {
		actions[resourceKey(c.Type, c.Name)] = c.Action
	}
	return actions
}

func keysWithAction(actions map[string]deploy.Action, want deploy.Action) []string {
	var keys []string
	for k, a := range actions {
		if a == want {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys
}

// deleteEvents returns the keys of the resources an apply reported deleted.
func deleteEvents(events []deploy.ApplyEvent) []string {
	var keys []string
	for _, ev := range events {
		r := ev.Resource
		if r != nil && r.Action == deploy.ActionDelete && r.Status == ResStatusDeleted {
			keys = append(keys, resourceKey(r.Type, r.Name))
		}
	}
	sort.Strings(keys)
	return keys
}

// TestApply_DeletesWhatPlanDeletes is the contract: every resource the plan
// marks DELETE is deleted by a successful apply and is gone from its state,
// and nothing the plan keeps is touched.
func TestApply_DeletesWhatPlanDeletes(t *testing.T) {
	d := &recordingDestroyer{}
	provider := pruneProvider(d)

	_, v1 := mustApply(t, provider, applyRequest(t,
		evalToolPack([]string{"quality", "retired"}, []string{"search", "calc"}), ""))

	v2 := applyRequest(t, evalToolPack([]string{"quality"}, []string{"search"}), v1)
	planDeletes := keysWithAction(planActions(t, provider, v2), deploy.ActionDelete)
	wantDeletes := []string{
		resourceKey(ResTypeEvaluator, "retired"),
		resourceKey(ResTypeToolGateway, "calc"),
	}
	if strings.Join(planDeletes, ",") != strings.Join(wantDeletes, ",") {
		t.Fatalf("plan deletes %v, want %v", planDeletes, wantDeletes)
	}

	events, stateStr := mustApply(t, provider, v2)

	if got := deleteEvents(events); strings.Join(got, ",") != strings.Join(planDeletes, ",") {
		t.Errorf("apply reported deleting %v, want what the plan deletes: %v", got, planDeletes)
	}
	// The evaluator is deleted outright. The tool shares its gateway with
	// "search", which the apply kept, so only its target goes.
	if strings.Join(d.deleted, ",") != resourceKey(ResTypeEvaluator, "retired") {
		t.Errorf("DeleteResource calls = %v, want only the retired evaluator", d.deleted)
	}
	if strings.Join(d.targets, ",") != "calc" {
		t.Errorf("DeleteGatewayTarget calls = %v, want calc only", d.targets)
	}

	byKey := stateByKey(decodeState(t, stateStr))
	for _, key := range planDeletes {
		if _, ok := byKey[key]; ok {
			t.Errorf("%s was deleted but is still in the state", key)
		}
	}
	for _, key := range []string{
		resourceKey(ResTypeEvaluator, "quality"),
		resourceKey(ResTypeToolGateway, "search"),
		resourceKey(ResTypeAgentRuntime, "evalpack"),
		resourceKey(ResTypeOnlineEvalConfig, "evalpack_online_eval"),
	} {
		if _, ok := byKey[key]; !ok {
			t.Errorf("%s is still wanted and must stay in the state", key)
		}
	}

	// Once applied there is nothing left to delete.
	if again := keysWithAction(planActions(t, provider, applyRequest(t,
		evalToolPack([]string{"quality"}, []string{"search"}), stateStr)), deploy.ActionDelete); len(again) != 0 {
		t.Errorf("plan after apply still deletes %v", again)
	}
}

// TestApply_DeletesTheWholeGatewayWhenNoToolIsLeft covers dropping every
// tool: no kept resource shares the gateway, so it is deleted outright.
func TestApply_DeletesTheWholeGatewayWhenNoToolIsLeft(t *testing.T) {
	d := &recordingDestroyer{}
	provider := pruneProvider(d)
	_, v1 := mustApply(t, provider, applyRequest(t, evalToolPack(nil, []string{"search"}), ""))

	_, stateStr := mustApply(t, provider, applyRequest(t, evalToolPack(nil, nil), v1))

	if strings.Join(d.deleted, ",") != resourceKey(ResTypeToolGateway, "search") || len(d.targets) != 0 {
		t.Errorf("deleted = %v, targets = %v, want the gateway deleted via search", d.deleted, d.targets)
	}
	if hasResourceType(decodeState(t, stateStr).Resources, ResTypeToolGateway) {
		t.Error("the deleted gateway is still in the state")
	}
}

// TestPlanAndApply_KeepExistingEvaluatorNames is the migration case: a state
// written by an earlier release, whose evaluators apply named after the eval
// id. Plan must update them in place, and apply must neither delete nor
// duplicate them.
func TestPlanAndApply_KeepExistingEvaluatorNames(t *testing.T) {
	const qualityARN = "arn:aws:bedrock:us-west-2:123456789012:evaluator/quality"
	prior, _ := json.Marshal(AdapterState{
		PackID: "evalpack", Version: "v0.9.0",
		Resources: []ResourceState{
			{Type: ResTypeAgentRuntime, Name: "evalpack", ARN: "arn:aws:bedrock-agentcore:us-west-2:1:runtime/evalpack",
				Status: ResStatusCreated},
			{Type: ResTypeEvaluator, Name: "quality", ARN: qualityARN, Status: ResStatusCreated},
			{Type: ResTypeOnlineEvalConfig, Name: "evalpack_online_eval",
				ARN: "arn:aws:bedrock:us-west-2:1:online-evaluation-config/evalpack_online_eval", Status: ResStatusCreated},
		},
	})
	d := &recordingDestroyer{}
	provider := pruneProvider(d)
	req := applyRequest(t, evalToolPack([]string{"quality"}, nil), string(prior))

	actions := planActions(t, provider, req)
	if got := actions[resourceKey(ResTypeEvaluator, "quality")]; got != deploy.ActionUpdate {
		t.Errorf("plan action for evaluator quality = %q, want UPDATE (actions %v)", got, actions)
	}
	if creates := keysWithAction(actions, deploy.ActionCreate); len(creates) != 0 {
		t.Errorf("plan creates %v for an unchanged deployment", creates)
	}
	if deletes := keysWithAction(actions, deploy.ActionDelete); len(deletes) != 0 {
		t.Errorf("plan deletes %v for an unchanged deployment", deletes)
	}

	_, stateStr := mustApply(t, provider, req)
	if len(d.deleted)+len(d.targets) != 0 {
		t.Errorf("apply deleted %v %v from an unchanged deployment", d.deleted, d.targets)
	}
	var evaluators []ResourceState
	for _, r := range decodeState(t, stateStr).Resources {
		if r.Type == ResTypeEvaluator {
			evaluators = append(evaluators, r)
		}
	}
	if len(evaluators) != 1 || evaluators[0].Name != "quality" || evaluators[0].ARN != qualityARN {
		t.Errorf("evaluators in state = %+v, want the one existing quality evaluator", evaluators)
	}
}

// TestApply_RenamedEntryForAKeptResourceIsNotDeleted covers a state entry
// for the same AWS resource the apply just kept under another name: only the
// stale entry goes, the resource itself is left alone.
func TestApply_RenamedEntryForAKeptResourceIsNotDeleted(t *testing.T) {
	prior, _ := json.Marshal(AdapterState{Resources: []ResourceState{
		{Type: ResTypeEvaluator, Name: "quality_eval",
			ARN: "arn:aws:bedrock:us-west-2:123456789012:evaluator/quality", Status: ResStatusCreated},
	}})
	d := &recordingDestroyer{}
	_, stateStr := mustApply(t, pruneProvider(d), applyRequest(t, evalToolPack([]string{"quality"}, nil), string(prior)))

	if len(d.deleted) != 0 {
		t.Errorf("deleted %v: the evaluator is still in use as quality", d.deleted)
	}
	byKey := stateByKey(decodeState(t, stateStr))
	if _, ok := byKey[resourceKey(ResTypeEvaluator, "quality_eval")]; ok {
		t.Error("the stale entry must leave the state")
	}
	if _, ok := byKey[resourceKey(ResTypeEvaluator, "quality")]; !ok {
		t.Error("the kept evaluator must be in the state")
	}
}

// TestApply_DeleteFailureKeepsTheResourceInState: a resource that could not
// be deleted stays recorded, so a later apply or destroy can retry, and the
// others are still deleted.
func TestApply_DeleteFailureKeepsTheResourceInState(t *testing.T) {
	d := &recordingDestroyer{}
	provider := pruneProvider(d)
	_, v1 := mustApply(t, provider, applyRequest(t,
		evalToolPack([]string{"quality", "retired"}, []string{"search", "calc"}), ""))

	d.failOn = map[string]bool{ResTypeEvaluator: true}
	events, stateStr, err := collectEvents(t, provider,
		applyRequest(t, evalToolPack([]string{"quality"}, []string{"search"}), v1))
	if err == nil || !strings.Contains(err.Error(), "retired") {
		t.Fatalf("err = %v, want the failed delete of retired reported", err)
	}
	if countEventsOfType(events, "error") == 0 {
		t.Error("the failed delete must be reported as an error event")
	}
	byKey := stateByKey(decodeState(t, stateStr))
	if r, ok := byKey[resourceKey(ResTypeEvaluator, "retired")]; !ok || r.ARN == "" {
		t.Errorf("retired = %+v, want it kept in the state with its ARN", r)
	}
	if _, ok := byKey[resourceKey(ResTypeToolGateway, "calc")]; ok {
		t.Error("calc was deleted and must leave the state")
	}
	if _, ok := byKey[resourceKey(ResTypeEvaluator, "quality")]; !ok {
		t.Error("the kept evaluator must be in the state")
	}
}

// TestApply_FailedApplyDeletesNothing: until every phase has succeeded,
// "not produced" may mean "not reached", so nothing is deleted.
func TestApply_FailedApplyDeletesNothing(t *testing.T) {
	d := &recordingDestroyer{}
	provider := &Provider{
		awsClientFunc: func(_ context.Context, cfg *Config) (awsClient, error) {
			return &failingAWSClient{
				simulatedAWSClient: *newSimulatedAWSClient(cfg.Region),
				failOn:             map[string]bool{"evaluator": true},
			}, nil
		},
		destroyerFunc: func(context.Context, *Config) (resourceDestroyer, error) { return d, nil },
		checkerFunc:   newSimulatedProvider().checkerFunc,
	}
	prior, _ := json.Marshal(AdapterState{Resources: []ResourceState{
		{Type: ResTypeEvaluator, Name: "retired", ARN: "arn:retired", Status: ResStatusCreated},
	}})
	_, stateStr, err := collectEvents(t, provider, applyRequest(t, evalToolPack([]string{"quality"}, nil), string(prior)))
	if err == nil {
		t.Fatal("expected the evaluator failure to fail the apply")
	}
	if len(d.deleted)+len(d.targets) != 0 {
		t.Errorf("a failed apply deleted %v %v", d.deleted, d.targets)
	}
	if _, ok := stateByKey(decodeState(t, stateStr))[resourceKey(ResTypeEvaluator, "retired")]; !ok {
		t.Error("a failed apply must keep the prior resource in its state")
	}
}

// TestApply_DestroyerUnavailableKeepsRemovedResources: when nothing can
// delete, the removed resources stay recorded and the apply says so.
func TestApply_DestroyerUnavailableKeepsRemovedResources(t *testing.T) {
	for name, factory := range map[string]destroyerFactory{
		"factory error": func(context.Context, *Config) (resourceDestroyer, error) {
			return nil, errors.New("no credentials")
		},
		"no factory": nil,
	} {
		t.Run(name, func(t *testing.T) {
			provider := newSimulatedProvider()
			provider.destroyerFunc = factory
			prior, _ := json.Marshal(AdapterState{Resources: []ResourceState{
				{Type: ResTypeEvaluator, Name: "retired", ARN: "arn:retired", Status: ResStatusCreated},
			}})
			_, stateStr, err := collectEvents(t, provider, applyRequest(t, singleAgentPack(), string(prior)))
			if err == nil {
				t.Fatal("expected an error when removed resources cannot be deleted")
			}
			if _, ok := stateByKey(decodeState(t, stateStr))[resourceKey(ResTypeEvaluator, "retired")]; !ok {
				t.Error("the undeleted resource must stay in the state")
			}
		})
	}
}

// TestApply_PruneCallbackErrorKeepsTheRest: a callback that aborts after one
// deletion leaves the deletions not yet made recorded.
func TestApply_PruneCallbackErrorKeepsTheRest(t *testing.T) {
	d := &recordingDestroyer{}
	prior, _ := json.Marshal(AdapterState{Resources: []ResourceState{
		{Type: ResTypeEvaluator, Name: "a_retired", ARN: "arn:a", Status: ResStatusCreated},
		{Type: ResTypeEvaluator, Name: "b_retired", ARN: "arn:b", Status: ResStatusCreated},
	}})
	abort := errors.New("abort")
	callback := func(ev *deploy.ApplyEvent) error {
		if ev.Resource != nil && ev.Resource.Action == deploy.ActionDelete {
			return abort
		}
		return nil
	}
	stateStr, err := pruneProvider(d).Apply(context.Background(),
		applyRequest(t, singleAgentPack(), string(prior)), callback)
	if !errors.Is(err, abort) {
		t.Fatalf("err = %v, want the callback's error", err)
	}
	byKey := stateByKey(decodeState(t, stateStr))
	if _, ok := byKey[resourceKey(ResTypeEvaluator, "a_retired")]; ok {
		t.Error("a_retired was deleted and must leave the state")
	}
	if _, ok := byKey[resourceKey(ResTypeEvaluator, "b_retired")]; !ok {
		t.Error("b_retired was never deleted and must stay in the state")
	}
}

func TestRemovedResources_SkipsUnrealEntriesAndUsesDestroyOrder(t *testing.T) {
	prior := map[string]ResourceState{}
	for _, r := range []ResourceState{
		{Type: ResTypeMemory, Name: "m", ARN: "arn:m", Status: ResStatusCreated},
		{Type: ResTypeAgentRuntime, Name: "kept", ARN: "arn:kept", Status: ResStatusCreated},
		{Type: ResTypeEvaluator, Name: "e", ARN: "arn:e", Status: ResStatusUpdated},
		{Type: ResTypeOnlineEvalConfig, Name: "o", ARN: "arn:o", Status: ResStatusCreated},
		{Type: "custom", Name: "c", ARN: "arn:c", Status: ResStatusCreated},
		{Type: ResTypeEvaluator, Name: "dry", Status: ResStatusPlanned},
		{Type: ResTypeEvaluator, Name: "broken", Status: ResStatusFailed},
	} {
		prior[resourceKey(r.Type, r.Name)] = r
	}
	kept := []ResourceState{{Type: ResTypeAgentRuntime, Name: "kept", ARN: "arn:kept"}}

	var got []string
	for _, r := range removedResources(prior, kept) {
		got = append(got, r.Name)
	}
	if want := "o,e,m,c"; strings.Join(got, ",") != want {
		t.Errorf("removed = %v, want %s", got, want)
	}
}

func TestDeleteRemovedResource_SharedTargetSpellingIsKept(t *testing.T) {
	d := &recordingDestroyer{}
	kept := []ResourceState{{Type: ResTypeToolGateway, Name: "lookup-order", ARN: sharedGatewayARN}}
	stale := ResourceState{Type: ResTypeToolGateway, Name: "lookup_order", ARN: sharedGatewayARN}
	if err := deleteRemovedResource(context.Background(), d, stale, kept); err != nil {
		t.Fatal(err)
	}
	if len(d.deleted)+len(d.targets) != 0 {
		t.Errorf("deleted %v %v: the kept tool owns that target", d.deleted, d.targets)
	}
}

func TestResourcesWithARN_PlaceholdersMatchNothing(t *testing.T) {
	rs := []ResourceState{{Name: "a", ARN: ""}, {Name: "b", ARN: adoptedPlaceholder}}
	for _, arn := range []string{"", adoptedPlaceholder} {
		if got := resourcesWithARN(rs, arn); len(got) != 0 {
			t.Errorf("resourcesWithARN(%q) = %v, want none", arn, got)
		}
	}
}
