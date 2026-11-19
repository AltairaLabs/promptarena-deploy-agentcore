package agentcore

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/AltairaLabs/PromptKit/runtime/v2/prompt"
	"github.com/AltairaLabs/promptarena/v2/deploy"
)

// promptSpec is one prompt of a test pack: the tools it may use and the ones
// it blocklists.
type promptSpec struct {
	tools     []string
	blocklist []string
}

// blocklistPack is a single-agent pack with the given prompts, whose tools are
// search, calc and lookup (each with a Lambda in validArenaConfigJSON).
func blocklistPack(id string, prompts map[string]promptSpec) string {
	promptDefs := map[string]any{}
	for name, spec := range prompts {
		def := map[string]any{"id": name, "name": name, "system_template": "You help.", "version": "v1.0.0"}
		if spec.tools != nil {
			def["tools"] = spec.tools
		}
		if spec.blocklist != nil {
			def["tool_policy"] = map[string]any{"blocklist": spec.blocklist}
		}
		promptDefs[name] = def
	}
	tools := map[string]any{}
	for _, name := range []string{"search", "calc", "lookup"} {
		tools[name] = map[string]any{"name": name, "description": name}
	}
	b, _ := json.Marshal(map[string]any{
		"id": id, "version": "v1.0.0", "name": id,
		"prompts": promptDefs, "tools": tools,
		"template_engine": map[string]any{"version": "1.0", "syntax": "handlebars"},
	})
	return string(b)
}

func parseTestPack(t *testing.T, packJSON string) *prompt.Pack {
	t.Helper()
	var pack prompt.Pack
	if err := json.Unmarshal([]byte(packJSON), &pack); err != nil {
		t.Fatalf("parse pack: %v", err)
	}
	return &pack
}

func TestGatewayBlockedTools(t *testing.T) {
	cases := []struct {
		name            string
		prompts         map[string]promptSpec
		wantBlocked     string
		wantRuntimeOnly string
	}{
		{
			name:        "one prompt",
			prompts:     map[string]promptSpec{"chat": {tools: []string{"search", "calc"}, blocklist: []string{"calc"}}},
			wantBlocked: "calc",
		},
		{
			// The issue's case: each prompt blocks a tool the other cannot
			// use, so both must be refused by the one gateway.
			name: "two prompts, both blocklists enforceable",
			prompts: map[string]promptSpec{
				"billing": {tools: []string{"search"}, blocklist: []string{"calc"}},
				"support": {tools: []string{"search"}, blocklist: []string{"lookup"}},
			},
			wantBlocked: "calc,lookup",
		},
		{
			name: "a tool another prompt uses is left to the runtime",
			prompts: map[string]promptSpec{
				"billing": {tools: []string{"search"}, blocklist: []string{"calc"}},
				"support": {tools: []string{"search", "calc"}},
			},
			wantRuntimeOnly: "calc",
		},
		{
			name: "a prompt with no tools list may use any tool",
			prompts: map[string]promptSpec{
				"billing": {tools: []string{"search"}, blocklist: []string{"calc"}},
				"general": {},
			},
			wantRuntimeOnly: "calc",
		},
		{
			name:    "a blocklisted tool the pack does not define is dropped",
			prompts: map[string]promptSpec{"chat": {blocklist: []string{"nonexistent"}}},
		},
		{
			name:    "no blocklist",
			prompts: map[string]promptSpec{"chat": {tools: []string{"search"}}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pack := parseTestPack(t, blocklistPack("bpack", tc.prompts))
			if got := strings.Join(gatewayBlockedTools(pack), ","); got != tc.wantBlocked {
				t.Errorf("blocked = %q, want %q", got, tc.wantBlocked)
			}
			if got := strings.Join(gatewayRuntimeOnlyTools(pack), ","); got != tc.wantRuntimeOnly {
				t.Errorf("runtime-only = %q, want %q", got, tc.wantRuntimeOnly)
			}
		})
	}
	if gatewayBlockedTools(nil) != nil {
		t.Error("a nil pack blocks nothing")
	}
}

