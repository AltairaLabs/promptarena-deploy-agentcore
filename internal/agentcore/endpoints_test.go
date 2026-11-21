package agentcore

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/AltairaLabs/promptarena/v2/deploy"
)

const stagedRuntimeARN = "arn:aws:bedrock-agentcore:us-west-2:123456789012:runtime/mypack"

// stagedProvider runs every apply against one client, so versions and
// endpoints persist from one deploy to the next as they do on the platform.
func stagedProvider(c *simulatedAWSClient, d resourceDestroyer) *Provider {
	if d == nil {
		d = &recordingDestroyer{}
	}
	return &Provider{
		awsClientFunc: func(context.Context, *Config) (awsClient, error) { return c, nil },
		destroyerFunc: func(context.Context, *Config) (resourceDestroyer, error) { return d, nil },
		checkerFunc:   func(context.Context, *Config) (resourceChecker, error) { return &simulatedChecker{}, nil },
	}
}

// withOptions adds deploy options to a config JSON object.
func withOptions(cfg, extra string) string {
	return strings.TrimSuffix(cfg, "}") + "," + extra + "}"
}

func stageRequest(t *testing.T, extra, prior string) *deploy.PlanRequest {
	t.Helper()
	cfg := validConfig(t)
	if extra != "" {
		cfg = withOptions(cfg, extra)
	}
	return &deploy.PlanRequest{
		PackJSON: singleAgentPack(), DeployConfig: cfg,
		ArenaConfig: validArenaConfigJSON, PriorState: prior,
	}
}

func endpointEntry(t *testing.T, stateStr, endpoint string) ResourceState {
	t.Helper()
	r, ok := stateByKey(decodeState(t, stateStr))[resourceKey(ResTypeRuntimeEndpoint, "mypack/"+endpoint)]
	if !ok {
		t.Fatalf("state has no %s endpoint: %s", endpoint, stateStr)
	}
	return r
}

func TestValidateRollout(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		wantErr string
	}{
		{"defaults", Config{}, ""},
		{"each stage", Config{RolloutStage: StageCandidate}, ""},
		{"promote", Config{RolloutStage: StagePromote, LiveEndpoint: "prod", CandidateEndpoint: "canary_1"}, ""},
		{"unknown stage", Config{RolloutStage: "canary"}, "rollout_stage"},
		{"bad name", Config{LiveEndpoint: "1live"}, "live_endpoint"},
		{"long name", Config{CandidateEndpoint: strings.Repeat("a", 49)}, "candidate_endpoint"},
		{"hyphen", Config{CandidateEndpoint: "can-didate"}, "candidate_endpoint"},
		{"reserved", Config{LiveEndpoint: "default"}, "reserved"},
		{"same names", Config{LiveEndpoint: "candidate"}, "must differ"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := strings.Join(validateRollout(&tt.cfg), "; ")
			if tt.wantErr == "" && errs != "" {
				t.Errorf("unexpected errors: %s", errs)
			}
			if tt.wantErr != "" && !strings.Contains(errs, tt.wantErr) {
				t.Errorf("errors %q, want one about %q", errs, tt.wantErr)
			}
		})
	}
}

func TestConfigSchemaDeclaresRolloutOptions(t *testing.T) {
	for _, key := range []string{`"rollout_stage"`, `"live_endpoint"`, `"candidate_endpoint"`} {
		if !strings.Contains(configSchema, key) {
			t.Errorf("config schema does not declare %s", key)
		}
	}
	resp, err := newSimulatedProvider().ValidateConfig(context.Background(), &deploy.ValidateRequest{
		Config: withOptions(validDeployConfig, `"rollout_stage":"later"`),
	})
	if err != nil || resp.Valid {
		t.Errorf("ValidateConfig accepted an unknown rollout_stage: %+v, %v", resp, err)
	}
}

