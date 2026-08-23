package agentcore

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/AltairaLabs/PromptKit/runtime/prompt"
	"github.com/AltairaLabs/promptarena/deploy/adaptersdk"
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

// gatewayNamePattern is what AWS accepts for a Gateway name:
// alphanumerics with optional single hyphens, up to 48 characters.
//
// It is NOT awsNamePattern. Every other AgentCore resource takes underscores —
// runtime and memory names rely on them — but a Gateway rejects them outright.
// Validating gateway names against the permissive pattern is what let
// "lookup_order-gw" reach the API and come back a ValidationException.
// AWS states this as ^([0-9a-zA-Z][-]?){1,48}$; the bracket around the hyphen
// is redundant and dropped here, so the two read slightly differently while
// matching exactly the same names.
const gatewayNamePattern = `^([0-9a-zA-Z]-?){1,48}$`

var gatewayNameRe = regexp.MustCompile(gatewayNamePattern)

// gatewaySuffix is appended to the parent gateway's name.
//
// Hyphen rather than underscore because of the pattern above, and this is the
// suffix the API actually receives — the old toolGatewaySuffix ("_tool_gw")
// was only ever used for plan output and validation, never for a call, so the
// two disagreed about the name of the same resource.
const gatewaySuffix = "-gw"

// maxGatewayNameLen is the longest name a Gateway accepts.
const maxGatewayNameLen = 48

// maxGatewayTargetNameLen is the longest name a Gateway *target* accepts.
//
// Same character rule as the gateway, different limit — 100 rather than 48.
// Both reject underscores, which is the part that matters: a tool name goes
// into the target name directly, so "lookup_order" is refused there too, one
// call after the parent gateway that refused it first.
const maxGatewayTargetNameLen = 100

// sanitizeGatewayName makes a name the Gateway API will accept.
//
// Anything outside [0-9a-zA-Z] becomes a hyphen, runs of hyphens collapse, and
// leading and trailing hyphens go — the pattern allows a hyphen only after an
// alphanumeric, so "--" and a trailing "-" are both rejected.
//
// A name that is already valid comes back unchanged. That is the property that
// makes this safe to introduce: every gateway that deploys today has a name
// with no underscores, so no existing deployment is renamed by this. The only
// names that change are the ones that could never have been created.
func sanitizeGatewayName(name string) string {
	// Room for the suffix the caller appends.
	return sanitizeGatewayIdent(name, maxGatewayNameLen-len(gatewaySuffix))
}

// sanitizeGatewayTargetName makes a tool name acceptable as a gateway target.
//
// Targets take no suffix, and allow 100 characters rather than 48.
func sanitizeGatewayTargetName(name string) string {
	return sanitizeGatewayIdent(name, maxGatewayTargetNameLen)
}

// sanitizeGatewayIdent is the shared rule behind both.
func sanitizeGatewayIdent(name string, limit int) string {
	var b strings.Builder
	lastHyphen := false
	for _, r := range name {
		switch {
		case (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z'):
			b.WriteRune(r)
			lastHyphen = false
		case !lastHyphen && b.Len() > 0:
			b.WriteRune('-')
			lastHyphen = true
		}
	}

	out := strings.TrimRight(b.String(), "-")

	// The cap has to cover the whole name the API receives. Truncating a
	// gateway stem to the full 48 and then appending "-gw" produces 51
	// characters, which AWS rejects — so callers pass the room they need.
	if len(out) > limit {
		out = strings.TrimRight(out[:limit], "-")
	}
	return out
}

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
//
// The tool name itself, because that is what apply records in state: the
// gateway target carries the tool's own name. Deriving a different name here
// meant plan and state never agreed on the same resource.
func collectToolNames(names map[string]string, pack *prompt.Pack) {
	for toolName := range pack.Tools {
		names[toolName] = ResTypeToolGateway
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
		if err := validateDerivedName(name, derived[name]); err != nil {
			errs = append(errs, err.Error())
		}
	}
	return errs
}

// validateDerivedName applies the rule that belongs to the resource type.
//
// Tool gateways are the exception, and the reason this function exists. A tool
// name becomes a gateway *target* name, and the parent Gateway name is derived
// from it — and Gateway names take hyphens but not underscores, the opposite of
// every other AgentCore resource. Applying the general rule here rejected
// "web-search", which AWS accepts, while passing "lookup_order", which it
// rejects with a ValidationException at apply.
//
// The gateway name that AWS actually validates is checked by
// validateToolTargetNames, against the pattern AWS actually uses.
func validateDerivedName(name, resourceType string) error {
	if resourceType == ResTypeToolGateway {
		if name == "" {
			return fmt.Errorf("resource name %q (%s) is invalid: must not be empty",
				name, resourceType)
		}
		return nil
	}
	return validateAWSName(name, resourceType)
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
		// The parent gateway is the name AWS validates strictly, and it is
		// derived rather than given, so check what will actually be sent.
		gwName := sanitizeGatewayName(name) + gatewaySuffix
		if !gatewayNameRe.MatchString(gwName) {
			errs = append(errs, fmt.Sprintf(
				"tool_targets: tool %q produces invalid gateway name %q: must match %s",
				name, gwName, gatewayNamePattern,
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
