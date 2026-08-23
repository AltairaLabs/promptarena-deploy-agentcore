package agentcore

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/AltairaLabs/promptarena/deploy"
)

// arenaWith builds an ArenaConfig with the given loaded providers.
func arenaWith(providers map[string]*ArenaProvider) *ArenaConfig {
	return &ArenaConfig{LoadedProviders: providers}
}

// --- resolution ---

func TestResolveProviders_InlineTypeAndModel(t *testing.T) {
	cfg := &Config{
		Providers: []ProviderBinding{
			{Name: "default", Role: RoleLLM, Type: "claude", Model: "claude-sonnet-4"},
		},
	}
	got, warns := resolveProviderBindings(cfg)
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 resolved provider, got %d", len(got))
	}
	if got[0].Type != "claude" || got[0].Model != "claude-sonnet-4" {
		t.Errorf("got %+v, want type=claude model=claude-sonnet-4", got[0])
	}
	if !got[0].Primary {
		t.Error("binding named default should be primary")
	}
}

func TestResolveProviders_ArenaProviderRefInheritsTypeAndModel(t *testing.T) {
	cfg := &Config{
		ArenaConfig: arenaWith(map[string]*ArenaProvider{
			"sonnet": {Type: "claude", Model: "claude-sonnet-4"},
			"haiku":  {Type: "claude", Model: "claude-haiku-4-5"},
		}),
		Providers: []ProviderBinding{
			{Name: "default", ArenaProvider: "haiku"},
		},
	}
	got, _ := resolveProviderBindings(cfg)
	if len(got) != 1 {
		t.Fatalf("expected 1 resolved provider, got %d", len(got))
	}
	if got[0].Model != "claude-haiku-4-5" {
		t.Errorf("model = %q, want claude-haiku-4-5 (the named arena provider)", got[0].Model)
	}
	if got[0].Role != RoleLLM {
		t.Errorf("role = %q, want %q (the default)", got[0].Role, RoleLLM)
	}
}

func TestResolveProviders_InlineOverridesArenaProvider(t *testing.T) {
	cfg := &Config{
		ArenaConfig: arenaWith(map[string]*ArenaProvider{
			"sonnet": {Type: "claude", Model: "claude-sonnet-4"},
		}),
		Providers: []ProviderBinding{
			{Name: "default", ArenaProvider: "sonnet", Model: "claude-opus-4"},
		},
	}
	got, _ := resolveProviderBindings(cfg)
	if got[0].Model != "claude-opus-4" {
		t.Errorf("model = %q, want the inline override claude-opus-4", got[0].Model)
	}
	if got[0].Type != "claude" {
		t.Errorf("type = %q, want claude inherited from the arena provider", got[0].Type)
	}
}

func TestResolveProviders_NoDefaultWarnsAndNamesPrimary(t *testing.T) {
	cfg := &Config{
		Providers: []ProviderBinding{
			{Name: "embed", Role: RoleEmbedding, Type: "claude", Model: "embed-1"},
			{Name: "chat", Role: RoleLLM, Type: "claude", Model: "claude-sonnet-4"},
		},
	}
	got, warns := resolveProviderBindings(cfg)
	if len(warns) != 1 {
		t.Fatalf("expected exactly 1 warning, got %v", warns)
	}
	if !strings.Contains(warns[0], "chat") {
		t.Errorf("warning must name the binding chosen as primary, got %q", warns[0])
	}
	primaries := 0
	for _, p := range got {
		if p.Primary {
			primaries++
			if p.Name != "chat" {
				t.Errorf("primary = %q, want chat (first llm-role binding)", p.Name)
			}
		}
	}
	if primaries != 1 {
		t.Errorf("expected exactly 1 primary, got %d", primaries)
	}
}

func TestResolveProviders_MultipleRolesAllResolved(t *testing.T) {
	cfg := &Config{
		Providers: []ProviderBinding{
			{Name: "default", Role: RoleLLM, Type: "claude", Model: "claude-sonnet-4"},
			{Name: "embed", Role: RoleEmbedding, Type: "titan", Model: "titan-embed-v2"},
			{Name: "voice", Role: RoleTTS, Type: "polly", Model: "neural"},
		},
	}
	got, _ := resolveProviderBindings(cfg)
	if len(got) != 3 {
		t.Fatalf("expected 3 resolved providers, got %d", len(got))
	}
	roles := map[string]string{}
	for _, p := range got {
		roles[p.Role] = p.Model
	}
	if roles[RoleEmbedding] != "titan-embed-v2" || roles[RoleTTS] != "neural" {
		t.Errorf("roles not resolved correctly: %+v", roles)
	}
}

// --- legacy fallback ---