func TestVersionDescription(t *testing.T) {
	tests := []struct{ existing, version, want string }{
		{"", "v1.0.0", "pack_version=v1.0.0"},
		{"support agent", "v1.0.0", "support agent pack_version=v1.0.0"},
		{"support agent pack_version=v0.9.0", "v1.0.0", "support agent pack_version=v1.0.0"},
		{"pack_version=v0.9.0 support agent", "v1.0.0", "support agent pack_version=v1.0.0"},
		{"support agent", "", "support agent"},
		{"", "1.0 beta", "pack_version=1.0_beta"},
	}
	for _, tt := range tests {
		got := versionDescription(tt.existing, tt.version)
		if got != tt.want {
			t.Errorf("versionDescription(%q, %q) = %q, want %q", tt.existing, tt.version, got, tt.want)
		}
		if tt.version != "" && packVersionFromDescription(got) != strings.ReplaceAll(tt.version, " ", "_") {
			t.Errorf("packVersionFromDescription(%q) = %q", got, packVersionFromDescription(got))
		}
	}
	long := versionDescription(strings.Repeat("x", 2000), "v2")
	if len(long) > maxRuntimeDescriptionLen || !strings.HasSuffix(long, " pack_version=v2") {
		t.Errorf("long description kept %d chars, ending %q", len(long), long[len(long)-20:])
	}
	if packVersionFromDescription("no tag here") != "" {
		t.Error("a description without the tag should report no pack version")
	}
}

func TestEvalServiceNameIsTheLiveEndpoint(t *testing.T) {
	cfg := &Config{ResourceTags: map[string]string{TagKeyPackID: "mypack"}, LiveEndpoint: "prod"}
	got := evalServiceNames(cfg)
	want := []string{"mypack.prod"} // AgentCore accepts one service name
	if got := evalServiceNames(&Config{ResourceTags: map[string]string{TagKeyPackID: "p"}}); got[0] != "p.live" {
		t.Errorf("default service name = %v, want p.live", got)
	}
	if !slices.Equal(got, want) {
		t.Errorf("evalServiceNames = %v, want %v", got, want)
	}
}

// A normal deploy creates the live endpoint on the version it made, and the
// next one moves live to the next version.
func TestApply_NormalDeployPointsLive(t *testing.T) {
	c := newSimulatedAWSClient("us-west-2")
	provider := stagedProvider(c, nil)

	events, state1 := mustApply(t, provider, stageRequest(t, "", ""))
	live := endpointEntry(t, state1, "live")
	if live.Metadata[metaVersion] != "1" || live.Status != ResStatusCreated ||
		live.Metadata[metaRuntimeARN] != stagedRuntimeARN || live.ARN == "" {
		t.Errorf("live = %+v, want created on version 1", live)
	}
	if out := resourceOutcomes(events)["mypack/live"]; out.action != deploy.ActionCreate {
		t.Errorf("live endpoint event = %+v, want a create", out)
	}
	rt := stateByKey(decodeState(t, state1))[resourceKey(ResTypeAgentRuntime, "mypack")]
	if rt.Metadata[metaLatestVersion] != "1" || rt.Metadata[metaPackVersion] != "v1.0.0" {
		t.Errorf("runtime metadata = %v, want latest_version 1 and pack_version v1.0.0", rt.Metadata)
	}

	_, state2 := mustApply(t, provider, stageRequest(t, "", state1))
	live = endpointEntry(t, state2, "live")
	if live.Metadata[metaVersion] != "2" || live.Status != ResStatusUpdated {
		t.Errorf("live after second deploy = %+v, want updated to version 2", live)
	}
	if _, ok := stateByKey(decodeState(t, state2))[resourceKey(ResTypeRuntimeEndpoint, "mypack/candidate")]; ok {
		t.Error("a normal deploy created a candidate endpoint")
	}
}

