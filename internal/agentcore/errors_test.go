package agentcore

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestDeployError_Error_FullMessage(t *testing.T) {
	cause := fmt.Errorf("AccessDeniedException: User is not authorized")
	de := newDeployError("create", ResTypeAgentRuntime, "my-runtime", cause)

	msg := de.Error()
	if !strings.Contains(msg, "create") {
		t.Errorf("expected operation in message, got %q", msg)
	}
	if !strings.Contains(msg, ResTypeAgentRuntime) {
		t.Errorf("expected resource type in message, got %q", msg)
	}
	if !strings.Contains(msg, "my-runtime") {
		t.Errorf("expected resource name in message, got %q", msg)
	}
	if !strings.Contains(msg, "AccessDeniedException") {
		t.Errorf("expected cause in message, got %q", msg)
	}
	if !strings.Contains(msg, "hint:") {
		t.Errorf("expected remediation hint in message, got %q", msg)
	}
}

func TestDeployError_Unwrap(t *testing.T) {
	cause := fmt.Errorf("root cause")
	de := &DeployError{
		Operation:    "create",
		ResourceType: ResTypeAgentRuntime,
		ResourceName: "test",
		Cause:        cause,
	}

	if !errors.Is(de, cause) {
		t.Error("expected Unwrap to return cause")
	}
}

func TestIsDeployError(t *testing.T) {
	cause := fmt.Errorf("something went wrong")
	de := newDeployError("create", ResTypeAgentRuntime, "test", cause)

	// Direct match.
	result := IsDeployError(de)
	if result == nil {
		t.Fatal("expected non-nil DeployError")
	}
	if result.ResourceName != "test" {
		t.Errorf("ResourceName = %q, want test", result.ResourceName)
	}

	// Wrapped match.
	wrapped := fmt.Errorf("outer: %w", de)
	result = IsDeployError(wrapped)
	if result == nil {
		t.Fatal("expected non-nil DeployError from wrapped error")
	}

	// Non-match.
	result = IsDeployError(fmt.Errorf("plain error"))
	if result != nil {
		t.Error("expected nil for plain error")
	}

	// Nil.
	result = IsDeployError(nil)
	if result != nil {
		t.Error("expected nil for nil error")
	}
}

func TestClassifyAWSError_Permission(t *testing.T) {
	tests := []struct {
		msg string
	}{
		{"AccessDeniedException: User is not authorized to perform this action"},
		{"access denied on resource"},
		{"UnauthorizedAccess"},
		{"operation forbidden"},
	}
	for _, tt := range tests {
		category, remediation := classifyAWSError(fmt.Errorf("%s", tt.msg))
		if category != ErrCategoryPermission {
			t.Errorf("classifyAWSError(%q) category = %q, want %q", tt.msg, category, ErrCategoryPermission)
		}
		if remediation == "" {
			t.Errorf("classifyAWSError(%q) remediation should not be empty", tt.msg)
		}
	}
}

func TestClassifyAWSError_Network(t *testing.T) {
	tests := []struct {
		msg string
	}{
		{"dial tcp: connection refused"},
		{"no such host: bedrock.us-invalid-1.amazonaws.com"},
	}
	for _, tt := range tests {
		category, _ := classifyAWSError(fmt.Errorf("%s", tt.msg))
		if category != ErrCategoryNetwork {
			t.Errorf("classifyAWSError(%q) category = %q, want %q", tt.msg, category, ErrCategoryNetwork)
		}
	}
}

func TestClassifyAWSError_Timeout(t *testing.T) {
	tests := []struct {
		msg string
	}{
		{"runtime did not become ready after 60 attempts"},
		{"context deadline exceeded"},
		{"context canceled"},
	}
	for _, tt := range tests {
		category, _ := classifyAWSError(fmt.Errorf("%s", tt.msg))
		if category != ErrCategoryTimeout {
			t.Errorf("classifyAWSError(%q) category = %q, want %q", tt.msg, category, ErrCategoryTimeout)
		}
	}
}

