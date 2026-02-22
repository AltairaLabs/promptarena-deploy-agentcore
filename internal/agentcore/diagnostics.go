package agentcore

import (
	"fmt"
	"slices"
	"strings"
)

// DiagnosticWarning represents a non-fatal issue detected during
// pre-deploy diagnostics.
type DiagnosticWarning struct {
	Category string
	Message  string
	Hint     string
}

// String formats the warning for display.
func (w DiagnosticWarning) String() string {
	if w.Hint != "" {
		return fmt.Sprintf("[%s] %s (hint: %s)", w.Category, w.Message, w.Hint)
	}
	return fmt.Sprintf("[%s] %s", w.Category, w.Message)
}

// agentcoreRegions lists AWS regions where Bedrock AgentCore is available.
var agentcoreRegions = map[string]bool{
	"us-east-1": true,
	"us-west-2": true,
	"eu-west-1": true,
}

// DiagnoseConfig checks the configuration for common misconfigurations and
// returns warnings. Unlike validate(), these are non-fatal — they highlight
// issues that are likely to cause deploy failures.
func DiagnoseConfig(cfg *Config) []DiagnosticWarning {
	var warnings []DiagnosticWarning
	warnings = append(warnings, diagnoseRegion(cfg)...)
	warnings = append(warnings, diagnoseRoleARN(cfg)...)
	warnings = append(warnings, diagnoseA2AConfig(cfg)...)
	warnings = append(warnings, diagnoseMemory(cfg)...)
	return warnings
}

// diagnoseRegion checks for unsupported or unusual regions.
func diagnoseRegion(cfg *Config) []DiagnosticWarning {
	if cfg.Region == "" {
		return nil // validate() will catch this
	}
	if !agentcoreRegions[cfg.Region] {
		return []DiagnosticWarning{{
			Category: ErrCategoryConfiguration,
			Message: fmt.Sprintf(
				"region %q may not support Bedrock AgentCore", cfg.Region,
			),
			Hint: fmt.Sprintf(
				"supported regions: %s", joinMapKeys(agentcoreRegions),
			),
		}}
	}
	return nil
}

// diagnoseRoleARN checks for common IAM role ARN mistakes.
func diagnoseRoleARN(cfg *Config) []DiagnosticWarning {
	if cfg.RuntimeRoleARN == "" {
		return nil // validate() will catch this
	}
	var warnings []DiagnosticWarning
	if extractAccountFromARN(cfg.RuntimeRoleARN) == "123456789012" {
		warnings = append(warnings, DiagnosticWarning{
			Category: ErrCategoryConfiguration,
			Message:  "runtime_role_arn uses the placeholder account ID 123456789012",
			Hint:     "replace with your real AWS account ID",
		})
	}
	if strings.Contains(cfg.RuntimeRoleARN, ":user/") {
		warnings = append(warnings, DiagnosticWarning{
			Category: ErrCategoryPermission,
			Message:  "runtime_role_arn appears to be an IAM user, not a role",
			Hint:     "use an IAM role ARN (arn:aws:iam::<account>:role/<name>)",
		})
	}
	if strings.Contains(cfg.RuntimeRoleARN, ":root") {
		warnings = append(warnings, DiagnosticWarning{
			Category: ErrCategoryPermission,
			Message:  "runtime_role_arn references the root account",
			Hint:     "create a dedicated IAM role with least-privilege permissions",
		})
	}
	return warnings
}

// diagnoseA2AConfig checks for A2A auth configuration issues.
func diagnoseA2AConfig(cfg *Config) []DiagnosticWarning {
	if cfg.A2AAuth == nil {
		return nil
	}
	var warnings []DiagnosticWarning
	if cfg.A2AAuth.Mode == A2AAuthModeJWT {
		if len(cfg.A2AAuth.AllowedAud) == 0 {
			warnings = append(warnings, DiagnosticWarning{
				Category: ErrCategoryConfiguration,
				Message:  "a2a_auth uses JWT mode but allowed_audience is empty",
				Hint:     "specify allowed_audience to restrict JWT token validation",
			})
		}
	}
	return warnings
}

// diagnoseMemory checks for memory strategy issues.
func diagnoseMemory(cfg *Config) []DiagnosticWarning {
	if slices.Contains(cfg.Memory.Strategies, StrategyEpisodic) {
		return []DiagnosticWarning{{
			Category: ErrCategoryConfiguration,
			Message:  "episodic memory strategy may be rejected by AWS Bedrock AgentCore",
			Hint:     "consider using semantic, summary, or user_preference instead",
		}}
	}
	return nil
}

// joinMapKeys returns sorted, comma-separated keys of a map.
func joinMapKeys(m map[string]bool) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	// Sort for deterministic output.
	sortStrings(keys)
	return strings.Join(keys, ", ")
}

// sortStrings sorts a string slice in place. This avoids importing "sort"
// in this file by using a simple insertion sort for small slices.
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// FormatWarnings returns a multi-line string from a list of warnings,
// suitable for display to the user.
func FormatWarnings(warnings []DiagnosticWarning) string {
	if len(warnings) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d diagnostic warning(s):\n", len(warnings))
	for i, w := range warnings {
		fmt.Fprintf(&b, "  %d. %s\n", i+1, w.String())
	}
	return b.String()
}
