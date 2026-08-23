// Package main implements the AgentCore runtime entrypoint binary.
// It loads a compiled .pack.json and serves it as an A2A HTTP agent.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"

	"github.com/AltairaLabs/promptarena-deploy-agentcore/internal/agentcore"
)

// Environment variable names.
const (
	envPackFile        = "PROMPTPACK_FILE"
	envPackJSON        = "PROMPTPACK_PACK_JSON"
	envAgentName       = "PROMPTPACK_AGENT"
	envPort            = "PROMPTPACK_PORT"
	envAWSRegion       = "AWS_REGION"
	envMemoryStore     = "PROMPTPACK_MEMORY_STORE"
	envMemoryID        = "PROMPTPACK_MEMORY_ID"
	envA2AAuthMode     = "PROMPTPACK_A2A_AUTH_MODE"
	envA2AAuthRole     = "PROMPTPACK_A2A_AUTH_ROLE"
	envPolicyEngineARN = "PROMPTPACK_POLICY_ENGINE_ARN"
	envMetricsConfig   = "PROMPTPACK_METRICS_CONFIG"
	envDashboardConfig = "PROMPTPACK_DASHBOARD_CONFIG"
	envLogGroup        = "PROMPTPACK_LOG_GROUP"
	envOTLPEndpoint    = "OTEL_EXPORTER_OTLP_ENDPOINT"
	// envTracingEnabled must match the name the adapter injects
	// (agentcore.EnvTracingEnabled). TestTracingEnvNamesMatchTheAdapter pins
	// the two together; they silently disagreed once already.
	envTracingEnabled = "PROMPTPACK_TRACING_ENABLED"
	envAgentEndpoints = "PROMPTPACK_AGENTS"
	envProviderType   = "PROMPTPACK_PROVIDER_TYPE"
	envProviderModel  = "PROMPTPACK_PROVIDER_MODEL"
	envProviders      = "PROMPTPACK_PROVIDERS"
	envProtocol       = "PROMPTPACK_PROTOCOL"

	// Tool execution config. The compiled pack carries only a tool's schema,
	// so how to run one arrives here.
	envToolSpecs       = "PROMPTPACK_TOOL_SPECS"
	envToolGatewayURL  = "PROMPTPACK_TOOL_GATEWAY_URL"
	envToolGatewayAuth = "PROMPTPACK_TOOL_GATEWAY_AUTH"
)

const defaultPort = 9000

// runtimeConfig holds all configuration parsed from environment variables.
type runtimeConfig struct {
	PackFile        string
	PackJSON        string
	AgentName       string
	Port            int
	Protocol        string // "http", "a2a", "both", or "" (default = both)
	AWSRegion       string
	MemoryStore     string
	MemoryID        string
	A2AAuthMode     string
	A2AAuthRole     string
	PolicyEngineARN string
	MetricsConfig   string
	DashboardConfig string
	LogGroup        string
	OTLPEndpoint    string
	TracingEnabled  bool
	ToolSpecsJSON   string
	ToolGatewayURL  string
	ToolGatewayAuth string
	AgentEndpoints  map[string]string
	ProviderType    string
	Model           string

	// Providers holds the resolved provider bindings, one per capability
	// role. Populated from PROMPTPACK_PROVIDERS, or synthesized from the
	// legacy ProviderType/Model pair when that env var is absent.
	Providers []agentcore.ResolvedProvider
}

// primaryProvider returns the binding that serves as the runtime's main LLM,
// or nil if there is none. The adapter marks exactly one binding primary;
// the first llm-role binding is used as a fallback when nothing is flagged.
func (c *runtimeConfig) primaryProvider() *agentcore.ResolvedProvider {
	for i := range c.Providers {
		if c.Providers[i].Primary {
			return &c.Providers[i]
		}
	}
	for i := range c.Providers {
		if c.Providers[i].Role == agentcore.RoleLLM {
			return &c.Providers[i]
		}
	}
	return nil
}

// Protocol mode constants matching adapter-side values.
const (
	protocolHTTP = "http"
	protocolA2A  = "a2a"
	protocolBoth = "both"
)