// The staged rollout: candidate gets the new version and live does not move;
// promote moves live to what candidate serves, makes no version, and is
// idempotent; a later normal deploy keeps the candidate endpoint.
func TestApply_CandidateThenPromote(t *testing.T) {
	c := newSimulatedAWSClient("us-west-2")
	d := &recordingDestroyer{}
	provider := stagedProvider(c, d)

	_, state1 := mustApply(t, provider, stageRequest(t, "", ""))
	_, state2 := mustApply(t, provider, stageRequest(t, `"rollout_stage":"candidate"`, state1))

	if v := c.endpoints[stagedRuntimeARN+"|live"]; v != "1" {
		t.Errorf("live serves %q after a candidate deploy, want it left on 1", v)
	}
	if v := c.endpoints[stagedRuntimeARN+"|candidate"]; v != "2" {
		t.Errorf("candidate serves %q, want the new version 2", v)
	}
	if got := endpointEntry(t, state2, "candidate").Metadata[metaVersion]; got != "2" {
		t.Errorf("state records candidate on %q, want 2", got)
	}
	if got := endpointEntry(t, state2, "live").Metadata[metaVersion]; got != "1" {
		t.Errorf("state records live on %q, want 1", got)
	}

	// The platform is the authority: candidate repointed outside this state.
	c.endpoints[stagedRuntimeARN+"|candidate"] = "2"
	versionsBefore := c.versions[stagedRuntimeARN]
	promote := stageRequest(t, `"rollout_stage":"promote"`, state2)
	events, state3 := mustApply(t, provider, promote)
	if c.versions[stagedRuntimeARN] != versionsBefore {
		t.Error("promote made a runtime version")
	}
	if v := c.endpoints[stagedRuntimeARN+"|live"]; v != "2" {
		t.Errorf("live serves %q after promote, want 2", v)
	}
	if got := endpointEntry(t, state3, "live"); got.Metadata[metaVersion] != "2" || got.Status != ResStatusUpdated {
		t.Errorf("state live after promote = %+v", got)
	}
	if out := resourceOutcomes(events)["mypack/live"]; out.action != deploy.ActionUpdate {
		t.Errorf("promote event = %+v, want an update of live", out)
	}
	if types := resourceEventTypes(events); len(types) != 1 {
		t.Errorf("promote touched %v, want only the live endpoint", types)
	}

	// Idempotent, and reported as no change.
	events, state4 := mustApply(t, provider, stageRequest(t, `"rollout_stage":"promote"`, state3))
	if out := resourceOutcomes(events)["mypack/live"]; out.action != deploy.ActionNoChange {
		t.Errorf("second promote event = %+v, want no change", out)
	}
	if v := c.endpoints[stagedRuntimeARN+"|live"]; v != "2" || len(decodeState(t, state4).Resources) !=
		len(decodeState(t, state3).Resources) {
		t.Errorf("second promote changed things: live=%q state=%s", v, state4)
	}

	// A normal deploy afterwards moves live and keeps candidate where it is.
	_, state5 := mustApply(t, provider, stageRequest(t, "", state4))
	if got := endpointEntry(t, state5, "candidate").Metadata[metaVersion]; got != "2" {
		t.Errorf("candidate after normal deploy = %q, want kept on 2", got)
	}
	if got := endpointEntry(t, state5, "live").Metadata[metaVersion]; got != "3" {
		t.Errorf("live after normal deploy = %q, want 3", got)
	}
	if len(d.deleted) != 0 {
		t.Errorf("a deploy deleted %v; endpoints it does not repoint must be kept", d.deleted)
	}
}

func TestApply_PromoteRefusals(t *testing.T) {
	c := newSimulatedAWSClient("us-west-2")
	provider := stagedProvider(c, nil)

	_, _, err := collectEvents(t, provider, stageRequest(t, `"rollout_stage":"promote"`, ""))
	if err == nil || !strings.Contains(err.Error(), "needs a deployed runtime") {
		t.Errorf("promote without a deployment: err = %v", err)
	}

	_, state1 := mustApply(t, provider, stageRequest(t, "", ""))
	_, stateStr, err := collectEvents(t, provider, stageRequest(t, `"rollout_stage":"promote"`, state1))
	if err == nil || !strings.Contains(err.Error(), "no candidate endpoint") {
		t.Errorf("promote with nothing staged: err = %v", err)
	}
	if got := endpointEntry(t, stateStr, "live").Metadata[metaVersion]; got != "1" {
		t.Errorf("a refused promote changed live to %q", got)
	}

	_, _, err = collectEvents(t, provider, stageRequest(t, `"rollout_stage":"sideways"`, state1))
	if err == nil || !strings.Contains(err.Error(), "rollout_stage") {
		t.Errorf("an unknown stage was applied: err = %v", err)
	}
}

