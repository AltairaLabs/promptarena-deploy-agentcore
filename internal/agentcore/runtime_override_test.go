package agentcore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/bedrockagentcorecontrol/types"

	"github.com/AltairaLabs/promptarena/v2/deploy"
)

const testOverrideImage = "123456789012.dkr.ecr.us-west-2.amazonaws.com/replacement:v1"

// overrideConfig returns a deploy config with the given runtime_override
// block and no runtime_binary_path.
func overrideConfig(override string) string {
	return `{"region":"us-west-2","runtime_role_arn":"arn:aws:iam::123456789012:role/test",` +
		`"runtime_override":` + override + `}`
}

// recordingAWSClient is a simulated client that records what the runtime
// phase and the uploads were given.
type recordingAWSClient struct {
	*simulatedAWSClient
	runtimeEnv  map[string]map[string]string
	artifacts   map[string]types.AgentRuntimeArtifact
	uploadKeys  []string
	uploadBytes [][]byte
	uploadErr   error
	updated     []string
}

func newRecordingClient() *recordingAWSClient {
	return &recordingAWSClient{
		simulatedAWSClient: newSimulatedAWSClient("us-west-2"),
		runtimeEnv:         map[string]map[string]string{},
		artifacts:          map[string]types.AgentRuntimeArtifact{},
	}
}

func (c *recordingAWSClient) CreateRuntime(ctx context.Context, name string, cfg *Config) (string, error) {
	c.runtimeEnv[name] = runtimeEnvVarsForAgent(cfg, name)
	c.artifacts[name] = buildRuntimeArtifact(cfg)
	return c.simulatedAWSClient.CreateRuntime(ctx, name, cfg)
}

func (c *recordingAWSClient) UpdateRuntime(ctx context.Context, arn, name string, cfg *Config) (string, error) {
	c.runtimeEnv[name] = runtimeEnvVarsForAgent(cfg, name)
	c.artifacts[name] = buildRuntimeArtifact(cfg)
	c.updated = append(c.updated, name)
	return c.simulatedAWSClient.UpdateRuntime(ctx, arn, name, cfg)
}

func (c *recordingAWSClient) UploadCodePackage(_ context.Context, data []byte, bucket, key string) error {
	if c.uploadErr != nil {
		return c.uploadErr
	}
	c.uploadKeys = append(c.uploadKeys, bucket+"/"+key)
	c.uploadBytes = append(c.uploadBytes, data)
	return nil
}

func providerWith(client awsClient) *Provider {
	p := newSimulatedProvider()
	p.awsClientFunc = func(_ context.Context, _ *Config) (awsClient, error) { return client, nil }
	return p
}

func TestRuntimeOverride_Capability(t *testing.T) {
	info, err := NewProvider().GetProviderInfo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(info.Capabilities, deploy.RuntimeOverrideCapability) {
		t.Errorf("capabilities %v do not advertise %q", info.Capabilities, deploy.RuntimeOverrideCapability)
	}
}

func TestRuntimeOverride_Schema(t *testing.T) {
	var doc map[string]any
	if err := json.Unmarshal([]byte(configSchema), &doc); err != nil {
		t.Fatalf("schema is not JSON: %v", err)
	}
	props := doc["properties"].(map[string]any)
	block, ok := props[deploy.RuntimeOverrideConfigKey].(map[string]any)
	if !ok {
		t.Fatalf("schema has no %s property", deploy.RuntimeOverrideConfigKey)
	}
	if block["type"] != "object" {
		t.Errorf("runtime_override type = %v, want object", block["type"])
	}
	// Every property the adapter defines itself survived the merge.
	for _, key := range []string{"region", "runtime_role_arn", "runtime_binary_path", "providers"} {
		if _, ok := props[key]; !ok {
			t.Errorf("schema lost property %q", key)
		}
	}
}

func TestWithRuntimeOverride_PanicsOnBadSchema(t *testing.T) {
	for name, schema := range map[string]string{
		"not json":      "{",
		"no properties": `{"type":"object"}`,
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Error("expected a panic")
				}
			}()
			withRuntimeOverride(schema)
		})
	}
}

