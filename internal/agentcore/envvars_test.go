package agentcore

import (
	"encoding/json"
	"testing"

	"github.com/AltairaLabs/PromptKit/runtime/evals"
	"github.com/AltairaLabs/PromptKit/runtime/prompt"
)

func TestBuildRuntimeEnvVars(t *testing.T) {
	tests := []struct {
		name string
		cfg  *Config
		want map[string]string
	}{
		{
			name: "empty config includes pack file",
			cfg:  &Config{},
			want: map[string]string{
				EnvPackFile: defaultPackPath,
			},
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
				EnvPackFile: defaultPackPath,
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
				EnvPackFile:       defaultPackPath,
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
				EnvPackFile:       defaultPackPath,
			},
		},
		{
			name: "tracing false is omitted",
			cfg: &Config{
				Observability: &ObservabilityConfig{
					TracingEnabled: false,
				},
			},
			want: map[string]string{
				EnvPackFile: defaultPackPath,
			},
		},
		{
			name: "memory episodic strategy",
			cfg: &Config{
				Memory: MemoryConfig{Strategies: []string{"episodic"}},
			},
			want: map[string]string{
				EnvMemoryStore: "episodic",
				EnvPackFile:    defaultPackPath,
			},
		},
		{
			name: "memory semantic strategy",
			cfg: &Config{
				Memory: MemoryConfig{Strategies: []string{"semantic"}},
			},
			want: map[string]string{
				EnvMemoryStore: "semantic",
				EnvPackFile:    defaultPackPath,
			},
		},
		{
			name: "combined observability and memory",
			cfg: &Config{
				Memory: MemoryConfig{Strategies: []string{"episodic"}},
				Observability: &ObservabilityConfig{
					CloudWatchLogGroup: "/aws/logs",
					TracingEnabled:     true,
				},
			},
			want: map[string]string{
				EnvLogGroup:       "/aws/logs",
				EnvTracingEnabled: "true",
				EnvMemoryStore:    "episodic",
				EnvPackFile:       defaultPackPath,
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
				EnvPackFile:    defaultPackPath,
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
				EnvPackFile:    defaultPackPath,
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

func TestInjectMetricsConfig(t *testing.T) {
	t.Run("sets env var when evals have metrics", func(t *testing.T) {
		cfg := &Config{
			RuntimeEnvVars: make(map[string]string),
		}
		pack := &prompt.Pack{
			ID: "test-pack",
			Evals: []evals.EvalDef{
				{
					ID: "accuracy",
					Metric: &evals.MetricDef{
						Name: "accuracy_score",
						Type: evals.MetricGauge,
					},
				},
			},
		}

		injectMetricsConfig(cfg, pack)

		raw, ok := cfg.RuntimeEnvVars[EnvMetricsConfig]
		if !ok {
			t.Fatal("expected PROMPTPACK_METRICS_CONFIG to be set")
		}

		var mc MetricsConfig
		if err := json.Unmarshal([]byte(raw), &mc); err != nil {
			t.Fatalf("failed to parse metrics config JSON: %v", err)
		}
		if mc.Namespace != metricsNamespace {
			t.Errorf("namespace = %q, want %q", mc.Namespace, metricsNamespace)
		}
		if len(mc.Metrics) != 1 {
			t.Fatalf("got %d metrics, want 1", len(mc.Metrics))
		}
		if mc.Metrics[0].MetricName != "accuracy_score" {
			t.Errorf("metric name = %q, want %q", mc.Metrics[0].MetricName, "accuracy_score")
		}
	})

	t.Run("no-op when no evals have metrics", func(t *testing.T) {
		cfg := &Config{
			RuntimeEnvVars: make(map[string]string),
		}
		pack := &prompt.Pack{
			ID: "empty-pack",
			Evals: []evals.EvalDef{
				{ID: "no-metric"},
			},
		}

		injectMetricsConfig(cfg, pack)

		if _, ok := cfg.RuntimeEnvVars[EnvMetricsConfig]; ok {
			t.Error("expected PROMPTPACK_METRICS_CONFIG to not be set")
		}
	})

	t.Run("no-op when evals slice is empty", func(t *testing.T) {
		cfg := &Config{
			RuntimeEnvVars: make(map[string]string),
		}
		pack := &prompt.Pack{ID: "no-evals"}

		injectMetricsConfig(cfg, pack)

		if _, ok := cfg.RuntimeEnvVars[EnvMetricsConfig]; ok {
			t.Error("expected PROMPTPACK_METRICS_CONFIG to not be set")
		}
	})
}

func TestInjectDashboardConfig(t *testing.T) {
	t.Run("sets env var for pack with evals", func(t *testing.T) {
		cfg := &Config{
			Region:         "us-west-2",
			RuntimeEnvVars: make(map[string]string),
		}
		pack := &prompt.Pack{
			ID: "dash-pack",
			Evals: []evals.EvalDef{
				{
					ID: "accuracy",
					Metric: &evals.MetricDef{
						Name: "accuracy_score",
						Type: evals.MetricGauge,
					},
				},
			},
		}

		injectDashboardConfig(cfg, pack)

		raw, ok := cfg.RuntimeEnvVars[EnvDashboardConfig]
		if !ok {
			t.Fatal("expected PROMPTPACK_DASHBOARD_CONFIG to be set")
		}

		var dc DashboardConfig
		if err := json.Unmarshal([]byte(raw), &dc); err != nil {
			t.Fatalf("failed to parse dashboard config JSON: %v", err)
		}
		if len(dc.Widgets) != 2 {
			t.Fatalf("got %d widgets, want 2 (1 agent + 1 eval)", len(dc.Widgets))
		}
		if dc.Widgets[0].Properties.Region != "us-west-2" {
			t.Errorf("region = %q, want %q", dc.Widgets[0].Properties.Region, "us-west-2")
		}
	})

	t.Run("sets env var for single agent pack without evals", func(t *testing.T) {
		cfg := &Config{
			Region:         "us-east-1",
			RuntimeEnvVars: make(map[string]string),
		}
		pack := &prompt.Pack{
			ID: "simple-agent",
			Prompts: map[string]*prompt.PackPrompt{
				"main": {},
			},
		}

		injectDashboardConfig(cfg, pack)

		raw, ok := cfg.RuntimeEnvVars[EnvDashboardConfig]
		if !ok {
			t.Fatal("expected PROMPTPACK_DASHBOARD_CONFIG to be set")
		}

		var dc DashboardConfig
		if err := json.Unmarshal([]byte(raw), &dc); err != nil {
			t.Fatalf("failed to parse dashboard config JSON: %v", err)
		}
		if len(dc.Widgets) != 1 {
			t.Fatalf("got %d widgets, want 1 (agent widget)", len(dc.Widgets))
		}
	})

	t.Run("pack with ID always produces dashboard with agent widget", func(t *testing.T) {
		cfg := &Config{
			Region:         "us-west-2",
			RuntimeEnvVars: make(map[string]string),
		}
		pack := &prompt.Pack{ID: "minimal"}

		injectDashboardConfig(cfg, pack)

		raw, ok := cfg.RuntimeEnvVars[EnvDashboardConfig]
		if !ok {
			t.Fatal("expected PROMPTPACK_DASHBOARD_CONFIG to be set")
		}
		var dc DashboardConfig
		if err := json.Unmarshal([]byte(raw), &dc); err != nil {
			t.Fatalf("failed to parse dashboard config JSON: %v", err)
		}
		if len(dc.Widgets) != 1 {
			t.Fatalf("got %d widgets, want 1", len(dc.Widgets))
		}
	})

	t.Run("multi-agent pack includes A2A widget", func(t *testing.T) {
		cfg := &Config{
			Region:         "us-west-2",
			RuntimeEnvVars: make(map[string]string),
		}
		pack := &prompt.Pack{
			ID: "multi",
			Agents: &prompt.AgentsConfig{
				Entry: "coord",
				Members: map[string]*prompt.AgentDef{
					"coord":  {},
					"worker": {},
				},
			},
			Prompts: map[string]*prompt.PackPrompt{
				"coord":  {},
				"worker": {},
			},
		}

		injectDashboardConfig(cfg, pack)

		raw, ok := cfg.RuntimeEnvVars[EnvDashboardConfig]
		if !ok {
			t.Fatal("expected PROMPTPACK_DASHBOARD_CONFIG to be set")
		}

		var dc DashboardConfig
		if err := json.Unmarshal([]byte(raw), &dc); err != nil {
			t.Fatalf("failed to parse dashboard config JSON: %v", err)
		}
		// 2 agent widgets + 1 A2A latency widget.
		if len(dc.Widgets) != 3 {
			t.Fatalf("got %d widgets, want 3", len(dc.Widgets))
		}
	})
}

func TestRuntimeEnvVarsForAgent(t *testing.T) {
	t.Run("adds agent name to runtime env vars", func(t *testing.T) {
		cfg := &Config{
			RuntimeEnvVars: map[string]string{
				EnvPackFile:    defaultPackPath,
				EnvMemoryStore: "episodic",
			},
		}

		env := runtimeEnvVarsForAgent(cfg, "worker")

		if env[EnvAgentName] != "worker" {
			t.Errorf("PROMPTPACK_AGENT = %q, want %q", env[EnvAgentName], "worker")
		}
		if env[EnvPackFile] != defaultPackPath {
			t.Errorf("PROMPTPACK_FILE = %q, want %q", env[EnvPackFile], defaultPackPath)
		}
		if env[EnvMemoryStore] != "episodic" {
			t.Errorf("PROMPTPACK_MEMORY_STORE = %q, want %q", env[EnvMemoryStore], "episodic")
		}
	})

	t.Run("does not mutate original map", func(t *testing.T) {
		orig := map[string]string{EnvPackFile: defaultPackPath}
		cfg := &Config{RuntimeEnvVars: orig}

		env := runtimeEnvVarsForAgent(cfg, "agent-a")

		if _, ok := orig[EnvAgentName]; ok {
			t.Error("runtimeEnvVarsForAgent mutated the original RuntimeEnvVars map")
		}
		if env[EnvAgentName] != "agent-a" {
			t.Errorf("PROMPTPACK_AGENT = %q, want %q", env[EnvAgentName], "agent-a")
		}
	})

	t.Run("different agents get different values", func(t *testing.T) {
		cfg := &Config{
			RuntimeEnvVars: map[string]string{EnvPackFile: defaultPackPath},
		}

		envA := runtimeEnvVarsForAgent(cfg, "coord")
		envB := runtimeEnvVarsForAgent(cfg, "worker")

		if envA[EnvAgentName] != "coord" {
			t.Errorf("agent A: PROMPTPACK_AGENT = %q, want %q", envA[EnvAgentName], "coord")
		}
		if envB[EnvAgentName] != "worker" {
			t.Errorf("agent B: PROMPTPACK_AGENT = %q, want %q", envB[EnvAgentName], "worker")
		}
	})
}