func TestClassifyAWSError_Configuration(t *testing.T) {
	tests := []struct {
		msg string
	}{
		{"validation error: field X is invalid"},
		{"malformed ARN"},
	}
	for _, tt := range tests {
		category, _ := classifyAWSError(fmt.Errorf("%s", tt.msg))
		if category != ErrCategoryConfiguration {
			t.Errorf("classifyAWSError(%q) category = %q, want %q", tt.msg, category, ErrCategoryConfiguration)
		}
	}
}

func TestClassifyAWSError_DefaultCategory(t *testing.T) {
	category, remediation := classifyAWSError(fmt.Errorf("something unknown happened"))
	if category != ErrCategoryResource {
		t.Errorf("category = %q, want %q", category, ErrCategoryResource)
	}
	if remediation != "" {
		t.Errorf("remediation should be empty for unknown errors, got %q", remediation)
	}
}

func TestClassifyAWSError_Nil(t *testing.T) {
	category, _ := classifyAWSError(nil)
	if category != ErrCategoryResource {
		t.Errorf("category = %q, want %q for nil error", category, ErrCategoryResource)
	}
}

func TestDeployError_NoRemediation(t *testing.T) {
	de := &DeployError{
		Operation:    "delete",
		ResourceType: ResTypeToolGateway,
		ResourceName: "my-tool",
		Message:      "something unknown",
	}
	msg := de.Error()
	if strings.Contains(msg, "hint:") {
		t.Errorf("should not have hint for unknown error, got %q", msg)
	}
}

func TestDeployError_NoCause(t *testing.T) {
	de := &DeployError{
		Operation:    "create",
		ResourceType: ResTypeAgentRuntime,
		ResourceName: "test",
		Message:      "failed",
	}
	msg := de.Error()
	if strings.Contains(msg, "cause:") {
		t.Errorf("should not have cause when nil, got %q", msg)
	}
}

func TestDiagnosticSummary(t *testing.T) {
	errs := []error{
		fmt.Errorf("error 1"),
		fmt.Errorf("error 2"),
	}
	summary := DiagnosticSummary(errs)
	if !strings.Contains(summary, "2 error(s)") {
		t.Errorf("expected '2 error(s)' in summary, got %q", summary)
	}
	if !strings.Contains(summary, "1. error 1") {
		t.Errorf("expected numbered error in summary, got %q", summary)
	}
}

func TestDiagnosticSummary_Empty(t *testing.T) {
	summary := DiagnosticSummary(nil)
	if summary != "" {
		t.Errorf("expected empty summary for nil errors, got %q", summary)
	}
}

func TestNewDeployError_ClassifiesPermission(t *testing.T) {
	cause := fmt.Errorf("AccessDeniedException: not authorized")
	de := newDeployError("create", ResTypeAgentRuntime, "rt-1", cause)
	if de.Category != ErrCategoryPermission {
		t.Errorf("category = %q, want %q", de.Category, ErrCategoryPermission)
	}
	if de.Remediation == "" {
		t.Error("expected non-empty remediation for permission error")
	}
}

func TestNewDeployError_PreservesFields(t *testing.T) {
	cause := fmt.Errorf("something failed")
	de := newDeployError("update", ResTypeToolGateway, "gw-1", cause)
	if de.Operation != "update" {
		t.Errorf("Operation = %q, want update", de.Operation)
	}
	if de.ResourceType != ResTypeToolGateway {
		t.Errorf("ResourceType = %q, want %q", de.ResourceType, ResTypeToolGateway)
	}
	if de.ResourceName != "gw-1" {
		t.Errorf("ResourceName = %q, want gw-1", de.ResourceName)
	}
	if de.Cause != cause {
		t.Error("Cause should be the original error")
	}
}

func TestContainsAny(t *testing.T) {
	if !containsAny("hello world", []string{"world"}) {
		t.Error("expected true for matching substring")
	}
	if containsAny("hello world", []string{"foo", "bar"}) {
		t.Error("expected false for non-matching substrings")
	}
	if containsAny("hello", nil) {
		t.Error("expected false for nil substrings")
	}
}
