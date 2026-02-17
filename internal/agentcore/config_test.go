package agentcore

import (
	"testing"
)

func TestParseConfig(t *testing.T) {
	t.Run("valid minimal config", func(t *testing.T) {
		cfg, err := parseConfig(`{"region":"us-west-2","runtime_role_arn":"arn:aws:iam::123456789012:role/test"}`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.Region != "us-west-2" {
			t.Errorf("region = %q, want us-west-2", cfg.Region)
		}
		if cfg.RuntimeRoleARN != "arn:aws:iam::123456789012:role/test" {
			t.Errorf("runtime_role_arn = %q, want arn:aws:iam::123456789012:role/test", cfg.RuntimeRoleARN)
		}
	})

	t.Run("invalid JSON", func(t *testing.T) {
		_, err := parseConfig(`{bad json}`)
		if err == nil {
			t.Fatal("expected error for invalid JSON")
		}
	})

	t.Run("full config", func(t *testing.T) {
		raw := `{
			"region": "eu-west-1",
			"runtime_role_arn": "arn:aws:iam::999888777666:role/my-agent-role",
			"memory_store": "session",
			"tools": {"code_interpreter": true},
			"observability": {"cloudwatch_log_group": "/aws/agentcore/test", "tracing_enabled": true}
		}`
		cfg, err := parseConfig(raw)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.MemoryStore != "session" {
			t.Errorf("memory_store = %q, want session", cfg.MemoryStore)
		}
		if cfg.Tools == nil || !cfg.Tools.CodeInterpreter {
			t.Error("expected tools.code_interpreter = true")
		}
		if cfg.Observability == nil || !cfg.Observability.TracingEnabled {
			t.Error("expected observability.tracing_enabled = true")
		}
	})
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name     string
		cfg      Config
		wantErrs int
	}{
		{
			name: "valid minimal",
			cfg: Config{
				Region:         "us-west-2",
				RuntimeRoleARN: "arn:aws:iam::123456789012:role/test",
			},
			wantErrs: 0,
		},
		{
			name:     "missing everything",
			cfg:      Config{},
			wantErrs: 2,
		},
		{
			name: "bad region format",
			cfg: Config{
				Region:         "invalid",
				RuntimeRoleARN: "arn:aws:iam::123456789012:role/test",
			},
			wantErrs: 1,
		},
		{
			name: "bad role ARN",
			cfg: Config{
				Region:         "us-east-1",
				RuntimeRoleARN: "not-an-arn",
			},
			wantErrs: 1,
		},
		{
			name: "invalid memory store",
			cfg: Config{
				Region:         "us-west-2",
				RuntimeRoleARN: "arn:aws:iam::123456789012:role/test",
				MemoryStore:    "invalid",
			},
			wantErrs: 1,
		},
		{
			name: "valid with session memory",
			cfg: Config{
				Region:         "ap-southeast-1",
				RuntimeRoleARN: "arn:aws:iam::111222333444:role/agent",
				MemoryStore:    "persistent",
			},
			wantErrs: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := tt.cfg.validate()
			if len(errs) != tt.wantErrs {
				t.Errorf("got %d errors %v, want %d", len(errs), errs, tt.wantErrs)
			}
		})
	}
}

func TestValidateA2AAuth(t *testing.T) {
	base := Config{
		Region:         "us-west-2",
		RuntimeRoleARN: "arn:aws:iam::123456789012:role/test",
	}

	tests := []struct {
		name     string
		auth     *A2AAuthConfig
		wantErrs int
	}{
		{
			name:     "nil auth is valid",
			auth:     nil,
			wantErrs: 0,
		},
		{
			name:     "iam mode valid",
			auth:     &A2AAuthConfig{Mode: "iam"},
			wantErrs: 0,
		},
		{
			name: "jwt mode with discovery URL valid",
			auth: &A2AAuthConfig{
				Mode:         "jwt",
				DiscoveryURL: "https://login.example.com/.well-known/openid-configuration",
				AllowedAud:   []string{"my-app"},
			},
			wantErrs: 0,
		},
		{
			name:     "jwt mode missing discovery URL",
			auth:     &A2AAuthConfig{Mode: "jwt"},
			wantErrs: 1,
		},
		{
			name:     "empty mode",
			auth:     &A2AAuthConfig{},
			wantErrs: 1,
		},
		{
			name:     "invalid mode",
			auth:     &A2AAuthConfig{Mode: "oauth2"},
			wantErrs: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := base
			cfg.A2AAuth = tt.auth
			errs := cfg.validate()
			if len(errs) != tt.wantErrs {
				t.Errorf("got %d errors %v, want %d", len(errs), errs, tt.wantErrs)
			}
		})
	}
}

