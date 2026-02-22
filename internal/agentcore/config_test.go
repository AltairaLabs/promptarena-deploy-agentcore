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
		if !cfg.HasMemory() {
			t.Error("expected memory to be configured")
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
				Region:            "us-west-2",
				RuntimeRoleARN:    "arn:aws:iam::123456789012:role/test",
				RuntimeBinaryPath: "/path/to/binary",
			},
			wantErrs: 0,
		},
		{
			name:     "missing everything",
			cfg:      Config{},
			wantErrs: 3, // region, runtime_role_arn, runtime_binary_path
		},
		{
			name: "bad region format",
			cfg: Config{
				Region:            "invalid",
				RuntimeRoleARN:    "arn:aws:iam::123456789012:role/test",
				RuntimeBinaryPath: "/path/to/binary",
			},
			wantErrs: 1,
		},
		{
			name: "bad role ARN",
			cfg: Config{
				Region:            "us-east-1",
				RuntimeRoleARN:    "not-an-arn",
				RuntimeBinaryPath: "/path/to/binary",
			},
			wantErrs: 1,
		},
		{
			name: "invalid memory store",
			cfg: Config{
				Region:            "us-west-2",
				RuntimeRoleARN:    "arn:aws:iam::123456789012:role/test",
				RuntimeBinaryPath: "/path/to/binary",
				Memory:            MemoryConfig{Strategies: []string{"invalid"}},
			},
			wantErrs: 1,
		},
		{
			name: "valid with session memory",
			cfg: Config{
				Region:            "ap-southeast-1",
				RuntimeRoleARN:    "arn:aws:iam::111222333444:role/agent",
				RuntimeBinaryPath: "/path/to/binary",
				Memory:            MemoryConfig{Strategies: []string{"semantic"}},
			},
			wantErrs: 0,
		},
		{
			name: "missing runtime_binary_path",
			cfg: Config{
				Region:         "us-west-2",
				RuntimeRoleARN: "arn:aws:iam::123456789012:role/test",
			},
			wantErrs: 1,
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
		Region:            "us-west-2",
		RuntimeRoleARN:    "arn:aws:iam::123456789012:role/test",
		RuntimeBinaryPath: "/path/to/binary",
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
		Region:            "us-west-2",
		RuntimeRoleARN:    "arn:aws:iam::123456789012:role/test",
		RuntimeBinaryPath: "/path/to/binary",
		Tags:              map[string]string{"env": "prod"},
	}
	errs := cfg.validate()
	if len(errs) != 0 {
		t.Errorf("expected 0 errors, got %d: %v", len(errs), errs)
	}
}

func TestValidate_WithInvalidTags(t *testing.T) {
	cfg := Config{
		Region:            "us-west-2",
		RuntimeRoleARN:    "arn:aws:iam::123456789012:role/test",
		RuntimeBinaryPath: "/path/to/binary",
		Tags:              map[string]string{"": "no-key"},
	}
	errs := cfg.validate()
	if len(errs) != 1 {
		t.Errorf("expected 1 error, got %d: %v", len(errs), errs)
	}
}

func TestExtractAccountFromARN(t *testing.T) {
	tests := []struct {
		name string
		arn  string
		want string
	}{
		{
			name: "valid role ARN",
			arn:  "arn:aws:iam::123456789012:role/my-role",
			want: "123456789012",
		},
		{
			name: "valid ARN different account",
			arn:  "arn:aws:iam::999888777666:role/agent",
			want: "999888777666",
		},
		{
			name: "malformed ARN too few parts",
			arn:  "arn:aws:iam",
			want: "",
		},
		{
			name: "empty string",
			arn:  "",
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractAccountFromARN(tt.arn)
			if got != tt.want {
				t.Errorf("extractAccountFromARN(%q) = %q, want %q", tt.arn, got, tt.want)
			}
		})
	}
}

// --- Memory config expansion tests ---

func TestParseConfig_MemoryStore_StringForm(t *testing.T) {
	tests := []struct {
		name       string
		memVal     string
		wantStrats []string
		wantErr    bool
	}{
		{"episodic", "episodic", []string{"episodic"}, false},
		{"semantic", "semantic", []string{"semantic"}, false},
		{"summary", "summary", []string{"summary"}, false},
		{"user_preference", "user_preference", []string{"user_preference"}, false},
		{"alias session", "session", []string{"episodic"}, false},
		{"alias persistent", "persistent", []string{"semantic"}, false},
		{"invalid strategy", "bogus", nil, true},
		{"empty string", "", nil, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := `{"region":"us-west-2",` +
				`"runtime_role_arn":"arn:aws:iam::123456789012:role/t",` +
				`"memory_store":"` + tt.memVal + `"}`
			cfg, err := parseConfig(raw)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(cfg.Memory.Strategies) != len(tt.wantStrats) {
				t.Fatalf("strategies = %v, want %v",
					cfg.Memory.Strategies, tt.wantStrats)
			}
			for i, s := range tt.wantStrats {
				if cfg.Memory.Strategies[i] != s {
					t.Errorf("strategy[%d] = %q, want %q",
						i, cfg.Memory.Strategies[i], s)
				}
			}
		})
	}
}