// endpointFailingClient fails endpoint calls.
type endpointFailingClient struct {
	simulatedAWSClient
	failPoint, failVersion bool
}

func (c *endpointFailingClient) PointRuntimeEndpoint(ctx context.Context, arn, ep, v string) (string, error) {
	if c.failPoint {
		return "", errors.New("simulated endpoint failure")
	}
	return c.simulatedAWSClient.PointRuntimeEndpoint(ctx, arn, ep, v)
}

func (c *endpointFailingClient) RuntimeVersion(ctx context.Context, arn string) (string, error) {
	if c.failVersion {
		return "", errors.New("simulated version read failure")
	}
	return c.simulatedAWSClient.RuntimeVersion(ctx, arn)
}

func TestApply_EndpointFailureKeepsPriorEndpoint(t *testing.T) {
	for _, tc := range []struct {
		name   string
		client *endpointFailingClient
	}{
		{"point fails", &endpointFailingClient{failPoint: true}},
		{"version read fails", &endpointFailingClient{failVersion: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sim := newSimulatedAWSClient("us-west-2")
			_, state1 := mustApply(t, stagedProvider(sim, nil), stageRequest(t, "", ""))

			tc.client.simulatedAWSClient = *sim
			provider := &Provider{
				awsClientFunc: func(context.Context, *Config) (awsClient, error) { return tc.client, nil },
				destroyerFunc: func(context.Context, *Config) (resourceDestroyer, error) { return &recordingDestroyer{}, nil },
				checkerFunc:   func(context.Context, *Config) (resourceChecker, error) { return &simulatedChecker{}, nil },
			}
			_, stateStr, err := collectEvents(t, provider, stageRequest(t, "", state1))
			if err == nil {
				t.Fatal("expected the endpoint failure to fail the apply")
			}
			if got := endpointEntry(t, stateStr, "live").Metadata[metaVersion]; got != "1" {
				t.Errorf("live recorded on %q after a failed repoint, want the prior 1", got)
			}
		})
	}
}

// A version an apply failed to finish is not pointed at.
func TestApply_EarlierFailurePointsNothing(t *testing.T) {
	c := &failingAWSClient{simulatedAWSClient: *newSimulatedAWSClient("us-west-2"),
		failOn: map[string]bool{"tool_gateway": true}}
	provider := &Provider{
		awsClientFunc: func(context.Context, *Config) (awsClient, error) { return c, nil },
		destroyerFunc: func(context.Context, *Config) (resourceDestroyer, error) { return &recordingDestroyer{}, nil },
		checkerFunc:   func(context.Context, *Config) (resourceChecker, error) { return &simulatedChecker{}, nil },
	}
	req := &deploy.PlanRequest{
		PackJSON: singleAgentPackWithTools(), DeployConfig: validConfig(t), ArenaConfig: validArenaConfigJSON,
	}
	if _, _, err := collectEvents(t, provider, req); err == nil {
		t.Fatal("expected the tool failure to fail the apply")
	}
	if len(c.pointed) != 0 {
		t.Errorf("pointed %v after a failed phase", c.pointed)
	}
}