// wantHTTPBridge returns true if the HTTP bridge should be started.
func (c *runtimeConfig) wantHTTPBridge() bool {
	return c.Protocol == "" || c.Protocol == protocolBoth || c.Protocol == protocolHTTP
}

// wantA2AServer returns true if the A2A server should be started.
func (c *runtimeConfig) wantA2AServer() bool {
	return c.Protocol == "" || c.Protocol == protocolBoth || c.Protocol == protocolA2A
}

// loadConfig reads configuration from environment variables.
// PROMPTPACK_FILE is required; all others have sensible defaults.
func loadConfig() (*runtimeConfig, error) {
	cfg := &runtimeConfig{
		PackFile:        os.Getenv(envPackFile),
		PackJSON:        os.Getenv(envPackJSON),
		AgentName:       os.Getenv(envAgentName),
		Protocol:        os.Getenv(envProtocol),
		AWSRegion:       os.Getenv(envAWSRegion),
		MemoryStore:     os.Getenv(envMemoryStore),
		MemoryID:        os.Getenv(envMemoryID),
		A2AAuthMode:     os.Getenv(envA2AAuthMode),
		A2AAuthRole:     os.Getenv(envA2AAuthRole),
		PolicyEngineARN: os.Getenv(envPolicyEngineARN),
		MetricsConfig:   os.Getenv(envMetricsConfig),
		DashboardConfig: os.Getenv(envDashboardConfig),
		LogGroup:        os.Getenv(envLogGroup),
		OTLPEndpoint:    os.Getenv(envOTLPEndpoint),
		ProviderType:    os.Getenv(envProviderType),
		Model:           os.Getenv(envProviderModel),
		ToolSpecsJSON:   os.Getenv(envToolSpecs),
		ToolGatewayURL:  os.Getenv(envToolGatewayURL),
		ToolGatewayAuth: os.Getenv(envToolGatewayAuth),
		Port:            defaultPort,
	}

	if cfg.PackFile == "" && cfg.PackJSON == "" {
		return nil, fmt.Errorf("%s or %s is required", envPackFile, envPackJSON)
	}

	if portStr := os.Getenv(envPort); portStr != "" {
		port, err := strconv.Atoi(portStr)
		if err != nil {
			return nil, fmt.Errorf("invalid %s %q: %w", envPort, portStr, err)
		}
		cfg.Port = port
	}

	if tracingStr := os.Getenv(envTracingEnabled); tracingStr != "" {
		enabled, err := strconv.ParseBool(tracingStr)
		if err != nil {
			return nil, fmt.Errorf("invalid %s %q: %w", envTracingEnabled, tracingStr, err)
		}
		cfg.TracingEnabled = enabled
	}

	if agentsJSON := os.Getenv(envAgentEndpoints); agentsJSON != "" {
		endpoints := make(map[string]string)
		if err := json.Unmarshal([]byte(agentsJSON), &endpoints); err != nil {
			return nil, fmt.Errorf("invalid %s JSON: %w", envAgentEndpoints, err)
		}
		cfg.AgentEndpoints = endpoints
	}

	providers, err := loadProviderBindings(cfg)
	if err != nil {
		return nil, err
	}
	cfg.Providers = providers

	return cfg, nil
}

// loadProviderBindings reads the provider bindings from PROMPTPACK_PROVIDERS.
// When that is unset it synthesizes a single primary LLM binding from the
// legacy PROMPTPACK_PROVIDER_TYPE/MODEL pair, so a runtime deployed by an
// older adapter keeps working.
func loadProviderBindings(cfg *runtimeConfig) ([]agentcore.ResolvedProvider, error) {
	if raw := os.Getenv(envProviders); raw != "" {
		var providers []agentcore.ResolvedProvider
		if err := json.Unmarshal([]byte(raw), &providers); err != nil {
			return nil, fmt.Errorf("invalid %s JSON: %w", envProviders, err)
		}
		return providers, nil
	}

	if cfg.ProviderType == "" && cfg.Model == "" {
		return nil, nil
	}
	return []agentcore.ResolvedProvider{{
		Name:    "default",
		Role:    agentcore.RoleLLM,
		Type:    cfg.ProviderType,
		Model:   cfg.Model,
		Primary: true,
	}}, nil
}
