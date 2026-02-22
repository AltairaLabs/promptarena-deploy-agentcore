package main

import (
	"testing"
)

func TestLoadConfig_RequiresPack(t *testing.T) {
	t.Setenv(envPackFile, "")
	t.Setenv(envPackJSON, "")
	_, err := loadConfig()
	if err == nil {
		t.Fatal("expected error when both PROMPTPACK_FILE and PROMPTPACK_PACK_JSON are empty")
	}
}

func TestLoadConfig_PackJSONOnly(t *testing.T) {
	t.Setenv(envPackFile, "")
	t.Setenv(envPackJSON, `{"id":"test","prompts":{}}`)
	t.Setenv(envPort, "")
	t.Setenv(envTracingEnabled, "")
	t.Setenv(envAgentEndpoints, "")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.PackFile != "" {
		t.Errorf("PackFile = %q, want empty", cfg.PackFile)
	}
	if cfg.PackJSON != `{"id":"test","prompts":{}}` {
		t.Errorf("PackJSON = %q, want pack JSON", cfg.PackJSON)
	}
}

func TestLoadConfig_BothPackFileAndPackJSON(t *testing.T) {
	t.Setenv(envPackFile, "explicit.pack.json")
	t.Setenv(envPackJSON, `{"id":"test"}`)
	t.Setenv(envPort, "")
	t.Setenv(envTracingEnabled, "")
	t.Setenv(envAgentEndpoints, "")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.PackFile != "explicit.pack.json" {
		t.Errorf("PackFile = %q, want %q", cfg.PackFile, "explicit.pack.json")
	}
	if cfg.PackJSON != `{"id":"test"}` {
		t.Errorf("PackJSON = %q, want pack JSON", cfg.PackJSON)
	}
}

func TestLoadConfig_Defaults(t *testing.T) {
	t.Setenv(envPackFile, "test.pack.json")
	t.Setenv(envPackJSON, "")
	// Clear optional vars
	t.Setenv(envPort, "")
	t.Setenv(envTracingEnabled, "")
	t.Setenv(envAgentEndpoints, "")
	t.Setenv(envAgentName, "")
	t.Setenv(envAWSRegion, "")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.PackFile != "test.pack.json" {
		t.Errorf("PackFile = %q, want %q", cfg.PackFile, "test.pack.json")
	}
	if cfg.Port != defaultPort {
		t.Errorf("Port = %d, want %d", cfg.Port, defaultPort)
	}
	if cfg.TracingEnabled {
		t.Error("TracingEnabled should default to false")
	}
	if cfg.AgentEndpoints != nil {
		t.Errorf("AgentEndpoints should be nil by default, got %v", cfg.AgentEndpoints)
	}
}

func TestLoadConfig_InvalidPort(t *testing.T) {
	t.Setenv(envPackFile, "test.pack.json")
	t.Setenv(envPort, "not-a-number")

	_, err := loadConfig()
	if err == nil {
		t.Fatal("expected error for invalid port")
	}
}

func TestLoadConfig_InvalidTracingBool(t *testing.T) {
	t.Setenv(envPackFile, "test.pack.json")
	t.Setenv(envPort, "")
	t.Setenv(envTracingEnabled, "maybe")

	_, err := loadConfig()
	if err == nil {
		t.Fatal("expected error for invalid tracing bool")
	}
}

func TestLoadConfig_InvalidAgentsJSON(t *testing.T) {
	t.Setenv(envPackFile, "test.pack.json")
	t.Setenv(envPort, "")
	t.Setenv(envTracingEnabled, "")
	t.Setenv(envAgentEndpoints, "not-json")

	_, err := loadConfig()
	if err == nil {
		t.Fatal("expected error for invalid agents JSON")
	}
}

func TestLoadConfig_ValidAgentsJSON(t *testing.T) {
	t.Setenv(envPackFile, "test.pack.json")
	t.Setenv(envPort, "")
	t.Setenv(envTracingEnabled, "")
	t.Setenv(envAgentEndpoints, `{"agent1":"http://a1:9000","agent2":"http://a2:9001"}`)

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(cfg.AgentEndpoints) != 2 {
		t.Fatalf("expected 2 endpoints, got %d", len(cfg.AgentEndpoints))
	}
	if cfg.AgentEndpoints["agent1"] != "http://a1:9000" {
		t.Errorf("agent1 = %q, want %q", cfg.AgentEndpoints["agent1"], "http://a1:9000")
	}
}