func TestResolveProviders_LegacyFallbackIsDeterministic(t *testing.T) {
	arena := arenaWith(map[string]*ArenaProvider{
		"zeta":  {Type: "claude", Model: "zeta-model"},
		"alpha": {Type: "claude", Model: "alpha-model"},
		"mid":   {Type: "claude", Model: "mid-model"},
	})

	// Same config resolved repeatedly must always pick the same provider.
	// A map-iteration-order implementation fails this within a few runs.
	first, warns := resolveProviderBindings(&Config{ArenaConfig: arena})
	if len(first) != 1 {
		t.Fatalf("expected 1 provider from legacy fallback, got %d", len(first))
	}
	if len(warns) == 0 {
		t.Error("legacy fallback must emit a deprecation warning")
	}
	if first[0].Model != "alpha-model" {
		t.Errorf("model = %q, want alpha-model (lowest key, sorted)", first[0].Model)
	}
	for i := range 50 {
		got, _ := resolveProviderBindings(&Config{ArenaConfig: arena})
		if got[0].Model != first[0].Model {
			t.Fatalf("run %d picked %q, first run picked %q — selection is not deterministic",
				i, got[0].Model, first[0].Model)
		}
	}
}

func TestResolveProviders_NoLLMBindingWarnsAndLeavesNoPrimary(t *testing.T) {
	cfg := &Config{
		Providers: []ProviderBinding{
			{Name: "embed", Role: RoleEmbedding, Type: "titan", Model: "titan-embed-v2"},
		},
	}
	got, warns := resolveProviderBindings(cfg)
	if len(warns) != 1 || !strings.Contains(warns[0], RoleLLM) {
		t.Errorf("expected a warning about the missing llm role, got %v", warns)
	}
	for _, p := range got {
		if p.Primary {
			t.Errorf("no binding should be primary when none has role llm, got %+v", p)
		}
	}
}

func TestResolveProviders_NilConfig(t *testing.T) {
	got, warns := resolveProviderBindings(nil)
	if got != nil || warns != nil {
		t.Errorf("nil config should resolve to nothing, got %v / %v", got, warns)
	}
}

func TestArenaConfig_IgnoresNilProviderEntries(t *testing.T) {
	// A nil value must not be selected as the lowest key, even though "aaa"
	// sorts first.
	arena := arenaWith(map[string]*ArenaProvider{
		"aaa": nil,
		"bbb": {Type: "claude", Model: "bbb-model"},
	})
	name, p := arena.firstProvider()
	if p == nil {
		t.Fatal("expected the non-nil provider to be selected")
	}
	if name != "bbb" || p.Model != "bbb-model" {
		t.Errorf("selected %q/%+v, want bbb", name, p)
	}
}

func TestResolveProviders_NoProvidersAnywhere(t *testing.T) {
	got, _ := resolveProviderBindings(&Config{})
	if len(got) != 0 {
		t.Errorf("expected no resolved providers, got %+v", got)
	}
}

// --- validation ---

func TestValidateProviderBindings(t *testing.T) {
	arena := arenaWith(map[string]*ArenaProvider{
		"sonnet": {Type: "claude", Model: "claude-sonnet-4"},
	})

	tests := []struct {
		name     string
		bindings []ProviderBinding
		wantErr  string
	}{
		{
			name:     "valid inline",
			bindings: []ProviderBinding{{Name: "default", Type: "claude", Model: "m"}},
		},
		{
			name:     "valid arena ref",
			bindings: []ProviderBinding{{Name: "default", ArenaProvider: "sonnet"}},
		},
		{
			name:     "empty name",
			bindings: []ProviderBinding{{Name: "", Type: "claude", Model: "m"}},
			wantErr:  "name is required",
		},
		{
			name: "duplicate names",
			bindings: []ProviderBinding{
				{Name: "default", Type: "claude", Model: "m"},
				{Name: "default", Type: "claude", Model: "n"},
			},
			wantErr: "duplicate",
		},
		{
			name:     "invalid role",
			bindings: []ProviderBinding{{Name: "default", Role: "telepathy", Type: "claude", Model: "m"}},
			wantErr:  "role",
		},
		{
			name:     "unknown arena provider",
			bindings: []ProviderBinding{{Name: "default", ArenaProvider: "nope"}},
			wantErr:  "not found",
		},
		{
			name:     "missing type",
			bindings: []ProviderBinding{{Name: "default", Model: "m"}},
			wantErr:  "type",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{ArenaConfig: arena, Providers: tt.bindings}
			errs := validateProviderBindings(cfg)
			if tt.wantErr == "" {
				if len(errs) != 0 {
					t.Fatalf("expected no errors, got %v", errs)
				}
				return
			}
			if len(errs) == 0 {
				t.Fatalf("expected an error containing %q, got none", tt.wantErr)
			}
			joined := strings.Join(errs, "; ")
			if !strings.Contains(joined, tt.wantErr) {
				t.Errorf("errors %q do not mention %q", joined, tt.wantErr)
			}
		})
	}
}

