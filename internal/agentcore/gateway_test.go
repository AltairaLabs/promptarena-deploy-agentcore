package agentcore

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/bedrockagentcorecontrol/types"
)

func TestResolveToolEndpoint(t *testing.T) {
	tests := []struct {
		name     string
		toolName string
		spec     *ArenaToolSpec
		want     string
	}{
		{
			name:     "nil spec falls back to placeholder",
			toolName: "search",
			spec:     nil,
			want:     "https://search.mcp.local",
		},
		{
			name:     "spec with no HTTPConfig falls back to placeholder",
			toolName: "calc",
			spec:     &ArenaToolSpec{Mode: "mock"},
			want:     "https://calc.mcp.local",
		},
		{
			name:     "spec with empty URL falls back to placeholder",
			toolName: "calc",
			spec:     &ArenaToolSpec{HTTPConfig: &ArenaHTTPConfig{Method: "POST"}},
			want:     "https://calc.mcp.local",
		},
		{
			name:     "spec with HTTP URL uses real endpoint",
			toolName: "search",
			spec: &ArenaToolSpec{
				HTTPConfig: &ArenaHTTPConfig{URL: "https://api.example.com/search"},
			},
			want: "https://api.example.com/search",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveToolEndpoint(tt.toolName, tt.spec)
			if got != tt.want {
				t.Errorf("resolveToolEndpoint(%q) = %q, want %q", tt.toolName, got, tt.want)
			}
		})
	}
}

func TestBuildTargetConfig(t *testing.T) {
	tests := []struct {
		name         string
		toolName     string
		cfg          *Config
		wantEndpoint string
	}{
		{
			name:         "nil ArenaConfig falls back to placeholder",
			toolName:     "search",
			cfg:          &Config{},
			wantEndpoint: "https://search.mcp.local",
		},
		{
			name:     "no matching tool spec falls back to placeholder",
			toolName: "unknown",
			cfg: &Config{
				ArenaConfig: &ArenaConfig{
					ToolSpecs: map[string]*ArenaToolSpec{
						"other": {HTTPConfig: &ArenaHTTPConfig{URL: "https://other.example.com"}},
					},
				},
			},
			wantEndpoint: "https://unknown.mcp.local",
		},
		{
			name:     "matching tool spec with HTTP URL uses real endpoint",
			toolName: "search",
			cfg: &Config{
				ArenaConfig: &ArenaConfig{
					ToolSpecs: map[string]*ArenaToolSpec{
						"search": {HTTPConfig: &ArenaHTTPConfig{URL: "https://api.example.com/search"}},
					},
				},
			},
			wantEndpoint: "https://api.example.com/search",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildTargetConfig(tt.toolName, tt.cfg)

			mcpTarget, ok := got.Value.(*types.McpTargetConfigurationMemberMcpServer)
			if !ok {
				t.Fatal("expected McpTargetConfigurationMemberMcpServer")
			}
			endpoint := *mcpTarget.Value.Endpoint
			if endpoint != tt.wantEndpoint {
				t.Errorf("endpoint = %q, want %q", endpoint, tt.wantEndpoint)
			}
		})
	}
}