func TestApply_MultiAgentPointsEachRuntimeAfterA2AInjection(t *testing.T) {
	c := newSimulatedAWSClient("us-west-2")
	req := &deploy.PlanRequest{
		PackJSON: multiAgentPack(), DeployConfig: validConfig(t), ArenaConfig: validArenaConfigJSON,
	}
	_, stateStr := mustApply(t, stagedProvider(c, nil), req)
	byKey := stateByKey(decodeState(t, stateStr))
	// The entry agent is updated again to learn its members, so its live
	// endpoint must be on that second version.
	if v := byKey[resourceKey(ResTypeRuntimeEndpoint, "coordinator/live")].Metadata[metaVersion]; v != "2" {
		t.Errorf("coordinator live on %q, want 2 (after the A2A update)", v)
	}
	if v := byKey[resourceKey(ResTypeRuntimeEndpoint, "worker/live")].Metadata[metaVersion]; v != "1" {
		t.Errorf("worker live on %q, want 1", v)
	}
}

func TestApply_CustomEndpointNames(t *testing.T) {
	c := newSimulatedAWSClient("us-west-2")
	opts := `"live_endpoint":"prod","candidate_endpoint":"next","rollout_stage":"candidate"`
	_, stateStr := mustApply(t, stagedProvider(c, nil), stageRequest(t, opts, ""))
	if got := endpointEntry(t, stateStr, "next").Metadata[metaVersion]; got != "1" {
		t.Errorf("next on %q, want 1", got)
	}
	if _, ok := c.endpoints[stagedRuntimeARN+"|prod"]; ok {
		t.Error("a candidate deploy created the live endpoint")
	}
}

// A carried endpoint the platform no longer has is dropped from state.
func TestApply_CarriedEndpointGoneIsDropped(t *testing.T) {
	c := newSimulatedAWSClient("us-west-2")
	provider := stagedProvider(c, nil)
	_, state1 := mustApply(t, provider, stageRequest(t, "", ""))
	_, state2 := mustApply(t, provider, stageRequest(t, `"rollout_stage":"candidate"`, state1))
	delete(c.endpoints, stagedRuntimeARN+"|live")
	_, state3 := mustApply(t, provider, stageRequest(t, `"rollout_stage":"candidate"`, state2))
	if _, ok := stateByKey(decodeState(t, state3))[resourceKey(ResTypeRuntimeEndpoint, "mypack/live")]; ok {
		t.Error("state still records a live endpoint the platform no longer has")
	}
}

func TestApply_DryRunPlansEndpoints(t *testing.T) {
	cfg := `{"region":"us-west-2","runtime_role_arn":"arn:aws:iam::123456789012:role/test",` +
		`"runtime_binary_path":"/nonexistent","dry_run":true`
	for _, tc := range []struct {
		stage string
		want  []string
	}{
		{"", []string{ResTypeAgentRuntime, ResTypeRuntimeEndpoint}},
		{`,"rollout_stage":"promote"`, []string{ResTypeRuntimeEndpoint}},
	} {
		events, _, err := collectEvents(t, newSimulatedProvider(), &deploy.PlanRequest{
			PackJSON: singleAgentPack(), DeployConfig: cfg + tc.stage + "}", ArenaConfig: validArenaConfigJSON,
		})
		if err != nil {
			t.Fatalf("dry run: %v", err)
		}
		if got := resourceEventTypes(events); !slices.Equal(got, tc.want) {
			t.Errorf("dry run %q planned %v, want %v", tc.stage, got, tc.want)
		}
	}
}

func planWith(t *testing.T, provider *Provider, extra, prior string) *deploy.PlanResponse {
	t.Helper()
	cfg := validDeployConfig
	if extra != "" {
		cfg = withOptions(cfg, extra)
	}
	resp, err := provider.Plan(context.Background(), &deploy.PlanRequest{
		PackJSON: singleAgentPack(), DeployConfig: cfg, ArenaConfig: validArenaConfigJSON, PriorState: prior,
	})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	return resp
}

func changeFor(t *testing.T, resp *deploy.PlanResponse, name string) deploy.ResourceChange {
	t.Helper()
	for _, c := range resp.Changes {
		if c.Type == ResTypeRuntimeEndpoint && c.Name == name {
			return c
		}
	}
	t.Fatalf("plan has no change for endpoint %s: %+v", name, resp.Changes)
	return deploy.ResourceChange{}
}

