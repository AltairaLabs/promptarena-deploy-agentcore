package agentcore

import (
	"encoding/json"
	"strconv"

	"github.com/AltairaLabs/PromptKit/runtime/prompt"
)

// Environment variable keys injected into AgentCore runtimes.
const (
	EnvLogGroup        = "PROMPTPACK_LOG_GROUP"
	EnvTracingEnabled  = "PROMPTPACK_TRACING_ENABLED"
	EnvMemoryStore     = "PROMPTPACK_MEMORY_STORE"
	EnvMemoryID        = "PROMPTPACK_MEMORY_ID"
	EnvA2AAgents       = "PROMPTPACK_AGENTS"
	EnvA2AAuthMode     = "PROMPTPACK_A2A_AUTH_MODE"
	EnvA2AAuthRole     = "PROMPTPACK_A2A_AUTH_ROLE"
	EnvPolicyEngineARN = "PROMPTPACK_POLICY_ENGINE_ARN"
	EnvMetricsConfig   = "PROMPTPACK_METRICS_CONFIG"
	EnvDashboardConfig = "PROMPTPACK_DASHBOARD_CONFIG"
	EnvPackFile        = "PROMPTPACK_FILE"
	EnvAgentName       = "PROMPTPACK_AGENT"
)

// defaultPackPath is the default path where the pack file is mounted
// inside the container.
const defaultPackPath = "/app/pack.json"

// buildRuntimeEnvVars constructs the environment variable map that will be
// passed to CreateAgentRuntime / UpdateAgentRuntime. It reads observability,
// memory, and auth settings from cfg and merges them into a single map.
func buildRuntimeEnvVars(cfg *Config) map[string]string {
	env := make(map[string]string)

	if cfg.Observability != nil {
		if cfg.Observability.CloudWatchLogGroup != "" {
			env[EnvLogGroup] = cfg.Observability.CloudWatchLogGroup
		}
		if cfg.Observability.TracingEnabled {
			env[EnvTracingEnabled] = strconv.FormatBool(cfg.Observability.TracingEnabled)
		}
	}

	if cfg.MemoryStore != "" {
		env[EnvMemoryStore] = cfg.MemoryStore
	}

	if cfg.A2AAuth != nil && cfg.A2AAuth.Mode != "" {
		env[EnvA2AAuthMode] = cfg.A2AAuth.Mode
		if cfg.A2AAuth.Mode == A2AAuthModeIAM && cfg.RuntimeRoleARN != "" {
			env[EnvA2AAuthRole] = cfg.RuntimeRoleARN
		}
	}

	env[EnvPackFile] = defaultPackPath

	return env
}

// runtimeEnvVarsForAgent returns a copy of cfg.RuntimeEnvVars with
// PROMPTPACK_AGENT set to the given agent name. Each runtime gets its
// own copy so the per-agent value does not leak across runtimes.
func runtimeEnvVarsForAgent(cfg *Config, agentName string) map[string]string {
	env := make(map[string]string, len(cfg.RuntimeEnvVars)+1)
	for k, v := range cfg.RuntimeEnvVars {
		env[k] = v
	}
	env[EnvAgentName] = agentName
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
		if r.Status != "created" && r.Status != "updated" {
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