func TestParseConfig_MemoryStore_ArrayForm(t *testing.T) {
	raw := `{
		"region":"us-west-2",
		"runtime_role_arn":"arn:aws:iam::123456789012:role/t",
		"memory_store":["episodic","semantic","summary"]
	}`
	cfg, err := parseConfig(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"episodic", "semantic", "summary"}
	if len(cfg.Memory.Strategies) != len(want) {
		t.Fatalf("strategies = %v, want %v", cfg.Memory.Strategies, want)
	}
	for i, s := range want {
		if cfg.Memory.Strategies[i] != s {
			t.Errorf("strategy[%d] = %q, want %q",
				i, cfg.Memory.Strategies[i], s)
		}
	}
}

func TestParseConfig_MemoryStore_ArrayWithAliases(t *testing.T) {
	raw := `{
		"region":"us-west-2",
		"runtime_role_arn":"arn:aws:iam::123456789012:role/t",
		"memory_store":["session","persistent"]
	}`
	cfg, err := parseConfig(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"episodic", "semantic"}
	if len(cfg.Memory.Strategies) != len(want) {
		t.Fatalf("strategies = %v, want %v", cfg.Memory.Strategies, want)
	}
	for i, s := range want {
		if cfg.Memory.Strategies[i] != s {
			t.Errorf("strategy[%d] = %q, want %q",
				i, cfg.Memory.Strategies[i], s)
		}
	}
}

func TestParseConfig_MemoryStore_ArrayDeduplicates(t *testing.T) {
	raw := `{
		"region":"us-west-2",
		"runtime_role_arn":"arn:aws:iam::123456789012:role/t",
		"memory_store":["session","episodic"]
	}`
	cfg, err := parseConfig(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Memory.Strategies) != 1 {
		t.Fatalf("expected 1 strategy after dedup, got %v",
			cfg.Memory.Strategies)
	}
	if cfg.Memory.Strategies[0] != "episodic" {
		t.Errorf("strategy = %q, want episodic", cfg.Memory.Strategies[0])
	}
}

func TestParseConfig_MemoryStore_ObjectForm(t *testing.T) {
	raw := `{
		"region":"us-west-2",
		"runtime_role_arn":"arn:aws:iam::123456789012:role/t",
		"memory_store":{
			"strategies":["episodic","semantic"],
			"event_expiry_days":90,
			"encryption_key_arn":"arn:aws:kms:us-west-2:123456789012:key/abc"
		}
	}`
	cfg, err := parseConfig(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Memory.Strategies) != 2 {
		t.Fatalf("strategies = %v, want [episodic semantic]",
			cfg.Memory.Strategies)
	}
	if cfg.Memory.EventExpiryDays != 90 {
		t.Errorf("event_expiry_days = %d, want 90",
			cfg.Memory.EventExpiryDays)
	}
	if cfg.Memory.EncryptionKeyARN !=
		"arn:aws:kms:us-west-2:123456789012:key/abc" {
		t.Errorf("encryption_key_arn = %q", cfg.Memory.EncryptionKeyARN)
	}
}

func TestParseConfig_MemoryStore_ObjectWithAliases(t *testing.T) {
	raw := `{
		"region":"us-west-2",
		"runtime_role_arn":"arn:aws:iam::123456789012:role/t",
		"memory_store":{"strategies":["session","persistent"]}
	}`
	cfg, err := parseConfig(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"episodic", "semantic"}
	if len(cfg.Memory.Strategies) != len(want) {
		t.Fatalf("strategies = %v, want %v", cfg.Memory.Strategies, want)
	}
}

func TestParseConfig_MemoryStore_InvalidInArray(t *testing.T) {
	raw := `{
		"region":"us-west-2",
		"runtime_role_arn":"arn:aws:iam::123456789012:role/t",
		"memory_store":["episodic","bogus"]
	}`
	_, err := parseConfig(raw)
	if err == nil {
		t.Fatal("expected error for invalid strategy in array")
	}
}

