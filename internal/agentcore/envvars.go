package agentcore

import "strconv"

// Environment variable keys injected into AgentCore runtimes.
const (
	EnvLogGroup       = "PROMPTPACK_LOG_GROUP"
	EnvTracingEnabled = "PROMPTPACK_TRACING_ENABLED"
	EnvMemoryStore    = "PROMPTPACK_MEMORY_STORE"
	EnvMemoryID       = "PROMPTPACK_MEMORY_ID"
	EnvA2AAgents      = "PROMPTPACK_AGENTS"
	EnvA2AAuthMode    = "PROMPTPACK_A2A_AUTH_MODE"
	EnvA2AAuthRole    = "PROMPTPACK_A2A_AUTH_ROLE"
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

	return env
}