func TestParseConfig_RuntimeOverride(t *testing.T) {
	cfg, err := parseConfig(overrideConfig(`{"image":"` + testOverrideImage + `","env":{"A":"1"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.overrideImage() != testOverrideImage || cfg.RuntimeOverride.Env["A"] != "1" {
		t.Errorf("override not parsed: %+v", cfg.RuntimeOverride)
	}
	if !cfg.usesContainerImage() {
		t.Error("usesContainerImage = false with an image set")
	}

	empty, err := parseConfig(overrideConfig(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if empty.RuntimeOverride != nil || empty.usesContainerImage() {
		t.Errorf("empty block should be no override, got %+v", empty.RuntimeOverride)
	}

	if _, err := parseConfig(overrideConfig(`{"imagee":"x"}`)); err == nil {
		t.Error("a misspelled key should be refused")
	}
}

func TestValidate_RuntimeOverride(t *testing.T) {
	long := strings.Repeat("x", maxEnvValueLen+1)
	tests := []struct {
		name     string
		override string
		want     []string // substrings, one per expected error
	}{
		{name: "image only", override: `{"image":"` + testOverrideImage + `"}`},
		{name: "image and env", override: `{"image":"` + testOverrideImage + `","env":{"MY_URL":"wss://x"}}`},
		{name: "env only needs the binary", override: `{"env":{"A":"1"}}`, want: []string{"runtime_binary_path is required"}},
		{name: "ECR Public image", override: `{"image":"public.ecr.aws/org/img:v1"}`},
		{name: "ECR digest", override: `{"image":"123456789012.dkr.ecr.us-east-1.amazonaws.com/a/b@sha256:abc"}`},
		{name: "ECR uppercase repo", override: `{"image":"123456789012.dkr.ecr.us-east-1.amazonaws.com/Repo:v1"}`, want: []string{"not an Amazon ECR image"}},
		{name: "non-ECR image", override: `{"image":"ghcr.io/org/img:v1"}`, want: []string{"not an Amazon ECR image"}},
		{name: "command", override: `{"image":"` + testOverrideImage + `","command":["/bin/sh"]}`, want: []string{"command is not supported"}},
		{name: "args", override: `{"image":"` + testOverrideImage + `","args":["-v"]}`, want: []string{"args is not supported"}},
		{
			name:     "secret env",
			override: `{"image":"` + testOverrideImage + `","secret_env":{"KEY":{"name":"s"}}}`,
			want:     []string{"secret_env is not supported"},
		},
		{name: "adapter prefix", override: `{"image":"` + testOverrideImage + `","env":{"PROMPTPACK_X":"1"}}`, want: []string{"reserved"}},
		{name: "aws prefix", override: `{"image":"` + testOverrideImage + `","env":{"AWS_REGION":"eu-west-1"}}`, want: []string{"reserved"}},
		{name: "otlp endpoint", override: `{"image":"` + testOverrideImage + `","env":{"OTEL_EXPORTER_OTLP_ENDPOINT":"x"}}`, want: []string{"reserved"}},
		{name: "control character", override: `{"image":"` + testOverrideImage + `","env":{"CFG":"{\n}"}}`, want: []string{"control character"}},
		{name: "too many variables", override: `{"image":"` + testOverrideImage + `","env":` + manyEnv(maxEnvVars-maxAdapterEnvVars+1) + `}`, want: []string{"so at most"}},
		{name: "value too long", override: `{"image":"` + testOverrideImage + `","env":{"BIG":"` + long + `"}}`, want: []string{"at most 5000"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := parseConfig(overrideConfig(tt.override))
			if err != nil {
				t.Fatal(err)
			}
			errs := cfg.validate()
			if len(errs) != len(tt.want) {
				t.Fatalf("errors = %v, want %d matching %v", errs, len(tt.want), tt.want)
			}
			for _, w := range tt.want {
				if !hasErrorContaining(errs, w) {
					t.Errorf("errors %v lack %q", errs, w)
				}
			}
		})
	}
}

func TestValidate_RuntimeOverrideImageSkipsBinaryArchCheck(t *testing.T) {
	// A binary for the wrong architecture is irrelevant when an image runs.
	path := writeTemp(t, "runtime", elfHeader(0x3E))
	raw := fmt.Sprintf(`{"region":"us-west-2","runtime_role_arn":"arn:aws:iam::123456789012:role/test",`+
		`"runtime_binary_path":%q,"runtime_override":{"image":%q}}`, path, testOverrideImage)
	cfg, err := parseConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	if errs := cfg.validate(); len(errs) != 0 {
		t.Errorf("unexpected errors: %v", errs)
	}
}

func TestApply_RuntimeOverride_ContainerArtifactAndHandoff(t *testing.T) {
	client := newRecordingClient()
	req := &deploy.PlanRequest{
		ArenaConfig:  validArenaConfigJSON,
		PackJSON:     singleAgentPack(),
		DeployConfig: overrideConfig(`{"image":"` + testOverrideImage + `","env":{"MY_URL":"wss://example.invalid"}}`),
	}
	_, _, err := collectEvents(t, providerWith(client), req)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if len(client.uploadKeys) != 0 {
		t.Errorf("an inline pack should upload nothing, uploaded %v", client.uploadKeys)
	}
	art, ok := client.artifacts["mypack"].(*types.AgentRuntimeArtifactMemberContainerConfiguration)
	if !ok {
		t.Fatalf("artifact = %T, want a container configuration", client.artifacts["mypack"])
	}
	if got := *art.Value.ContainerUri; got != testOverrideImage {
		t.Errorf("container URI = %q", got)
	}

	env := client.runtimeEnv["mypack"]
	if env[deploy.EnvPackJSON] != singleAgentPack() {
		t.Errorf("%s not set to the pack", deploy.EnvPackJSON)
	}
	if _, ok := env[deploy.EnvPackURI]; ok {
		t.Errorf("%s set for an inline pack", deploy.EnvPackURI)
	}
	// The runtime is named after the pack; the replacement is still told its one prompt.
	if env[deploy.EnvAgent] != "chat" {
		t.Errorf("%s = %q, want chat", deploy.EnvAgent, env[deploy.EnvAgent])
	}
	if env["MY_URL"] != "wss://example.invalid" {
		t.Errorf("override env not applied: %v", env)
	}
	if env["AWS_REGION"] != "us-west-2" {
		t.Error("adapter env lost")
	}
}

func TestApply_RuntimeOverride_LargePackStagedToS3(t *testing.T) {
	var pack map[string]any
	_ = json.Unmarshal([]byte(singleAgentPack()), &pack)
	pack["description"] = strings.Repeat("d", maxEnvValueLen)
	b, _ := json.Marshal(pack)

	client := newRecordingClient()
	req := &deploy.PlanRequest{
		ArenaConfig:  validArenaConfigJSON,
		PackJSON:     string(b),
		DeployConfig: overrideConfig(`{"image":"` + testOverrideImage + `"}`),
	}
	if _, _, err := collectEvents(t, providerWith(client), req); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	wantKey := "bedrock-agentcore-code-123456789012-us-west-2/promptkit/mypack/v1.0.0/pack.json"
	if len(client.uploadKeys) != 1 || client.uploadKeys[0] != wantKey {
		t.Fatalf("uploads = %v, want [%s]", client.uploadKeys, wantKey)
	}
	if string(client.uploadBytes[0]) != string(b) {
		t.Error("staged object is not the pack")
	}
	env := client.runtimeEnv["mypack"]
	if env[deploy.EnvPackURI] != "s3://"+wantKey {
		t.Errorf("%s = %q", deploy.EnvPackURI, env[deploy.EnvPackURI])
	}
	if _, ok := env[deploy.EnvPackJSON]; ok {
		t.Errorf("%s set for a staged pack", deploy.EnvPackJSON)
	}
}

func TestApply_RuntimeOverride_StagingFailure(t *testing.T) {
	var pack map[string]any
	_ = json.Unmarshal([]byte(singleAgentPack()), &pack)
	pack["description"] = strings.Repeat("d", maxEnvValueLen)
	b, _ := json.Marshal(pack)

	client := newRecordingClient()
	client.uploadErr = errors.New("access denied")
	req := &deploy.PlanRequest{
		ArenaConfig:  validArenaConfigJSON,
		PackJSON:     string(b),
		DeployConfig: overrideConfig(`{"image":"` + testOverrideImage + `"}`),
	}
	_, _, err := collectEvents(t, providerWith(client), req)
	if err == nil || !strings.Contains(err.Error(), "stage pack for runtime_override") {
		t.Fatalf("err = %v, want a staging error", err)
	}
	if len(client.runtimeEnv) != 0 {
		t.Error("runtime created after the pack failed to stage")
	}
}

func TestApply_RuntimeOverride_EnvOnlyKeepsCodePackage(t *testing.T) {
	client := newRecordingClient()
	raw := fmt.Sprintf(`{"region":"us-west-2","runtime_role_arn":"arn:aws:iam::123456789012:role/test",`+
		`"runtime_binary_path":%q,"runtime_override":{"env":{"EXTRA":"yes"}}}`, testBinaryPath(t))
	req := &deploy.PlanRequest{ArenaConfig: validArenaConfigJSON, PackJSON: singleAgentPack(), DeployConfig: raw}
	if _, _, err := collectEvents(t, providerWith(client), req); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if _, ok := client.artifacts["mypack"].(*types.AgentRuntimeArtifactMemberCodeConfiguration); !ok {
		t.Errorf("artifact = %T, want the code package", client.artifacts["mypack"])
	}
	if len(client.uploadKeys) != 1 || !strings.HasSuffix(client.uploadKeys[0], "deployment_package.zip") {
		t.Errorf("uploads = %v, want the code package", client.uploadKeys)
	}
	env := client.runtimeEnv["mypack"]
	if env["EXTRA"] != "yes" {
		t.Error("override env not applied to the adapter's runtime")
	}
	if _, ok := env[deploy.EnvPackJSON]; ok {
		t.Error("the code package carries the pack; it should not be inlined")
	}
	if _, ok := env[deploy.EnvAgent]; ok {
		t.Error("the adapter's runtime auto-discovers its prompt; PROMPTPACK_AGENT should stay unset")
	}
}

func TestSoleAgentName(t *testing.T) {
	if got := soleAgentName(map[string]bool{"a": true}); got != "a" {
		t.Errorf("one prompt: %q", got)
	}
	if got := soleAgentName(map[string]bool{"a": true, "b": true}); got != "" {
		t.Errorf("two prompts: %q", got)
	}
	if got := soleAgentName(nil); got != "" {
		t.Errorf("no prompts: %q", got)
	}
}

func TestPlan_RuntimeOverrideAnnotatesRuntimes(t *testing.T) {
	req := &deploy.PlanRequest{
		ArenaConfig:  validArenaConfigJSON,
		PackJSON:     multiAgentPack(),
		DeployConfig: overrideConfig(`{"image":"` + testOverrideImage + `"}`),
	}
	resp, err := newSimulatedProvider().Plan(context.Background(), req)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	runtimes := 0
	for _, c := range resp.Changes {
		annotated := strings.Contains(c.Detail, testOverrideImage)
		if c.Type == ResTypeAgentRuntime {
			runtimes++
			if !annotated {
				t.Errorf("runtime %s detail %q does not name the image", c.Name, c.Detail)
			}
		} else if annotated {
			t.Errorf("%s %s annotated with the image", c.Type, c.Name)
		}
	}
	if runtimes == 0 {
		t.Fatal("plan has no runtimes")
	}

	// An update names the image too, and a delete does not.
	changes := []deploy.ResourceChange{
		{Type: ResTypeAgentRuntime, Name: "a", Action: deploy.ActionUpdate, Detail: "Update"},
		{Type: ResTypeAgentRuntime, Name: "b", Action: deploy.ActionDelete, Detail: "Delete"},
	}
	cfg := &Config{RuntimeOverride: &deploy.RuntimeOverride{Image: testOverrideImage}}
	annotateRuntimeOverride(changes, cfg)
	if !strings.Contains(changes[0].Detail, testOverrideImage) || changes[1].Detail != "Delete" {
		t.Errorf("annotations = %q, %q", changes[0].Detail, changes[1].Detail)
	}
}

func TestApplyDryRun_RuntimeOverrideAnnotated(t *testing.T) {
	req := &deploy.PlanRequest{
		ArenaConfig: validArenaConfigJSON,
		PackJSON:    singleAgentPack(),
		DeployConfig: `{"region":"us-west-2","runtime_role_arn":"arn:aws:iam::123456789012:role/test",` +
			`"dry_run":true,"runtime_override":{"image":"` + testOverrideImage + `"}}`,
	}
	events, _, err := collectEvents(t, newSimulatedProvider(), req)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	for _, ev := range events {
		if ev.Resource != nil && ev.Resource.Type == ResTypeAgentRuntime &&
			strings.Contains(ev.Resource.Detail, testOverrideImage) {
			return
		}
	}
	t.Error("dry run does not name the override image")
}

func TestStageOverridePack_CompactsIndentedPack(t *testing.T) {
	var indented bytes.Buffer
	if err := json.Indent(&indented, []byte(singleAgentPack()), "", "  "); err != nil {
		t.Fatal(err)
	}
	cfg := &Config{RuntimeEnvVars: map[string]string{}}
	if err := stageOverridePack(context.Background(), newRecordingClient(), cfg, indented.String()); err != nil {
		t.Fatal(err)
	}
	got := cfg.RuntimeEnvVars[deploy.EnvPackJSON]
	if strings.ContainsFunc(got, isControlChar) {
		t.Errorf("inline pack holds a control character: %q", got)
	}
	if got != singleAgentPack() {
		t.Errorf("compacted pack = %s, want %s", got, singleAgentPack())
	}

	if err := stageOverridePack(context.Background(), newRecordingClient(), cfg, "{not json"); err == nil {
		t.Error("a pack that is not JSON should be refused")
	}
}

// manyEnv returns a JSON object of n distinct variables.
func manyEnv(n int) string {
	parts := make([]string, n)
	for i := range parts {
		parts[i] = fmt.Sprintf(`"V%d":"x"`, i)
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func TestApply_RuntimeOverride_ValidatedWithoutPlan(t *testing.T) {
	client := newRecordingClient()
	req := &deploy.PlanRequest{
		ArenaConfig:  validArenaConfigJSON,
		PackJSON:     singleAgentPack(),
		DeployConfig: overrideConfig(`{"image":"` + testOverrideImage + `","env":{"AWS_REGION":"eu-west-1"}}`),
	}
	_, _, err := collectEvents(t, providerWith(client), req)
	if err == nil || !strings.Contains(err.Error(), "invalid runtime_override") {
		t.Fatalf("err = %v, want an invalid runtime_override error", err)
	}
	if len(client.runtimeEnv) != 0 || len(client.uploadKeys) != 0 {
		t.Error("Apply acted on an invalid override")
	}
}

func TestApply_RuntimeOverride_UpdatesMultiAgentRuntimes(t *testing.T) {
	client := newRecordingClient()
	prior := AdapterState{PackID: "multipack", Version: "v1.0.0", Resources: []ResourceState{
		{Type: ResTypeAgentRuntime, Name: "coordinator", Status: ResStatusCreated,
			ARN: "arn:aws:bedrock-agentcore:us-west-2:123456789012:runtime/coordinator"},
		{Type: ResTypeAgentRuntime, Name: "worker", Status: ResStatusCreated,
			ARN: "arn:aws:bedrock-agentcore:us-west-2:123456789012:runtime/worker"},
	}}
	priorJSON, _ := json.Marshal(prior)
	req := &deploy.PlanRequest{
		ArenaConfig:  validArenaConfigJSON,
		PackJSON:     multiAgentPack(),
		PriorState:   string(priorJSON),
		DeployConfig: overrideConfig(`{"image":"` + testOverrideImage + `"}`),
	}
	if _, _, err := collectEvents(t, providerWith(client), req); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	for _, name := range []string{"coordinator", "worker"} {
		if !slices.Contains(client.updated, name) {
			t.Errorf("runtime %s was not updated", name)
		}
		if _, ok := client.artifacts[name].(*types.AgentRuntimeArtifactMemberContainerConfiguration); !ok {
			t.Errorf("%s artifact = %T, want a container configuration", name, client.artifacts[name])
		}
		if got := client.runtimeEnv[name][deploy.EnvAgent]; got != name {
			t.Errorf("%s %s = %q", name, deploy.EnvAgent, got)
		}
	}
}