func TestHasMemory(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
		want bool
	}{
		{"no memory", Config{}, false},
		{"empty strategies", Config{Memory: MemoryConfig{}}, false},
		{
			"with strategies",
			Config{Memory: MemoryConfig{Strategies: []string{"episodic"}}},
			true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.HasMemory(); got != tt.want {
				t.Errorf("HasMemory() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMemoryStrategiesCSV(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
		want string
	}{
		{"empty", Config{}, ""},
		{
			"single",
			Config{Memory: MemoryConfig{Strategies: []string{"episodic"}}},
			"episodic",
		},
		{
			"multiple",
			Config{
				Memory: MemoryConfig{
					Strategies: []string{"episodic", "semantic", "summary"},
				},
			},
			"episodic,semantic,summary",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.MemoryStrategiesCSV(); got != tt.want {
				t.Errorf("MemoryStrategiesCSV() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestValidateMemory_ExpiryRange(t *testing.T) {
	tests := []struct {
		name     string
		mem      MemoryConfig
		wantErrs int
	}{
		{
			"valid expiry",
			MemoryConfig{Strategies: []string{"episodic"}, EventExpiryDays: 30},
			0,
		},
		{
			"min boundary",
			MemoryConfig{Strategies: []string{"episodic"}, EventExpiryDays: 3},
			0,
		},
		{
			"max boundary",
			MemoryConfig{Strategies: []string{"episodic"}, EventExpiryDays: 365},
			0,
		},
		{
			"below min",
			MemoryConfig{Strategies: []string{"episodic"}, EventExpiryDays: 2},
			1,
		},
		{
			"above max",
			MemoryConfig{Strategies: []string{"episodic"}, EventExpiryDays: 366},
			1,
		},
		{
			"zero is valid (means default)",
			MemoryConfig{Strategies: []string{"episodic"}, EventExpiryDays: 0},
			0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := validateMemory(&tt.mem)
			if len(errs) != tt.wantErrs {
				t.Errorf("got %d errors %v, want %d",
					len(errs), errs, tt.wantErrs)
			}
		})
	}
}

func TestValidateMemory_EncryptionKeyARN(t *testing.T) {
	tests := []struct {
		name     string
		arn      string
		wantErrs int
	}{
		{"empty is valid", "", 0},
		{
			"valid KMS ARN",
			"arn:aws:kms:us-west-2:123456789012:key/abc-123",
			0,
		},
		{"invalid ARN", "not-an-arn", 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mem := MemoryConfig{
				Strategies:       []string{"episodic"},
				EncryptionKeyARN: tt.arn,
			}
			errs := validateMemory(&mem)
			if len(errs) != tt.wantErrs {
				t.Errorf("got %d errors %v, want %d",
					len(errs), errs, tt.wantErrs)
			}
		})
	}
}

func TestValidateMemory_NoStrategies(t *testing.T) {
	mem := MemoryConfig{}
	errs := validateMemory(&mem)
	if len(errs) != 0 {
		t.Errorf("expected 0 errors for empty strategies, got %d: %v",
			len(errs), errs)
	}
}

func TestResolveAlias(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"session", "episodic"},
		{"persistent", "semantic"},
		{"episodic", "episodic"},
		{"semantic", "semantic"},
		{"summary", "summary"},
		{"user_preference", "user_preference"},
		{"unknown", "unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := resolveAlias(tt.input); got != tt.want {
				t.Errorf("resolveAlias(%q) = %q, want %q",
					tt.input, got, tt.want)
			}
		})
	}
}

func TestParseConfig_MemoryStore_NullOmitted(t *testing.T) {
	raw := `{
		"region":"us-west-2",
		"runtime_role_arn":"arn:aws:iam::123456789012:role/t",
		"memory_store":null
	}`
	cfg, err := parseConfig(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.HasMemory() {
		t.Error("expected no memory when memory_store is null")
	}
}

func TestValidate_Protocol(t *testing.T) {
	base := Config{
		Region:            "us-west-2",
		RuntimeRoleARN:    "arn:aws:iam::123456789012:role/test",
		RuntimeBinaryPath: "/path/to/binary",
	}

	t.Run("empty protocol is valid", func(t *testing.T) {
		cfg := base
		errs := cfg.validate()
		for _, e := range errs {
			if contains(e, "protocol") {
				t.Errorf("unexpected protocol error: %s", e)
			}
		}
	})

	for _, proto := range []string{"http", "a2a", "both"} {
		t.Run("valid "+proto, func(t *testing.T) {
			cfg := base
			cfg.Protocol = proto
			errs := cfg.validate()
			for _, e := range errs {
				if contains(e, "protocol") {
					t.Errorf("unexpected protocol error: %s", e)
				}
			}
		})
	}

	t.Run("invalid protocol", func(t *testing.T) {
		cfg := base
		cfg.Protocol = "grpc"
		errs := cfg.validate()
		found := false
		for _, e := range errs {
			if contains(e, "protocol") {
				found = true
			}
		}
		if !found {
			t.Error("expected protocol validation error")
		}
	})
}

func TestResolveServerProtocol(t *testing.T) {
	tests := []struct {
		protocol string
		want     string
	}{
		{"", ""},
		{"http", "HTTP"},
		{"a2a", "A2A"},
		{"both", "HTTP"},
	}
	for _, tt := range tests {
		t.Run(tt.protocol, func(t *testing.T) {
			cfg := &Config{Protocol: tt.protocol}
			if got := resolveServerProtocol(cfg); got != tt.want {
				t.Errorf("resolveServerProtocol(%q) = %q, want %q",
					tt.protocol, got, tt.want)
			}
		})
	}
}

func TestParseConfig_WithProtocol(t *testing.T) {
	raw := `{
		"region":"us-west-2",
		"runtime_role_arn":"arn:aws:iam::123456789012:role/t",
		"runtime_binary_path":"/bin/rt",
		"protocol":"a2a"
	}`
	cfg, err := parseConfig(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Protocol != "a2a" {
		t.Errorf("Protocol = %q, want a2a", cfg.Protocol)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
