package agentcore

import (
	"strings"
	"testing"
)

func TestDiagnoseConfig_SupportedRegion(t *testing.T) {
	cfg := &Config{
		Region:         "us-west-2",
		RuntimeRoleARN: "arn:aws:iam::123456789012:role/test",
	}
	warnings := DiagnoseConfig(cfg)
	for _, w := range warnings {
		if w.Category == ErrCategoryConfiguration && strings.Contains(w.Message, "region") {
			t.Errorf("unexpected region warning for supported region: %s", w.Message)
		}
	}
}

func TestDiagnoseConfig_UnsupportedRegion(t *testing.T) {
	cfg := &Config{
		Region:         "ap-southeast-1",
		RuntimeRoleARN: "arn:aws:iam::123456789012:role/test",
	}
	warnings := DiagnoseConfig(cfg)
	found := false
	for _, w := range warnings {
		if strings.Contains(w.Message, "may not support Bedrock AgentCore") {
			found = true
			if !strings.Contains(w.Hint, "us-west-2") {
				t.Errorf("expected hint to include supported region, got %q", w.Hint)
			}
		}
	}
	if !found {
		t.Error("expected a warning about unsupported region")
	}
}

func TestDiagnoseConfig_EmptyRegion(t *testing.T) {
	cfg := &Config{
		Region:         "",
		RuntimeRoleARN: "arn:aws:iam::123456789012:role/test",
	}
	warnings := DiagnoseConfig(cfg)
	for _, w := range warnings {
		if strings.Contains(w.Message, "region") {
			t.Error("should not warn about empty region (validate catches it)")
		}
	}
}

func TestDiagnoseConfig_IAMUser(t *testing.T) {
	cfg := &Config{
		Region:         "us-west-2",
		RuntimeRoleARN: "arn:aws:iam::123456789012:user/testuser",
	}
	warnings := DiagnoseConfig(cfg)
	found := false
	for _, w := range warnings {
		if strings.Contains(w.Message, "IAM user") {
			found = true
		}
	}
	if !found {
		t.Error("expected a warning about IAM user ARN")
	}
}

func TestDiagnoseConfig_RootAccount(t *testing.T) {
	cfg := &Config{
		Region:         "us-west-2",
		RuntimeRoleARN: "arn:aws:iam::123456789012:root",
	}
	warnings := DiagnoseConfig(cfg)
	found := false
	for _, w := range warnings {
		if strings.Contains(w.Message, "root account") {
			found = true
		}
	}
	if !found {
		t.Error("expected a warning about root account")
	}
}

func TestDiagnoseConfig_JWTWithoutAudience(t *testing.T) {
	cfg := &Config{
		Region:         "us-west-2",
		RuntimeRoleARN: "arn:aws:iam::123456789012:role/test",
		A2AAuth: &A2AAuthConfig{
			Mode:         A2AAuthModeJWT,
			DiscoveryURL: "https://example.com/.well-known/openid-configuration",
		},
	}
	warnings := DiagnoseConfig(cfg)
	found := false
	for _, w := range warnings {
		if strings.Contains(w.Message, "allowed_audience is empty") {
			found = true
		}
	}
	if !found {
		t.Error("expected a warning about empty allowed_audience for JWT")
	}
}

func TestDiagnoseConfig_JWTWithAudience(t *testing.T) {
	cfg := &Config{
		Region:         "us-west-2",
		RuntimeRoleARN: "arn:aws:iam::123456789012:role/test",
		A2AAuth: &A2AAuthConfig{
			Mode:         A2AAuthModeJWT,
			DiscoveryURL: "https://example.com/.well-known/openid-configuration",
			AllowedAud:   []string{"my-audience"},
		},
	}
	warnings := DiagnoseConfig(cfg)
	for _, w := range warnings {
		if strings.Contains(w.Message, "allowed_audience") {
			t.Error("should not warn when allowed_audience is set")
		}
	}
}

func TestDiagnoseConfig_IAMMode_NoAudienceWarning(t *testing.T) {
	cfg := &Config{
		Region:         "us-west-2",
		RuntimeRoleARN: "arn:aws:iam::123456789012:role/test",
		A2AAuth: &A2AAuthConfig{
			Mode: A2AAuthModeIAM,
		},
	}
	warnings := DiagnoseConfig(cfg)
	for _, w := range warnings {
		if strings.Contains(w.Message, "allowed_audience") {
			t.Error("should not warn about audience for IAM mode")
		}
	}
}

func TestDiagnoseConfig_NoA2AAuth(t *testing.T) {
	cfg := &Config{
		Region:         "us-west-2",
		RuntimeRoleARN: "arn:aws:iam::123456789012:role/test",
	}
	warnings := DiagnoseConfig(cfg)
	for _, w := range warnings {
		if strings.Contains(w.Message, "a2a") || strings.Contains(w.Message, "JWT") {
			t.Errorf("should not have A2A warnings when no auth configured: %s", w.Message)
		}
	}
}

func TestDiagnoseConfig_EmptyRoleARN(t *testing.T) {
	cfg := &Config{
		Region:         "us-west-2",
		RuntimeRoleARN: "",
	}
	warnings := DiagnoseConfig(cfg)
	for _, w := range warnings {
		if strings.Contains(w.Message, "role") {
			t.Error("should not warn about empty role (validate catches it)")
		}
	}
}

func TestDiagnosticWarning_String_WithHint(t *testing.T) {
	w := DiagnosticWarning{
		Category: ErrCategoryConfiguration,
		Message:  "test message",
		Hint:     "fix it",
	}
	s := w.String()
	if !strings.Contains(s, "[configuration]") {
		t.Errorf("expected category in string, got %q", s)
	}
	if !strings.Contains(s, "test message") {
		t.Errorf("expected message in string, got %q", s)
	}
	if !strings.Contains(s, "hint: fix it") {
		t.Errorf("expected hint in string, got %q", s)
	}
}

func TestDiagnosticWarning_String_WithoutHint(t *testing.T) {
	w := DiagnosticWarning{
		Category: ErrCategoryPermission,
		Message:  "no hint here",
	}
	s := w.String()
	if strings.Contains(s, "hint:") {
		t.Errorf("should not have hint, got %q", s)
	}
}

func TestFormatWarnings_Empty(t *testing.T) {
	result := FormatWarnings(nil)
	if result != "" {
		t.Errorf("expected empty string for nil warnings, got %q", result)
	}
}

func TestFormatWarnings_MultipleWarnings(t *testing.T) {
	warnings := []DiagnosticWarning{
		{Category: "a", Message: "first"},
		{Category: "b", Message: "second"},
	}
	result := FormatWarnings(warnings)
	if !strings.Contains(result, "2 diagnostic warning(s)") {
		t.Errorf("expected header in output, got %q", result)
	}
	if !strings.Contains(result, "1. [a] first") {
		t.Errorf("expected numbered first warning, got %q", result)
	}
	if !strings.Contains(result, "2. [b] second") {
		t.Errorf("expected numbered second warning, got %q", result)
	}
}

func TestJoinMapKeys(t *testing.T) {
	m := map[string]bool{"b": true, "a": true, "c": true}
	result := joinMapKeys(m)
	if result != "a, b, c" {
		t.Errorf("joinMapKeys = %q, want %q", result, "a, b, c")
	}
}

func TestJoinMapKeys_Empty(t *testing.T) {
	result := joinMapKeys(nil)
	if result != "" {
		t.Errorf("joinMapKeys(nil) = %q, want empty", result)
	}
}
