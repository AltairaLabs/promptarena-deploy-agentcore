package agentcore

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/AltairaLabs/PromptKit/runtime/deploy/adaptersdk"
	"github.com/AltairaLabs/PromptKit/runtime/prompt"
)

// awsNamePattern is the regex pattern for valid AWS Bedrock AgentCore resource
// names. Names must start with a letter, contain only letters, digits, and
// underscores, and be at most 48 characters long.
const awsNamePattern = `^[a-zA-Z][a-zA-Z0-9_]{0,47}$`

// awsNameRe is the compiled regex for validating AWS resource names.
var awsNameRe = regexp.MustCompile(awsNamePattern)

// defaultPackName is used when a pack has no explicit ID.
const defaultPackName = "default"

// validateAWSName checks whether name is a valid AWS resource name and returns
// an error describing the problem if not. The resourceType is used in the error
// message to help users identify which resource is invalid.
func validateAWSName(name, resourceType string) error {
	if !awsNameRe.MatchString(name) {
		return fmt.Errorf(
			"resource name %q (%s) is invalid: must match %s",
			name, resourceType, awsNamePattern,
		)
	}
	return nil
}

// toolGatewaySuffix is the suffix appended to tool names when creating
// tool gateway resources. This constant ensures plan and apply use the
// same naming convention.
const toolGatewaySuffix = "_tool_gw"

// collectDerivedNames builds a map of all derived resource names to their
// resource types, simulating the same naming patterns used by
// generateDesiredResources and apply phases.
func collectDerivedNames(pack *prompt.Pack, cfg *Config) map[string]string {
	names := make(map[string]string)
	collectPackLevelNames(names, pack, cfg)
	collectEvalNames(names, pack)
	collectToolNames(names, pack)
	collectAgentNames(names, pack)
	return names
}

// collectPackLevelNames adds memory, cedar policy, and online eval names.
func collectPackLevelNames(names map[string]string, pack *prompt.Pack, cfg *Config) {
	if cfg.HasMemory() {
		names[pack.ID+"_memory"] = ResTypeMemory
	}
	for _, policyName := range policyResourceNames(pack) {
		names[policyName+"_policy_engine"] = ResTypeCedarPolicy
	}
}

// collectEvalNames adds evaluator and online eval config names.
func collectEvalNames(names map[string]string, pack *prompt.Pack) {
	hasOnlineEval := false
	for i := range pack.Evals {
		switch pack.Evals[i].Type {
		case evalTypeLLMAsJudge:
			names[pack.Evals[i].ID+"_eval"] = ResTypeEvaluator
			hasOnlineEval = true
		case evalTypeBuiltin:
			hasOnlineEval = true
		}
	}
	if hasOnlineEval {
		names[pack.ID+"_online_eval"] = ResTypeOnlineEvalConfig
	}
}

// collectToolNames adds tool gateway names.
func collectToolNames(names map[string]string, pack *prompt.Pack) {
	for toolName := range pack.Tools {
		names[toolName+toolGatewaySuffix] = ResTypeToolGateway
	}
}

// collectAgentNames adds runtime, a2a endpoint, and gateway names.
func collectAgentNames(names map[string]string, pack *prompt.Pack) {
	if adaptersdk.IsMultiAgent(pack) {
		for memberName := range pack.Agents.Members {
			names[memberName] = ResTypeAgentRuntime
			names[memberName+"_endpoint"] = ResTypeA2AEndpoint
		}
		if pack.Agents.Entry != "" {
			names[pack.Agents.Entry+"_gateway"] = "gateway"
		}
		return
	}
	name := pack.ID
	if name == "" {
		name = defaultPackName
	}
	names[name] = ResTypeAgentRuntime
}

// validateResourceNames collects all derived resource names and validates them
// against the AWS naming pattern. Returns a list of validation errors, or nil
// if all names are valid.
func validateResourceNames(pack *prompt.Pack, cfg *Config) []string {
	derived := collectDerivedNames(pack, cfg)

	// Sort keys for deterministic error ordering.
	keys := make([]string, 0, len(derived))
	for k := range derived {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var errs []string
	for _, name := range keys {
		if err := validateAWSName(name, derived[name]); err != nil {
			errs = append(errs, err.Error())
		}
	}
	return errs
}

// validateToolTargetNames checks that tool target map keys will produce valid
// derived gateway names. Returns validation errors for any invalid names.
func validateToolTargetNames(targets map[string]*ArenaToolSpec) []string {
	if len(targets) == 0 {
		return nil
	}

	keys := make([]string, 0, len(targets))
	for k := range targets {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var errs []string
	for _, name := range keys {
		gwName := name + toolGatewaySuffix
		if err := validateAWSName(gwName, "tool_gateway"); err != nil {
			errs = append(errs, fmt.Sprintf(
				"tool_targets: tool %q produces invalid gateway name %q: must match %s",
				name, gwName, awsNamePattern,
			))
		}
	}
	return errs
}

// formatNameErrors formats resource name validation errors into a single
// joined string suitable for returning in an error response.
func formatNameErrors(errs []string) string {
	return strings.Join(errs, "; ")
}