// ValidateConfig receives only the deploy config — deploy.ValidateRequest has
// no arena config field — so an arena_provider reference cannot be resolved
// there. Rejecting it outright would make every binding that names an arena
// provider fail standalone validation.
func TestValidateProviderBindings_ArenaRefSkippedWhenNoArenaConfig(t *testing.T) {
	cfg := &Config{
		Providers: []ProviderBinding{{Name: "default", ArenaProvider: "sonnet"}},
	}
	if errs := validateProviderBindings(cfg); len(errs) != 0 {
		t.Errorf("arena_provider must not be rejected when there is no arena config to check against, got %v", errs)
	}
}

// The reference is still checked when an arena config IS present, so a typo
// is caught at plan time rather than deploying the wrong model.
func TestValidateProviderBindings_ArenaRefStillCheckedWhenArenaConfigPresent(t *testing.T) {
	cfg := &Config{
		ArenaConfig: arenaWith(map[string]*ArenaProvider{"sonnet": {Type: "claude", Model: "m"}}),
		Providers:   []ProviderBinding{{Name: "default", ArenaProvider: "typo"}},
	}
	errs := validateProviderBindings(cfg)
	if len(errs) == 0 || !strings.Contains(strings.Join(errs, ";"), "not found") {
		t.Errorf("expected an unknown arena_provider to be rejected, got %v", errs)
	}
}

// Plan must parse the arena config before validating, or an arena_provider
// binding is checked against a nil arena config and always fails. This is the
// real end-to-end path and is not covered by tests that build Config directly.
func TestPlan_ArenaProviderBindingResolves(t *testing.T) {
	provider := NewProvider()

	deployCfg := `{
		"region":"us-west-2",
		"runtime_role_arn":"arn:aws:iam::123456789012:role/test",
		"runtime_binary_path":"/usr/local/bin/promptkit-runtime",
		"providers":[{"name":"default","role":"llm","arena_provider":"sonnet"}]
	}`
	arenaCfg := `{"tool_specs":{},"loaded_providers":{"sonnet":{"type":"claude","model":"claude-sonnet-4"}}}`

	resp, err := provider.Plan(context.Background(), &deploy.PlanRequest{
		PackJSON:     singleAgentPackJSON(),
		DeployConfig: deployCfg,
		ArenaConfig:  arenaCfg,
	})
	if err != nil {
		t.Fatalf("Plan rejected a valid arena_provider binding: %v", err)
	}
	if len(resp.Changes) == 0 {
		t.Error("expected planned changes")
	}
}

// A genuinely wrong reference must still be rejected once the arena config is
// known, so a typo does not silently deploy the wrong model.
func TestPlan_UnknownArenaProviderRejected(t *testing.T) {
	provider := NewProvider()

	deployCfg := `{
		"region":"us-west-2",
		"runtime_role_arn":"arn:aws:iam::123456789012:role/test",
		"runtime_binary_path":"/usr/local/bin/promptkit-runtime",
		"providers":[{"name":"default","role":"llm","arena_provider":"nope"}]
	}`
	arenaCfg := `{"tool_specs":{},"loaded_providers":{"sonnet":{"type":"claude","model":"claude-sonnet-4"}}}`

	_, err := provider.Plan(context.Background(), &deploy.PlanRequest{
		PackJSON:     singleAgentPackJSON(),
		DeployConfig: deployCfg,
		ArenaConfig:  arenaCfg,
	})
	if err == nil {
		t.Fatal("expected Plan to reject an unknown arena_provider, got nil")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error %q should say the arena_provider was not found", err)
	}
}

// --- diagnostics ---

