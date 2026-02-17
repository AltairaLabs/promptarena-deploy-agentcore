package agentcore

import (
	"encoding/json"
	"strconv"
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
)

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
