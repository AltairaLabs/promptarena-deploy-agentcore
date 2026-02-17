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