func TestPlan_ShowsLiveAndCandidateVersions(t *testing.T) {
	c := newSimulatedAWSClient("us-west-2")
	provider := stagedProvider(c, nil)
	_, state1 := mustApply(t, provider, stageRequest(t, "", ""))

	resp := planWith(t, provider, `"rollout_stage":"candidate"`, state1)
	cand := changeFor(t, resp, "mypack/candidate")
	if cand.Action != deploy.ActionCreate || !strings.Contains(cand.Detail, "live does not change") {
		t.Errorf("candidate change = %+v", cand)
	}
	live := changeFor(t, resp, "mypack/live")
	if live.Action != deploy.ActionNoChange || !strings.Contains(live.Detail, "stays on version 1") {
		t.Errorf("live change = %+v", live)
	}

	_, state2 := mustApply(t, provider, stageRequest(t, `"rollout_stage":"candidate"`, state1))
	resp = planWith(t, provider, `"rollout_stage":"promote"`, state2)
	if len(resp.Changes) != 2 {
		t.Errorf("promote plan = %+v, want only the endpoints", resp.Changes)
	}
	live = changeFor(t, resp, "mypack/live")
	if live.Action != deploy.ActionUpdate || !strings.Contains(live.Detail, "version 2") ||
		!strings.Contains(live.Detail, "live now serves version 1") {
		t.Errorf("promote live change = %+v", live)
	}
	if resp.Summary != "Plan: 0 to create, 1 to update, 0 to delete" {
		t.Errorf("promote summary = %q", resp.Summary)
	}

	resp = planWith(t, provider, "", state2)
	if got := changeFor(t, resp, "mypack/live"); got.Action != deploy.ActionUpdate ||
		!strings.Contains(got.Detail, "now version 1") {
		t.Errorf("normal plan live change = %+v", got)
	}
	if got := changeFor(t, resp, "mypack/candidate"); got.Action != deploy.ActionNoChange {
		t.Errorf("normal plan candidate change = %+v, want no change", got)
	}
}

func TestPlanEndpoints_DeletesEndpointsOfRemovedRuntimes(t *testing.T) {
	prior := &AdapterState{Resources: []ResourceState{
		endpointState("gone", "arn:aws:bedrock-agentcore:us-west-2:1:runtime/gone", "live", "x", "4", ResStatusCreated),
	}}
	changes := planEndpoints([]string{"kept"}, &Config{}, prior)
	if len(changes) != 2 || changes[1].Name != "gone/live" || changes[1].Action != deploy.ActionDelete {
		t.Errorf("changes = %+v, want gone/live deleted", changes)
	}
	cfg := &Config{RolloutStage: StagePromote}
	if got := planEndpoints([]string{"kept"}, cfg, nil); !strings.Contains(got[0].Detail, "version ?") {
		t.Errorf("promote plan with nothing recorded = %+v", got)
	}
}

// Endpoints are deleted before the runtime they belong to, both by destroy
// and by the prune of a runtime the pack dropped.
func TestEndpointsAreDeletedBeforeTheirRuntime(t *testing.T) {
	if destroyRank(ResTypeRuntimeEndpoint) >= destroyRank(ResTypeAgentRuntime) {
		t.Fatal("runtime_endpoint must come before agent_runtime in destroyOrder")
	}
	var log []string
	d := &orderingDestroyer{log: &log}
	provider := &Provider{destroyerFunc: func(context.Context, *Config) (resourceDestroyer, error) { return d, nil }}
	_, stateStr := mustApply(t, stagedProvider(newSimulatedAWSClient("us-west-2"), nil), stageRequest(t, "", ""))
	if err := provider.Destroy(context.Background(), &deploy.DestroyRequest{
		DeployConfig: validConfig(t), PriorState: stateStr,
	}, nil); err != nil {
		t.Fatalf("destroy: %v", err)
	}
	ep := slices.Index(log, "delete "+resourceKey(ResTypeRuntimeEndpoint, "mypack/live"))
	rt := slices.Index(log, "delete "+resourceKey(ResTypeAgentRuntime, "mypack"))
	if ep < 0 || rt < 0 || ep > rt {
		t.Errorf("destroy order %v, want the endpoint deleted before the runtime", log)
	}
}

