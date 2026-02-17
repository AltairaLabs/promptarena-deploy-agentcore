package agentcore

import (
	"errors"
	"fmt"
	"strings"
)

// Error category constants classify deployment failures for diagnostics.
const (
	ErrCategoryPermission    = "permission"
	ErrCategoryConfiguration = "configuration"
	ErrCategoryResource      = "resource"
	ErrCategoryTimeout       = "timeout"
	ErrCategoryNetwork       = "network"
)

// DeployError is a structured error type that provides actionable diagnostics
// for deployment failures. It includes the failed resource, error category,
// and a human-readable remediation hint.
type DeployError struct {
	// Category classifies the failure (e.g. "permission", "configuration").
	Category string
	// ResourceType is the type of resource that failed (e.g. "agent_runtime").
	ResourceType string
	// ResourceName is the name of the resource that failed.
	ResourceName string
	// Operation is the action that failed (e.g. "create", "update", "delete").
	Operation string
	// Message is the primary error description.
	Message string
	// Remediation is a human-readable hint on how to fix the issue.
	Remediation string
	// Cause is the underlying error, if any.
	Cause error
}

// Error implements the error interface with a diagnostic-rich message.
func (e *DeployError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s %q failed", e.Operation, e.ResourceType, e.ResourceName)
	if e.Message != "" {
		fmt.Fprintf(&b, ": %s", e.Message)
	}
	if e.Cause != nil {
		fmt.Fprintf(&b, " (cause: %v)", e.Cause)
	}
	if e.Remediation != "" {
		fmt.Fprintf(&b, " [hint: %s]", e.Remediation)
	}
	return b.String()
}

// Unwrap returns the underlying cause for errors.Is/As compatibility.
func (e *DeployError) Unwrap() error {
	return e.Cause
}

// classifyAWSError inspects an AWS error and returns a category and
// remediation hint. It checks for common patterns in error messages.
func classifyAWSError(err error) (category, remediation string) {
	if err == nil {
		return ErrCategoryResource, ""
	}
	msg := err.Error()
	return classifyErrorMessage(msg)
}

// classifyErrorMessage determines category and remediation from an error string.
func classifyErrorMessage(msg string) (category, remediation string) {
	lower := strings.ToLower(msg)

	if containsAny(lower, permissionKeywords) {
		return ErrCategoryPermission, hintCheckIAM
	}
	if containsAny(lower, networkKeywords) {
		return ErrCategoryNetwork, hintCheckNetwork
	}
	if containsAny(lower, timeoutKeywords) {
		return ErrCategoryTimeout, hintRetryOrTimeout
	}
	if containsAny(lower, configKeywords) {
		return ErrCategoryConfiguration, hintCheckConfig
	}
	return ErrCategoryResource, ""
}

// Keyword groups for error classification.
var (
	permissionKeywords = []string{
		"accessdenied", "access denied", "unauthorized",
		"not authorized", "forbidden", "insufficientprivileges",
	}
	networkKeywords = []string{
		"connection refused", "no such host", "timeout",
		"dial tcp", "tls handshake", "endpoint",
	}
	timeoutKeywords = []string{
		"did not become ready", "deadline exceeded",
		"context canceled",
	}
	configKeywords = []string{
		"validation", "invalid", "malformed", "does not match",
	}
)

// Remediation hint constants.
const (
	hintCheckIAM       = "verify the runtime_role_arn has required permissions for Bedrock AgentCore"
	hintCheckNetwork   = "verify the AWS region is correct and network connectivity is available"
	hintRetryOrTimeout = "the resource may still be provisioning; retry after a short wait"
	hintCheckConfig    = "check the deploy config values match AWS requirements"
)

// containsAny returns true if s contains any of the substrings.
func containsAny(s string, substrings []string) bool {
	for _, sub := range substrings {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// newDeployError creates a DeployError with automatic AWS error classification.
func newDeployError(operation, resType, resName string, cause error) *DeployError {
	category, remediation := classifyAWSError(cause)
	return &DeployError{
		Category:     category,
		ResourceType: resType,
		ResourceName: resName,
		Operation:    operation,
		Message:      cause.Error(),
		Remediation:  remediation,
		Cause:        cause,
	}
}

// IsDeployError returns the DeployError if err is (or wraps) one.
func IsDeployError(err error) *DeployError {
	var de *DeployError
	if errors.As(err, &de) {
		return de
	}
	return nil
}

// DiagnosticSummary returns a multi-line diagnostic string for a slice of
// errors, suitable for display to the user after a failed deployment.
func DiagnosticSummary(errs []error) string {
	if len(errs) == 0 {
		return ""
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Deployment completed with %d error(s):\n", len(errs))
	for i, err := range errs {
		fmt.Fprintf(&b, "  %d. %s\n", i+1, err.Error())
	}
	return b.String()
}
