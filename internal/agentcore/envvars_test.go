package agentcore

import "testing"

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