// endpointReadingChecker reports endpoint versions, as the real client does.
type endpointReadingChecker struct {
	simulatedChecker
	version string
}

func (c *endpointReadingChecker) RuntimeEndpointVersion(context.Context, string, string) (string, error) {
	return c.version, nil
}

func TestStatus_ShowsEndpointVersionsFromThePlatform(t *testing.T) {
	_, stateStr := mustApply(t, stagedProvider(newSimulatedAWSClient("us-west-2"), nil), stageRequest(t, "", ""))
	for _, tc := range []struct {
		checker resourceChecker
		want    string
	}{
		{&simulatedChecker{}, "live serves version 1"},
		{&endpointReadingChecker{version: "7"}, "live serves version 7"},
	} {
		provider := &Provider{checkerFunc: func(context.Context, *Config) (resourceChecker, error) {
			return tc.checker, nil
		}}
		resp, err := provider.Status(context.Background(), &deploy.StatusRequest{
			DeployConfig: validConfig(t), PriorState: stateStr,
		})
		if err != nil {
			t.Fatalf("status: %v", err)
		}
		var detail, runtimeDetail string
		for _, r := range resp.Resources {
			switch r.Type {
			case ResTypeRuntimeEndpoint:
				detail = r.Detail
			case ResTypeAgentRuntime:
				runtimeDetail = r.Detail
			}
		}
		if !strings.Contains(detail, tc.want) {
			t.Errorf("endpoint detail = %q, want %q", detail, tc.want)
		}
		if !strings.Contains(runtimeDetail, "--qualifier live") {
			t.Errorf("runtime detail = %q, want the invoke hint to use the live endpoint", runtimeDetail)
		}
		if tc.want == "live serves version 7" && !strings.Contains(resp.State, `"version":"7"`) {
			t.Errorf("status state does not record the version read: %s", resp.State)
		}
	}
}

func TestRuntimeIDFromARN(t *testing.T) {
	for arn, want := range map[string]string{
		"arn:aws:bedrock-agentcore:us-east-1:1:runtime/acproj-Lx":                       "acproj-Lx",
		"arn:aws:bedrock-agentcore:us-east-1:1:runtime/acproj-Lx/runtime-endpoint/live": "acproj-Lx",
		"not-an-arn": "",
	} {
		if got := runtimeIDFromARN(arn); got != want {
			t.Errorf("runtimeIDFromARN(%q) = %q, want %q", arn, got, want)
		}
	}
}

// packVersionFromDescription reads the pack version back out of a
// description, as a consumer of the documented format would.
func packVersionFromDescription(desc string) string {
	m := packVersionTokenRE.FindString(desc)
	return strings.TrimPrefix(strings.TrimSpace(m), packVersionKey+"=")
}

func TestPromoteOutcome(t *testing.T) {
	for _, tc := range []struct {
		prior            ResourceState
		current, version string
		action           deploy.Action
		status           string
	}{
		{ResourceState{}, "", "3", deploy.ActionCreate, ResStatusCreated},
		{ResourceState{Status: ResStatusCreated}, "2", "3", deploy.ActionUpdate, ResStatusUpdated},
		{ResourceState{Status: ResStatusCreated}, "3", "3", deploy.ActionNoChange, ResStatusCreated},
		{ResourceState{}, "3", "3", deploy.ActionNoChange, ResStatusUpdated},
	} {
		action, status := promoteOutcome(tc.prior, tc.current, tc.version)
		if action != tc.action || status != tc.status {
			t.Errorf("promoteOutcome(%+v, %q, %q) = %s/%s, want %s/%s",
				tc.prior, tc.current, tc.version, action, status, tc.action, tc.status)
		}
	}
}