func TestLoadConfig_CustomPort(t *testing.T) {
	t.Setenv(envPackFile, "test.pack.json")
	t.Setenv(envPort, "8080")
	t.Setenv(envTracingEnabled, "")
	t.Setenv(envAgentEndpoints, "")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Port != 8080 {
		t.Errorf("Port = %d, want 8080", cfg.Port)
	}
}

func TestLoadConfig_TracingEnabled(t *testing.T) {
	t.Setenv(envPackFile, "test.pack.json")
	t.Setenv(envPort, "")
	t.Setenv(envTracingEnabled, "true")
	t.Setenv(envAgentEndpoints, "")
	t.Setenv(envOTLPEndpoint, "http://collector:4318")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !cfg.TracingEnabled {
		t.Error("TracingEnabled should be true")
	}
	if cfg.OTLPEndpoint != "http://collector:4318" {
		t.Errorf("OTLPEndpoint = %q, want %q", cfg.OTLPEndpoint, "http://collector:4318")
	}
}

func TestLoadConfig_AllEnvVars(t *testing.T) {
	t.Setenv(envPackFile, "my.pack.json")
	t.Setenv(envAgentName, "myagent")
	t.Setenv(envPort, "3000")
	t.Setenv(envProtocol, "a2a")
	t.Setenv(envAWSRegion, "us-east-1")
	t.Setenv(envMemoryStore, "dynamodb")
	t.Setenv(envMemoryID, "mem-123")
	t.Setenv(envA2AAuthMode, "iam")
	t.Setenv(envA2AAuthRole, "arn:aws:iam::123:role/test")
	t.Setenv(envPolicyEngineARN, "arn:aws:cedar:policy")
	t.Setenv(envMetricsConfig, "metrics.json")
	t.Setenv(envDashboardConfig, "dash.json")
	t.Setenv(envLogGroup, "/aws/agentcore/myagent")
	t.Setenv(envOTLPEndpoint, "http://otel:4318")
	t.Setenv(envTracingEnabled, "true")
	t.Setenv(envAgentEndpoints, `{"sub":"http://sub:9000"}`)

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.AgentName != "myagent" {
		t.Errorf("AgentName = %q, want %q", cfg.AgentName, "myagent")
	}
	if cfg.Port != 3000 {
		t.Errorf("Port = %d, want 3000", cfg.Port)
	}
	if cfg.Protocol != "a2a" {
		t.Errorf("Protocol = %q, want %q", cfg.Protocol, "a2a")
	}
	if cfg.AWSRegion != "us-east-1" {
		t.Errorf("AWSRegion = %q, want %q", cfg.AWSRegion, "us-east-1")
	}
	if cfg.MemoryStore != "dynamodb" {
		t.Errorf("MemoryStore = %q, want %q", cfg.MemoryStore, "dynamodb")
	}
	if cfg.A2AAuthMode != "iam" {
		t.Errorf("A2AAuthMode = %q, want %q", cfg.A2AAuthMode, "iam")
	}
	if cfg.LogGroup != "/aws/agentcore/myagent" {
		t.Errorf("LogGroup = %q, want %q", cfg.LogGroup, "/aws/agentcore/myagent")
	}
}

func TestWantHTTPBridge(t *testing.T) {
	tests := []struct {
		protocol string
		want     bool
	}{
		{"", true},
		{"both", true},
		{"http", true},
		{"a2a", false},
	}
	for _, tt := range tests {
		t.Run(tt.protocol, func(t *testing.T) {
			cfg := &runtimeConfig{Protocol: tt.protocol}
			if got := cfg.wantHTTPBridge(); got != tt.want {
				t.Errorf("wantHTTPBridge(%q) = %v, want %v",
					tt.protocol, got, tt.want)
			}
		})
	}
}

func TestWantA2AServer(t *testing.T) {
	tests := []struct {
		protocol string
		want     bool
	}{
		{"", true},
		{"both", true},
		{"a2a", true},
		{"http", false},
	}
	for _, tt := range tests {
		t.Run(tt.protocol, func(t *testing.T) {
			cfg := &runtimeConfig{Protocol: tt.protocol}
			if got := cfg.wantA2AServer(); got != tt.want {
				t.Errorf("wantA2AServer(%q) = %v, want %v",
					tt.protocol, got, tt.want)
			}
		})
	}
}