func TestPackCedarStatements_OneForbidPerToolAndOnePermit(t *testing.T) {
	pack := parseTestPack(t, blocklistPack("bpack", map[string]promptSpec{
		"billing": {tools: []string{"search"}, blocklist: []string{"calc"}},
		"support": {tools: []string{"search"}, blocklist: []string{"lookup"}},
	}))
	const gw = "arn:aws:bedrock-agentcore:us-east-1:1:gateway/bpack-gw"

	stmts := packCedarStatements(pack, gw)
	if len(stmts) != 3 {
		t.Fatalf("statements = %v, want two forbids and a permit", stmts)
	}
	if !strings.Contains(stmts[0], `"calc___calc"`) || !strings.Contains(stmts[1], `"lookup___lookup"`) {
		t.Errorf("forbids = %v, want calc then lookup", stmts[:2])
	}
	if !strings.HasPrefix(stmts[2], "permit") {
		t.Errorf("last statement = %q, want the permit", stmts[2])
	}

	none := parseTestPack(t, blocklistPack("bpack", map[string]promptSpec{"chat": {}}))
	if got := packCedarStatements(none, gw); got != nil {
		t.Errorf("no blocklist: statements = %v, want none", got)
	}
}

// policyRecordingClient records the policy calls apply makes.
type policyRecordingClient struct {
	simulatedAWSClient
	engines      []string
	associations []string
	policies     []string // "<name>: <statement>"
}

func (c *policyRecordingClient) CreatePolicyEngine(ctx context.Context, name string, cfg *Config) (string, string, error) {
	c.engines = append(c.engines, name)
	return c.simulatedAWSClient.CreatePolicyEngine(ctx, name, cfg)
}

func (c *policyRecordingClient) AssociatePolicyEngine(_ context.Context, arn string, _ *Config) error {
	c.associations = append(c.associations, arn)
	return nil
}

func (c *policyRecordingClient) CreateCedarPolicy(
	ctx context.Context, engineID, name, stmt string, cfg *Config,
) (string, string, error) {
	c.policies = append(c.policies, name+": "+stmt)
	return c.simulatedAWSClient.CreateCedarPolicy(ctx, engineID, name, stmt, cfg)
}

// Two prompts with blocklists used to get an engine each, both associated with
// the pack's one gateway, so the second association replaced the first and
// only the last prompt's blocklist was enforced. Now the pack gets one engine,
// associated once, holding both prompts' forbids.
func TestApply_SeveralBlocklistsShareOnePolicyEngine(t *testing.T) {
	c := &policyRecordingClient{}
	provider := newSimulatedProvider()
	provider.awsClientFunc = func(_ context.Context, cfg *Config) (awsClient, error) {
		c.simulatedAWSClient = *newSimulatedAWSClient(cfg.Region)
		return c, nil
	}
	packJSON := blocklistPack("bpack", map[string]promptSpec{
		"billing": {tools: []string{"search"}, blocklist: []string{"calc"}},
		"support": {tools: []string{"search"}, blocklist: []string{"lookup"}},
	})

	events, stateStr, err := collectEvents(t, provider, applyRequest(t, packJSON, ""))
	if err != nil {
		t.Fatalf("apply: %v", err)
	}

	if strings.Join(c.engines, ",") != "bpack_policy_engine" {
		t.Errorf("engines = %v, want the one pack-scoped engine", c.engines)
	}
	if len(c.associations) != 1 {
		t.Errorf("associations = %v, want exactly one", c.associations)
	}
	joined := strings.Join(c.policies, "\n")
	for _, want := range []string{`"calc___calc"`, `"lookup___lookup"`, "bpack_policy_0:", "permit"} {
		if !strings.Contains(joined, want) {
			t.Errorf("policies %v should contain %q", c.policies, want)
		}
	}
	if !hasResourceEvent(events, ResTypeCedarPolicy, ResStatusCreated) {
		t.Error("expected a created cedar_policy event")
	}

	var engines []ResourceState
	for _, r := range decodeState(t, stateStr).Resources {
		if r.Type == ResTypeCedarPolicy {
			engines = append(engines, r)
		}
	}
	if len(engines) != 1 || engines[0].Name != "bpack_policy_engine" || engines[0].Metadata["policy_count"] != "3" {
		t.Errorf("cedar_policy state = %+v, want one bpack_policy_engine with 3 policies", engines)
	}

	// Plan names the same one resource, so a re-plan changes nothing.
	planReq := applyRequest(t, packJSON, stateStr)
	actions := planActions(t, provider, planReq)
	if got := actions[resourceKey(ResTypeCedarPolicy, "bpack_policy_engine")]; got != deploy.ActionUpdate {
		t.Errorf("re-plan action = %q, want UPDATE (actions %v)", got, actions)
	}
	if dl := keysWithAction(actions, deploy.ActionDelete); len(dl) != 0 {
		t.Errorf("re-plan deletes %v", dl)
	}
}

