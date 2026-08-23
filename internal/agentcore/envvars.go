package agentcore

import (
	"encoding/json"
	"log"
	"strconv"

	"github.com/AltairaLabs/PromptKit/runtime/prompt"
)

// Environment variable keys injected into AgentCore runtimes.
const (
	EnvLogGroup       = "PROMPTPACK_LOG_GROUP"
	EnvTracingEnabled = "PROMPTPACK_TRACING_ENABLED"

	// EnvOTLPEndpoint is the standard OpenTelemetry variable, not a
	// PROMPTPACK_ one, because the exporter in the runtime reads it by that
	// name.
	EnvOTLPEndpoint = "OTEL_EXPORTER_OTLP_ENDPOINT"

	EnvMemoryStore     = "PROMPTPACK_MEMORY_STORE"
	EnvMemoryID        = "PROMPTPACK_MEMORY_ID"
	EnvA2AAgents       = "PROMPTPACK_AGENTS"
	EnvA2AAuthMode     = "PROMPTPACK_A2A_AUTH_MODE"
	EnvA2AAuthRole     = "PROMPTPACK_A2A_AUTH_ROLE"
	EnvPolicyEngineARN = "PROMPTPACK_POLICY_ENGINE_ARN"
	EnvMetricsConfig   = "PROMPTPACK_METRICS_CONFIG"
	EnvDashboardConfig = "PROMPTPACK_DASHBOARD_CONFIG"
	EnvAgentName       = "PROMPTPACK_AGENT"
	EnvProviderType    = "PROMPTPACK_PROVIDER_TYPE"
	EnvProviderModel   = "PROMPTPACK_PROVIDER_MODEL"
	EnvProtocol        = "PROMPTPACK_PROTOCOL"

	// EnvProviders carries the full set of resolved provider bindings as a
	// JSON array. PROMPTPACK_PROVIDER_TYPE/MODEL cannot express a list with
	// roles, so they remain populated from the primary binding only, for
	// runtimes built before provider bindings existed.
	EnvProviders = "PROMPTPACK_PROVIDERS"
)

// buildRuntimeEnvVars constructs the environment variable map that will be
// passed to CreateAgentRuntime / UpdateAgentRuntime. It reads observability,
// memory, and auth settings from cfg and merges them into a single map.
func buildRuntimeEnvVars(cfg *Config, pack *prompt.Pack) map[string]string {
	env := make(map[string]string)

	if cfg.Observability != nil {
		if cfg.Observability.CloudWatchLogGroup != "" {
			env[EnvLogGroup] = cfg.Observability.CloudWatchLogGroup
		}
		if cfg.Observability.TracingEnabled {
			env[EnvTracingEnabled] = strconv.FormatBool(cfg.Observability.TracingEnabled)
		}
		if cfg.Observability.OTLPEndpoint != "" {
			env[EnvOTLPEndpoint] = cfg.Observability.OTLPEndpoint
		}
	}

	if cfg.HasMemory() {
		env[EnvMemoryStore] = cfg.MemoryStrategiesCSV()
	}

	if cfg.A2AAuth != nil && cfg.A2AAuth.Mode != "" {
		env[EnvA2AAuthMode] = cfg.A2AAuth.Mode
		if cfg.A2AAuth.Mode == A2AAuthModeIAM && cfg.RuntimeRoleARN != "" {
			env[EnvA2AAuthRole] = cfg.RuntimeRoleARN
		}
	}

	// Code deploy: pack.json is bundled in the ZIP alongside main.py.
	// The Go binary reads PROMPTPACK_FILE which is set by main.py at startup.
	// No pack-related env vars needed here.

	// AWS_REGION is required by the Go runtime to configure Bedrock as the
	// LLM provider. AgentCore may set it automatically, but we inject it
	// explicitly to ensure it's always available.
	if cfg.Region != "" {
		env["AWS_REGION"] = cfg.Region
	}

	if cfg.Protocol != "" {
		env[EnvProtocol] = cfg.Protocol
	}

	injectProviderEnvVars(env, cfg)
	injectToolEnvVars(env, pack, cfg)

	return env
}

// injectProviderEnvVars serializes the resolved provider bindings into
// PROMPTPACK_PROVIDERS, and mirrors the primary binding into the legacy
// PROMPTPACK_PROVIDER_TYPE/MODEL pair.
func injectProviderEnvVars(env map[string]string, cfg *Config) {
	resolved, warns := resolveProviderBindings(cfg)
	for _, w := range warns {
		log.Printf("agentcore: %s", w)
	}
	if len(resolved) == 0 {
		return
	}

	if b, err := json.Marshal(resolved); err == nil {
		env[EnvProviders] = string(b)
	} else {
		log.Printf("agentcore: could not encode provider bindings: %v", err)
	}

	for i := range resolved {
		if !resolved[i].Primary {
			continue
		}
		if resolved[i].Type != "" {
			env[EnvProviderType] = resolved[i].Type
		}
		if resolved[i].Model != "" {
			env[EnvProviderModel] = resolved[i].Model
		}
		return
	}
}

// runtimeEnvVarsForAgent returns a copy of cfg.RuntimeEnvVars with
// PROMPTPACK_AGENT set to the given agent name. Each runtime gets its
// own copy so the per-agent value does not leak across runtimes.
//
// For single-agent packs the runtime is named after the pack ID, which
// may not match the prompt name. When that happens we omit PROMPTPACK_AGENT
// so the runtime auto-discovers the single prompt from the pack.
func runtimeEnvVarsForAgent(cfg *Config, agentName string) map[string]string {
	env := make(map[string]string, len(cfg.RuntimeEnvVars)+1)
	for k, v := range cfg.RuntimeEnvVars {
		env[k] = v
	}
	// If PromptNames is populated and the agent name is not a known
	// prompt, skip setting PROMPTPACK_AGENT to let the runtime
	// auto-discover from the pack.
	if len(cfg.PromptNames) == 0 || cfg.PromptNames[agentName] {
		env[EnvAgentName] = agentName
	}
	return env
}

// buildA2AEndpointMap builds a JSON string mapping agent member names to their
// runtime ARNs. Only successfully created/updated runtimes are included.
func buildA2AEndpointMap(runtimeResources []ResourceState) string {
	m := make(map[string]string)
	for _, r := range runtimeResources {
		if r.Type != ResTypeAgentRuntime {
			continue
		}
		if r.Status != ResStatusCreated && r.Status != ResStatusUpdated {
			continue
		}
		m[r.Name] = r.ARN
	}
	if len(m) == 0 {
		return ""
	}
	b, _ := json.Marshal(m)
	return string(b)
}

// injectDashboardConfig builds the CloudWatch dashboard JSON from the pack
// structure and sets it as an env var on the runtime config. No-op if no
// dashboard widgets are generated.
func injectDashboardConfig(cfg *Config, pack *prompt.Pack) {
	dc := buildDashboardConfig(pack, cfg.Region)
	if dc == nil {
		return
	}
	b, err := json.Marshal(dc)
	if err != nil {
		return
	}
	cfg.RuntimeEnvVars[EnvDashboardConfig] = string(b)
}

// injectMetricsConfig builds the CloudWatch metrics configuration from pack
// evals and sets it as an env var on the runtime config. No-op if no evals
// define metrics.
func injectMetricsConfig(cfg *Config, pack *prompt.Pack) {
	mc := buildMetricsConfig(pack)
	if mc == nil {
		return
	}
	b, err := json.Marshal(mc)
	if err != nil {
		return
	}
	cfg.RuntimeEnvVars[EnvMetricsConfig] = string(b)
}
