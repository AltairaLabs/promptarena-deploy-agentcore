// Package main implements the AgentCore runtime entrypoint binary.
// It loads a compiled .pack.json and serves it as an A2A HTTP agent.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
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
	envTracingEnabled  = "OTEL_TRACING_ENABLED"
	envAgentEndpoints  = "PROMPTPACK_AGENTS"
)

const defaultPort = 9000

// runtimeConfig holds all configuration parsed from environment variables.
type runtimeConfig struct {
	PackFile        string
	PackJSON        string
	AgentName       string
	Port            int
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
	AgentEndpoints  map[string]string
}

// loadConfig reads configuration from environment variables.
// PROMPTPACK_FILE is required; all others have sensible defaults.
func loadConfig() (*runtimeConfig, error) {
	cfg := &runtimeConfig{
		PackFile:        os.Getenv(envPackFile),
		PackJSON:        os.Getenv(envPackJSON),
		AgentName:       os.Getenv(envAgentName),
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

	return cfg, nil
}