// A state from an earlier release has an engine per prompt. The pack's one
// engine replaces them: plan creates it and deletes the per-prompt ones.
func TestPlan_PerPromptEnginesAreReplacedByThePackEngine(t *testing.T) {
	prior, _ := json.Marshal(AdapterState{PackID: "bpack", Resources: []ResourceState{
		{Type: ResTypeAgentRuntime, Name: "bpack", ARN: "arn:aws:bedrock-agentcore:us-west-2:1:runtime/bpack",
			Status: ResStatusCreated},
		{Type: ResTypeCedarPolicy, Name: "billing", ARN: "arn:p1", Status: ResStatusCreated,
			Metadata: map[string]string{"policy_engine_id": "pe-billing"}},
		{Type: ResTypeCedarPolicy, Name: "support", ARN: "arn:p2", Status: ResStatusCreated,
			Metadata: map[string]string{"policy_engine_id": "pe-support"}},
	}})
	packJSON := blocklistPack("bpack", map[string]promptSpec{
		"billing": {tools: []string{"search"}, blocklist: []string{"calc"}},
		"support": {tools: []string{"search"}, blocklist: []string{"lookup"}},
	})
	actions := planActions(t, newSimulatedProvider(), applyRequest(t, packJSON, string(prior)))

	if got := actions[resourceKey(ResTypeCedarPolicy, "bpack_policy_engine")]; got != deploy.ActionCreate {
		t.Errorf("pack engine action = %q, want CREATE", got)
	}
	for _, old := range []string{"billing", "support"} {
		if got := actions[resourceKey(ResTypeCedarPolicy, old)]; got != deploy.ActionDelete {
			t.Errorf("per-prompt engine %s action = %q, want DELETE", old, got)
		}
	}
}

// A blocklist only the runtime can enforce creates no engine.
func TestApply_RuntimeOnlyBlocklistCreatesNoEngine(t *testing.T) {
	c := &policyRecordingClient{}
	provider := newSimulatedProvider()
	provider.awsClientFunc = func(_ context.Context, cfg *Config) (awsClient, error) {
		c.simulatedAWSClient = *newSimulatedAWSClient(cfg.Region)
		return c, nil
	}
	packJSON := blocklistPack("bpack", map[string]promptSpec{
		"billing": {tools: []string{"search"}, blocklist: []string{"calc"}},
		"support": {tools: []string{"search", "calc"}},
	})
	if _, _, err := collectEvents(t, provider, applyRequest(t, packJSON, "")); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(c.engines)+len(c.associations)+len(c.policies) != 0 {
		t.Errorf("engines %v, associations %v, policies %v: want none", c.engines, c.associations, c.policies)
	}
}
