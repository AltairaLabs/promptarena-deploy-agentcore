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