// PromptKit v1.5.10 has no Bedrock-native provider for any role but llm, and
// options are applied eagerly when the runtime opens a conversation — so a
// binding that cannot construct breaks every request with no deploy-time
// signal. Warn at validate time instead.
func TestDiagnoseProviders_WarnsOnRolesBedrockCannotServe(t *testing.T) {
	tests := []struct {
		name     string
		bindings []ProviderBinding
		wantWarn bool
		mentions string
	}{
		{
			name:     "llm role is fine",
			bindings: []ProviderBinding{{Name: "default", Role: RoleLLM, Type: "claude", Model: "m"}},
			wantWarn: false,
		},
		{
			name:     "role defaulting to llm is fine",
			bindings: []ProviderBinding{{Name: "default", Type: "claude", Model: "m"}},
			wantWarn: false,
		},
		{
			name:     "embedding role warns",
			bindings: []ProviderBinding{{Name: "embed", Role: RoleEmbedding, Type: "titan", Model: "m"}},
			wantWarn: true,
			mentions: "embed",
		},
		{
			name:     "tts role warns",
			bindings: []ProviderBinding{{Name: "voice", Role: RoleTTS, Type: "polly", Model: "m"}},
			wantWarn: true,
			mentions: "voice",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := diagnoseProviders(&Config{Providers: tt.bindings})
			if !tt.wantWarn {
				if len(got) != 0 {
					t.Fatalf("expected no warnings, got %v", got)
				}
				return
			}
			if len(got) == 0 {
				t.Fatal("expected a warning, got none")
			}
			joined := got[0].String()
			if !strings.Contains(joined, tt.mentions) {
				t.Errorf("warning %q does not name the binding %q", joined, tt.mentions)
			}
		})
	}
}

func TestDiagnoseConfig_IncludesProviderWarnings(t *testing.T) {
	cfg := &Config{
		Region:         "us-west-2",
		RuntimeRoleARN: "arn:aws:iam::123456789012:role/Test",
		Providers: []ProviderBinding{
			{Name: "embed", Role: RoleEmbedding, Type: "titan", Model: "m"},
		},
	}
	var found bool
	for _, w := range DiagnoseConfig(cfg) {
		if strings.Contains(w.String(), "embed") {
			found = true
		}
	}
	if !found {
		t.Error("DiagnoseConfig should surface provider-role warnings")
	}
}

// --- env var wiring ---

func TestBuildRuntimeEnvVars_EmitsProvidersJSON(t *testing.T) {
	cfg := &Config{
		Region: "us-west-2",
		Providers: []ProviderBinding{
			{Name: "default", Role: RoleLLM, Type: "claude", Model: "claude-sonnet-4"},
			{Name: "embed", Role: RoleEmbedding, Type: "titan", Model: "titan-embed-v2"},
		},
	}
	env := buildRuntimeEnvVars(cfg, nil)

	raw, ok := env[EnvProviders]
	if !ok {
		t.Fatalf("%s not set; env = %v", EnvProviders, env)
	}
	var decoded []ResolvedProvider
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		t.Fatalf("%s is not valid JSON: %v", EnvProviders, err)
	}
	if len(decoded) != 2 {
		t.Fatalf("expected 2 providers in %s, got %d", EnvProviders, len(decoded))
	}

	// The legacy pair stays populated from the primary binding so a runtime
	// built before this change keeps working.
	if env[EnvProviderType] != "claude" || env[EnvProviderModel] != "claude-sonnet-4" {
		t.Errorf("legacy provider env vars not set from primary: type=%q model=%q",
			env[EnvProviderType], env[EnvProviderModel])
	}
}

// The schema sets additionalProperties:false, so any config field the adapter
// accepts must be declared there or a config using it is rejected outright.
func TestConfigSchemaDeclaresEverySupportedField(t *testing.T) {
	var schema struct {
		Properties           map[string]json.RawMessage `json:"properties"`
		AdditionalProperties *bool                      `json:"additionalProperties"`
	}
	if err := json.Unmarshal([]byte(configSchema), &schema); err != nil {
		t.Fatalf("configSchema is not valid JSON: %v", err)
	}
	if schema.AdditionalProperties == nil || *schema.AdditionalProperties {
		t.Skip("schema allows additional properties; this test guards the closed-schema case")
	}

	for _, field := range []string{
		"region", "runtime_role_arn", "memory_store", "tools", "observability",
		"tags", "dry_run", "a2a_auth", "runtime_binary_path", "protocol",
		"providers", "tool_targets",
	} {
		if _, ok := schema.Properties[field]; !ok {
			t.Errorf("configSchema is missing %q — a config using it would be rejected", field)
		}
	}
}

func TestBuildRuntimeEnvVars_LegacyArenaOnlyStillWorks(t *testing.T) {
	cfg := &Config{
		Region: "us-west-2",
		ArenaConfig: arenaWith(map[string]*ArenaProvider{
			"sonnet": {Type: "claude", Model: "claude-sonnet-4"},
		}),
	}
	env := buildRuntimeEnvVars(cfg, nil)
	if env[EnvProviderType] != "claude" || env[EnvProviderModel] != "claude-sonnet-4" {
		t.Errorf("legacy arena-derived provider not injected: type=%q model=%q",
			env[EnvProviderType], env[EnvProviderModel])
	}
	if env[EnvProviders] == "" {
		t.Error("PROMPTPACK_PROVIDERS should also be emitted for the legacy path")
	}
}
