package agentcore

import (
	"encoding/json"
	"testing"
)

func TestBuildRuntimeEnvVars(t *testing.T) {
	tests := []struct {
		name string
		cfg  *Config
		want map[string]string
	}{
		{
			name: "empty config produces empty map",
			cfg:  &Config{},
			want: map[string]string{},
		},
		{
			name: "observability log group",
			cfg: &Config{
				Observability: &ObservabilityConfig{
					CloudWatchLogGroup: "/aws/agentcore/myapp",
				},
			},
			want: map[string]string{
				EnvLogGroup: "/aws/agentcore/myapp",
			},
		},
		{
			name: "observability tracing enabled",
			cfg: &Config{
				Observability: &ObservabilityConfig{
					TracingEnabled: true,
				},
			},
			want: map[string]string{
				EnvTracingEnabled: "true",
			},
		},
		{
			name: "observability both fields",
			cfg: &Config{
				Observability: &ObservabilityConfig{
					CloudWatchLogGroup: "/aws/agentcore/prod",
					TracingEnabled:     true,
				},
			},
			want: map[string]string{
				EnvLogGroup:       "/aws/agentcore/prod",
				EnvTracingEnabled: "true",
			},
		},
		{
			name: "tracing false is omitted",
			cfg: &Config{
				Observability: &ObservabilityConfig{
					TracingEnabled: false,
				},
			},
			want: map[string]string{},
		},
		{
			name: "memory store session",
			cfg: &Config{
				MemoryStore: "session",
			},
			want: map[string]string{
				EnvMemoryStore: "session",
			},
		},
		{
			name: "memory store persistent",
			cfg: &Config{
				MemoryStore: "persistent",
			},
			want: map[string]string{
				EnvMemoryStore: "persistent",
			},
		},
		{
			name: "combined observability and memory",
			cfg: &Config{
				MemoryStore: "session",
				Observability: &ObservabilityConfig{
					CloudWatchLogGroup: "/aws/logs",
					TracingEnabled:     true,
				},
			},
			want: map[string]string{
				EnvLogGroup:       "/aws/logs",
				EnvTracingEnabled: "true",
				EnvMemoryStore:    "session",
			},
		},
		{
			name: "a2a auth iam mode",
			cfg: &Config{
				RuntimeRoleARN: "arn:aws:iam::123456789012:role/test",
				A2AAuth:        &A2AAuthConfig{Mode: A2AAuthModeIAM},
			},
			want: map[string]string{
				EnvA2AAuthMode: "iam",
				EnvA2AAuthRole: "arn:aws:iam::123456789012:role/test",
			},
		},
		{
			name: "a2a auth jwt mode (no role)",
			cfg: &Config{
				A2AAuth: &A2AAuthConfig{
					Mode:         A2AAuthModeJWT,
					DiscoveryURL: "https://auth.example.com",
				},
			},
			want: map[string]string{
				EnvA2AAuthMode: "jwt",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildRuntimeEnvVars(tt.cfg)
			if len(got) != len(tt.want) {
				t.Fatalf("got %d env vars, want %d: got=%v want=%v",
					len(got), len(tt.want), got, tt.want)
			}
			for k, wantV := range tt.want {
				if gotV, ok := got[k]; !ok {
					t.Errorf("missing key %q", k)
				} else if gotV != wantV {
					t.Errorf("key %q = %q, want %q", k, gotV, wantV)
				}
			}
		})
	}
}

func TestBuildA2AEndpointMap(t *testing.T) {
	tests := []struct {
		name      string
		resources []ResourceState
		wantMap   map[string]string // nil means expect empty string
	}{
		{
			name:      "empty resources",
			resources: nil,
			wantMap:   nil,
		},
		{
			name: "only non-runtime resources ignored",
			resources: []ResourceState{
				{Type: ResTypeToolGateway, Name: "gw", ARN: "arn:gw", Status: "created"},
			},
			wantMap: nil,
		},
		{
			name: "failed runtimes excluded",
			resources: []ResourceState{
				{Type: ResTypeAgentRuntime, Name: "agent1", ARN: "", Status: "failed"},
			},
			wantMap: nil,
		},
		{
			name: "single created runtime",
			resources: []ResourceState{
				{Type: ResTypeAgentRuntime, Name: "worker", ARN: "arn:worker", Status: "created"},
			},
			wantMap: map[string]string{"worker": "arn:worker"},
		},
		{
			name: "multiple runtimes mixed status",
			resources: []ResourceState{
				{Type: ResTypeAgentRuntime, Name: "coord", ARN: "arn:coord", Status: "updated"},
				{Type: ResTypeAgentRuntime, Name: "worker", ARN: "arn:worker", Status: "created"},
				{Type: ResTypeAgentRuntime, Name: "broken", ARN: "", Status: "failed"},
				{Type: ResTypeToolGateway, Name: "gw", ARN: "arn:gw", Status: "created"},
			},
			wantMap: map[string]string{
				"coord":  "arn:coord",
				"worker": "arn:worker",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildA2AEndpointMap(tt.resources)
			if tt.wantMap == nil {
				if got != "" {
					t.Errorf("expected empty string, got %q", got)
				}
				return
			}

			var gotMap map[string]string
			if err := json.Unmarshal([]byte(got), &gotMap); err != nil {
				t.Fatalf("failed to parse JSON: %v (raw=%q)", err, got)
			}
			if len(gotMap) != len(tt.wantMap) {
				t.Fatalf("got %d entries, want %d", len(gotMap), len(tt.wantMap))
			}
			for k, wantV := range tt.wantMap {
				if gotMap[k] != wantV {
					t.Errorf("key %q = %q, want %q", k, gotMap[k], wantV)
				}
			}
		})
	}
}
