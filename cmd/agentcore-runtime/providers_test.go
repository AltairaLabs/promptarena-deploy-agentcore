package main

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/AltairaLabs/promptarena-deploy-agentcore/internal/agentcore"
)

// setMinimalPack satisfies loadConfig's requirement that a pack be present.
func setMinimalPack(t *testing.T) {
	t.Helper()
	t.Setenv(envPackJSON, `{"id":"test"}`)
}

// The adapter writes these env vars and the runtime reads them, but each side
// declares its own string constant. Nothing else in the build links the two,
// so a rename on one side would silently break every deployed runtime.
func TestEnvVarNamesMatchTheAdapter(t *testing.T) {
	pairs := []struct {
		name             string
		adapter, runtime string
	}{
		{"providers", agentcore.EnvProviders, envProviders},
		{"provider type", agentcore.EnvProviderType, envProviderType},
		{"provider model", agentcore.EnvProviderModel, envProviderModel},
		{"agent endpoints", agentcore.EnvA2AAgents, envAgentEndpoints},
		{"agent name", agentcore.EnvAgentName, envAgentName},
		{"memory store", agentcore.EnvMemoryStore, envMemoryStore},
		{"memory id", agentcore.EnvMemoryID, envMemoryID},
		{"a2a auth mode", agentcore.EnvA2AAuthMode, envA2AAuthMode},
		{"a2a auth role", agentcore.EnvA2AAuthRole, envA2AAuthRole},
		{"policy engine arn", agentcore.EnvPolicyEngineARN, envPolicyEngineARN},
		{"metrics config", agentcore.EnvMetricsConfig, envMetricsConfig},
		{"dashboard config", agentcore.EnvDashboardConfig, envDashboardConfig},
		{"log group", agentcore.EnvLogGroup, envLogGroup},
		{"protocol", agentcore.EnvProtocol, envProtocol},
	}
	for _, p := range pairs {
		if p.adapter != p.runtime {
			t.Errorf("%s env var: adapter writes %q but runtime reads %q",
				p.name, p.adapter, p.runtime)
		}
	}
}

// Round-trips the exact bytes the adapter would write, proving the runtime
// reconstructs the same bindings and wires an SDK option for each role.
func TestProviderBindingsSurviveTheEnvVarRoundTrip(t *testing.T) {
	adapterSide := []agentcore.ResolvedProvider{
		{Name: "default", Role: agentcore.RoleLLM, Type: "claude", Model: "claude-sonnet-4", Primary: true},
		{Name: "embed", Role: agentcore.RoleEmbedding, Type: "titan", Model: "titan-embed-text-v2"},
		{Name: "voice", Role: agentcore.RoleTTS, Type: "polly", Model: "neural"},
		{Name: "ears", Role: agentcore.RoleSTT, Type: "transcribe", Model: "standard"},
		{Name: "pics", Role: agentcore.RoleImage, Type: "titan-image", Model: "v2"},
	}
	encoded, err := json.Marshal(adapterSide)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	setMinimalPack(t)
	t.Setenv(envProviders, string(encoded))

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if !reflect.DeepEqual(cfg.Providers, adapterSide) {
		t.Fatalf("round trip changed the bindings:\n adapter: %+v\n runtime: %+v",
			adapterSide, cfg.Providers)
	}

	primary := cfg.primaryProvider()
	if primary == nil || primary.Name != "default" {
		t.Fatalf("primary = %+v, want the binding named default", primary)
	}

	// One WithBedrock for the primary plus one WithXProvider for each of the
	// four other roles; every role must map to a real SDK option.
	cfg.AWSRegion = "us-west-2"
	opts := providerOptions(cfg)
	if len(opts) != len(adapterSide) {
		t.Errorf("providerOptions returned %d options for %d bindings — a role was dropped",
			len(opts), len(adapterSide))
	}
}

// Every role the adapter can emit must map to an SDK option, or bindings the
// adapter happily writes would be silently dropped at runtime.
func TestEveryAdapterRoleMapsToAnSDKOption(t *testing.T) {
	roles := []string{
		agentcore.RoleLLM, agentcore.RoleEmbedding, agentcore.RoleTTS,
		agentcore.RoleSTT, agentcore.RoleImage, agentcore.RoleInference,
	}
	for _, role := range roles {
		t.Run(role, func(t *testing.T) {
			p := agentcore.ResolvedProvider{Name: "x", Role: role, Type: "claude", Model: "m"}
			if _, ok := roleOption(p, "us-west-2"); !ok {
				t.Errorf("role %q has no SDK option — the adapter accepts it but the runtime drops it", role)
			}
		})
	}
}

func TestLoadConfig_ParsesProvidersJSON(t *testing.T) {
	setMinimalPack(t)
	t.Setenv(envProviders, `[
		{"name":"default","role":"llm","type":"claude","model":"claude-sonnet-4","primary":true},
		{"name":"embed","role":"embedding","type":"titan","model":"titan-embed-v2"}
	]`)

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if len(cfg.Providers) != 2 {
		t.Fatalf("expected 2 providers, got %d: %+v", len(cfg.Providers), cfg.Providers)
	}
	if cfg.Providers[1].Role != agentcore.RoleEmbedding {
		t.Errorf("second binding role = %q, want %q",
			cfg.Providers[1].Role, agentcore.RoleEmbedding)
	}
}

func TestLoadConfig_LegacyProviderEnvVarsSynthesizeBinding(t *testing.T) {
	setMinimalPack(t)
	t.Setenv(envProviderType, "claude")
	t.Setenv(envProviderModel, "claude-sonnet-4")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if len(cfg.Providers) != 1 {
		t.Fatalf("expected the legacy env pair to synthesize 1 binding, got %+v", cfg.Providers)
	}
	got := cfg.Providers[0]
	if got.Role != agentcore.RoleLLM || !got.Primary {
		t.Errorf("synthesized binding = %+v, want role=llm primary=true", got)
	}
	if got.Type != "claude" || got.Model != "claude-sonnet-4" {
		t.Errorf("synthesized binding = %+v, want type/model from the legacy env vars", got)
	}
}