func TestParseConfig_DryRun(t *testing.T) {
	t.Run("dry_run true", func(t *testing.T) {
		cfg, err := parseConfig(`{"region":"us-west-2","runtime_role_arn":"arn:aws:iam::123456789012:role/test","dry_run":true}`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !cfg.DryRun {
			t.Error("expected dry_run = true")
		}
	})

	t.Run("dry_run false", func(t *testing.T) {
		cfg, err := parseConfig(`{"region":"us-west-2","runtime_role_arn":"arn:aws:iam::123456789012:role/test","dry_run":false}`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.DryRun {
			t.Error("expected dry_run = false")
		}
	})

	t.Run("dry_run omitted defaults to false", func(t *testing.T) {
		cfg, err := parseConfig(`{"region":"us-west-2","runtime_role_arn":"arn:aws:iam::123456789012:role/test"}`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.DryRun {
			t.Error("expected dry_run to default to false")
		}
	})
}

func TestParseConfig_A2AAuth(t *testing.T) {
	raw := `{
		"region": "us-west-2",
		"runtime_role_arn": "arn:aws:iam::123456789012:role/test",
		"a2a_auth": {
			"mode": "jwt",
			"discovery_url": "https://auth.example.com/.well-known/openid-configuration",
			"allowed_audience": ["aud1"],
			"allowed_clients": ["client1", "client2"]
		}
	}`
	cfg, err := parseConfig(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.A2AAuth == nil {
		t.Fatal("expected a2a_auth to be parsed")
	}
	if cfg.A2AAuth.Mode != "jwt" {
		t.Errorf("mode = %q, want jwt", cfg.A2AAuth.Mode)
	}
	if cfg.A2AAuth.DiscoveryURL == "" {
		t.Error("expected discovery_url to be set")
	}
	if len(cfg.A2AAuth.AllowedAud) != 1 {
		t.Errorf("expected 1 audience, got %d", len(cfg.A2AAuth.AllowedAud))
	}
	if len(cfg.A2AAuth.AllowedClts) != 2 {
		t.Errorf("expected 2 clients, got %d", len(cfg.A2AAuth.AllowedClts))
	}
}

func TestParseConfig_Tags(t *testing.T) {
	raw := `{
		"region": "us-west-2",
		"runtime_role_arn": "arn:aws:iam::123456789012:role/test",
		"tags": {
			"env": "production",
			"team": "platform",
			"cost-center": "12345"
		}
	}`
	cfg, err := parseConfig(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Tags) != 3 {
		t.Fatalf("expected 3 tags, got %d", len(cfg.Tags))
	}
	if cfg.Tags["env"] != "production" {
		t.Errorf("tags[env] = %q, want production", cfg.Tags["env"])
	}
	if cfg.Tags["team"] != "platform" {
		t.Errorf("tags[team] = %q, want platform", cfg.Tags["team"])
	}
	if cfg.Tags["cost-center"] != "12345" {
		t.Errorf("tags[cost-center] = %q, want 12345", cfg.Tags["cost-center"])
	}
}

func TestParseConfig_NoTags(t *testing.T) {
	raw := `{"region":"us-west-2","runtime_role_arn":"arn:aws:iam::123456789012:role/test"}`
	cfg, err := parseConfig(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Tags != nil {
		t.Errorf("expected nil tags, got %v", cfg.Tags)
	}
}

func TestValidateTags(t *testing.T) {
	tests := []struct {
		name     string
		tags     map[string]string
		wantErrs int
	}{
		{
			name:     "nil tags",
			tags:     nil,
			wantErrs: 0,
		},
		{
			name:     "empty tags",
			tags:     map[string]string{},
			wantErrs: 0,
		},
		{
			name:     "valid tags",
			tags:     map[string]string{"env": "prod", "team": "platform"},
			wantErrs: 0,
		},
		{
			name:     "empty key",
			tags:     map[string]string{"": "value"},
			wantErrs: 1,
		},
		{
			name:     "key too long",
			tags:     map[string]string{string(make([]byte, maxTagKeyLen+1)): "v"},
			wantErrs: 1,
		},
		{
			name:     "value too long",
			tags:     map[string]string{"k": string(make([]byte, maxTagValueLen+1))},
			wantErrs: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := validateTags(tt.tags)
			if len(errs) != tt.wantErrs {
				t.Errorf("got %d errors %v, want %d", len(errs), errs, tt.wantErrs)
			}
		})
	}
}

func TestValidate_WithValidTags(t *testing.T) {
	cfg := Config{
		Region:         "us-west-2",
		RuntimeRoleARN: "arn:aws:iam::123456789012:role/test",
		Tags:           map[string]string{"env": "prod"},
	}
	errs := cfg.validate()
	if len(errs) != 0 {
		t.Errorf("expected 0 errors, got %d: %v", len(errs), errs)
	}
}

func TestValidate_WithInvalidTags(t *testing.T) {
	cfg := Config{
		Region:         "us-west-2",
		RuntimeRoleARN: "arn:aws:iam::123456789012:role/test",
		Tags:           map[string]string{"": "no-key"},
	}
	errs := cfg.validate()
	if len(errs) != 1 {
		t.Errorf("expected 1 error, got %d: %v", len(errs), errs)
	}
}

func TestContainerImageForAgent(t *testing.T) {
	tests := []struct {
		name      string
		cfg       Config
		agentName string
		want      string
	}{
		{
			name:      "default when nothing set",
			cfg:       Config{},
			agentName: "worker",
			want:      DefaultContainerImage,
		},
		{
			name: "global container_image",
			cfg: Config{
				ContainerImage: "my-registry.io/custom:v1",
			},
			agentName: "worker",
			want:      "my-registry.io/custom:v1",
		},
		{
			name: "per-agent override",
			cfg: Config{
				ContainerImage: "my-registry.io/custom:v1",
				AgentOverrides: map[string]*AgentOverride{
					"worker": {ContainerImage: "my-registry.io/worker:v2"},
				},
			},
			agentName: "worker",
			want:      "my-registry.io/worker:v2",
		},
		{
			name: "per-agent override takes precedence over global",
			cfg: Config{
				ContainerImage: "my-registry.io/global:v1",
				AgentOverrides: map[string]*AgentOverride{
					"agent-a": {ContainerImage: "my-registry.io/agent-a:v3"},
				},
			},
			agentName: "agent-a",
			want:      "my-registry.io/agent-a:v3",
		},
		{
			name: "agent not in overrides falls back to global",
			cfg: Config{
				ContainerImage: "my-registry.io/global:v1",
				AgentOverrides: map[string]*AgentOverride{
					"other": {ContainerImage: "my-registry.io/other:v2"},
				},
			},
			agentName: "worker",
			want:      "my-registry.io/global:v1",
		},
		{
			name: "override with empty image falls back to global",
			cfg: Config{
				ContainerImage: "my-registry.io/global:v1",
				AgentOverrides: map[string]*AgentOverride{
					"worker": {ContainerImage: ""},
				},
			},
			agentName: "worker",
			want:      "my-registry.io/global:v1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.cfg.containerImageForAgent(tt.agentName)
			if got != tt.want {
				t.Errorf("containerImageForAgent(%q) = %q, want %q", tt.agentName, got, tt.want)
			}
		})
	}
}

func TestValidateContainerImage(t *testing.T) {
	tests := []struct {
		name     string
		image    string
		wantErrs int
	}{
		{name: "empty is valid", image: "", wantErrs: 0},
		{name: "valid image", image: "ghcr.io/org/image:latest", wantErrs: 0},
		{name: "no slash", image: "justanimage", wantErrs: 1},
		{name: "whitespace", image: "my-registry.io/image name", wantErrs: 1},
		{name: "tab", image: "my-registry.io/image\tname", wantErrs: 1},
		{name: "no slash and whitespace", image: "bad image", wantErrs: 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := validateContainerImage(tt.image)
			if len(errs) != tt.wantErrs {
				t.Errorf("got %d errors %v, want %d", len(errs), errs, tt.wantErrs)
			}
		})
	}
}

func TestValidate_ContainerImageIntegration(t *testing.T) {
	cfg := Config{
		Region:         "us-west-2",
		RuntimeRoleARN: "arn:aws:iam::123456789012:role/test",
		ContainerImage: "badimage",
	}
	errs := cfg.validate()
	if len(errs) != 1 {
		t.Errorf("expected 1 error for bad container_image, got %d: %v", len(errs), errs)
	}
}

func TestValidate_AgentOverrideContainerImage(t *testing.T) {
	cfg := Config{
		Region:         "us-west-2",
		RuntimeRoleARN: "arn:aws:iam::123456789012:role/test",
		AgentOverrides: map[string]*AgentOverride{
			"worker": {ContainerImage: "noslash"},
		},
	}
	errs := cfg.validate()
	if len(errs) != 1 {
		t.Errorf("expected 1 error for bad agent override image, got %d: %v", len(errs), errs)
	}
}

func TestParseConfig_ContainerImageAndOverrides(t *testing.T) {
	raw := `{
		"region": "us-west-2",
		"runtime_role_arn": "arn:aws:iam::123456789012:role/test",
		"container_image": "my-registry.io/custom:v1",
		"agent_overrides": {
			"worker": {"container_image": "my-registry.io/worker:v2"}
		}
	}`
	cfg, err := parseConfig(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.ContainerImage != "my-registry.io/custom:v1" {
		t.Errorf("container_image = %q, want my-registry.io/custom:v1", cfg.ContainerImage)
	}
	if cfg.AgentOverrides == nil {
		t.Fatal("expected agent_overrides to be parsed")
	}
	if cfg.AgentOverrides["worker"] == nil {
		t.Fatal("expected agent_overrides[worker] to be parsed")
	}
	if cfg.AgentOverrides["worker"].ContainerImage != "my-registry.io/worker:v2" {
		t.Errorf("agent_overrides[worker].container_image = %q, want my-registry.io/worker:v2",
			cfg.AgentOverrides["worker"].ContainerImage)
	}
}