func TestLoadConfig_ProvidersJSONWinsOverLegacyPair(t *testing.T) {
	setMinimalPack(t)
	t.Setenv(envProviderType, "legacy")
	t.Setenv(envProviderModel, "legacy-model")
	t.Setenv(envProviders, `[{"name":"default","role":"llm","type":"claude","model":"new-model","primary":true}]`)

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if len(cfg.Providers) != 1 || cfg.Providers[0].Model != "new-model" {
		t.Errorf("providers = %+v, want the PROMPTPACK_PROVIDERS entry to win", cfg.Providers)
	}
}

func TestLoadConfig_InvalidProvidersJSONErrors(t *testing.T) {
	setMinimalPack(t)
	t.Setenv(envProviders, `{not json`)

	if _, err := loadConfig(); err == nil {
		t.Fatal("expected an error for malformed PROMPTPACK_PROVIDERS, got nil")
	}
}

func TestLoadConfig_NoProviderEnvVarsLeavesBindingsEmpty(t *testing.T) {
	setMinimalPack(t)

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if len(cfg.Providers) != 0 {
		t.Errorf("expected no bindings, got %+v", cfg.Providers)
	}
}

func TestPrimaryProvider(t *testing.T) {
	tests := []struct {
		name      string
		providers []agentcore.ResolvedProvider
		want      string
	}{
		{
			name: "explicit primary flag",
			providers: []agentcore.ResolvedProvider{
				{Name: "embed", Role: agentcore.RoleEmbedding, Type: "titan"},
				{Name: "chat", Role: agentcore.RoleLLM, Type: "claude", Primary: true},
			},
			want: "chat",
		},
		{
			name: "falls back to first llm when no primary flag",
			providers: []agentcore.ResolvedProvider{
				{Name: "embed", Role: agentcore.RoleEmbedding, Type: "titan"},
				{Name: "chat", Role: agentcore.RoleLLM, Type: "claude"},
			},
			want: "chat",
		},
		{
			name: "no llm binding at all",
			providers: []agentcore.ResolvedProvider{
				{Name: "embed", Role: agentcore.RoleEmbedding, Type: "titan"},
			},
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &runtimeConfig{Providers: tt.providers}
			got := cfg.primaryProvider()
			if tt.want == "" {
				if got != nil {
					t.Fatalf("expected no primary, got %+v", got)
				}
				return
			}
			if got == nil {
				t.Fatalf("expected primary %q, got nil", tt.want)
			}
			if got.Name != tt.want {
				t.Errorf("primary = %q, want %q", got.Name, tt.want)
			}
		})
	}
}

func TestBedrockProviderSpec_CarriesPlatformForKeylessAuth(t *testing.T) {
	p := agentcore.ResolvedProvider{
		Name: "embed", Role: agentcore.RoleEmbedding, Type: "titan", Model: "titan-embed-v2",
	}
	spec := bedrockProviderSpec(p, "us-west-2")

	if spec.Type != "titan" || spec.Model != "titan-embed-v2" {
		t.Errorf("spec = %+v, want type/model from the binding", spec)
	}
	if spec.Platform == nil {
		t.Fatal("spec.Platform is nil — non-LLM roles need it for Bedrock keyless auth")
	}
	if spec.Platform.Type != "bedrock" || spec.Platform.Region != "us-west-2" {
		t.Errorf("platform = %+v, want type=bedrock region=us-west-2", spec.Platform)
	}
}

func TestBuildSDKOptions_AddsOneOptionPerNonPrimaryBinding(t *testing.T) {
	base := &runtimeConfig{
		AWSRegion: "us-west-2",
		Providers: []agentcore.ResolvedProvider{
			{Name: "default", Role: agentcore.RoleLLM, Type: "claude", Model: "m", Primary: true},
		},
	}
	withRoles := &runtimeConfig{
		AWSRegion: "us-west-2",
		Providers: []agentcore.ResolvedProvider{
			{Name: "default", Role: agentcore.RoleLLM, Type: "claude", Model: "m", Primary: true},
			{Name: "embed", Role: agentcore.RoleEmbedding, Type: "titan", Model: "e"},
			{Name: "voice", Role: agentcore.RoleTTS, Type: "polly", Model: "v"},
		},
	}

	baseCount := len(buildSDKOptions(base))
	roleCount := len(buildSDKOptions(withRoles))

	if roleCount != baseCount+2 {
		t.Errorf("options with 2 extra role bindings = %d, want %d (baseline %d + 2)",
			roleCount, baseCount+2, baseCount)
	}
}

func TestBuildSDKOptions_UnknownRoleIsSkipped(t *testing.T) {
	base := &runtimeConfig{
		AWSRegion: "us-west-2",
		Providers: []agentcore.ResolvedProvider{
			{Name: "default", Role: agentcore.RoleLLM, Type: "claude", Model: "m", Primary: true},
		},
	}
	withUnknown := &runtimeConfig{
		AWSRegion: "us-west-2",
		Providers: []agentcore.ResolvedProvider{
			{Name: "default", Role: agentcore.RoleLLM, Type: "claude", Model: "m", Primary: true},
			{Name: "weird", Role: "telepathy", Type: "esp", Model: "x"},
		},
	}

	if len(buildSDKOptions(withUnknown)) != len(buildSDKOptions(base)) {
		t.Error("a binding with an unrecognized role should not add an SDK option")
	}
}
