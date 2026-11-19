package agentcore

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/AltairaLabs/PromptKit/runtime/v2/prompt"
	"github.com/AltairaLabs/promptarena/v2/deploy/adaptersdk"
)

// awsNamePattern is the regex pattern for valid AWS Bedrock AgentCore resource
// names. Names must start with a letter, contain only letters, digits, and
// underscores, and be at most 48 characters long.
const awsNamePattern = `^[a-zA-Z][a-zA-Z0-9_]{0,47}$`

// awsNameRe is the compiled regex for validating AWS resource names.
var awsNameRe = regexp.MustCompile(awsNamePattern)

// defaultPackName is used when a pack has no explicit ID.
const defaultPackName = "default"

// maxAWSNameLen is the longest name AWS accepts for these resources.
const maxAWSNameLen = 48

// packBaseName is the AWS-safe stem every pack-level resource name is built
// from.
//
// A pack id and an AgentCore resource name cannot be spelled the same way. The
// pack schema requires ^[a-z][a-z0-9-]*$ and the runtime refuses a pack that
// fails it; AWS requires ^[a-zA-Z][a-zA-Z0-9_]{0,47}$ here. Hyphens are legal
// in one and rejected by the other, underscores the reverse, so the two overlap
// only on ids with no separator at all.
//
// That made "customer-support" — the schema's own idiom, and the obvious thing
// to write — undeployable, while "customer_support" deployed, reached ready,
// and then failed its first turn because the runtime would not load the pack.
// Translating here means the author writes an id their pack schema accepts and
// this finds a name AWS accepts.
//
// Every pack-level name goes through this, so plan, apply and validation cannot
// disagree about what a resource is called.
func packBaseName(pack *prompt.Pack) string {
	if pack == nil || pack.ID == "" {
		return defaultPackName
	}
	return sanitizeAWSName(pack.ID)
}

// sanitizeAWSName rewrites a name into one AWS will accept.
//
// Anything outside [a-zA-Z0-9] becomes an underscore, runs collapse, and a
// leading non-letter is dropped because AWS requires the first character to be
// one. A name that is already valid comes back unchanged, which is what makes
// this safe to introduce: no deployment that works today is renamed, and the
// only names that change are ones that could never have been deployed.
func sanitizeAWSName(name string) string {
	out := sanitizeAWSChars(name)
	// Suffixes like "_online_eval" are appended to this, so leave room; the
	// cap has to cover the whole name AWS finally receives.
	if len(out) > maxAWSNameLen {
		out = strings.TrimRight(out[:maxAWSNameLen], "_")
	}
	return out
}

// sanitizeAWSChars applies sanitizeAWSName's character rules without its
// length cap.
func sanitizeAWSChars(name string) string {
	var b strings.Builder
	lastUnderscore := false
	for _, r := range name {
		switch {
		case (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z'):
			// AWS wants a letter first, so digits before one are dropped
			// rather than carried into an invalid name.
			if b.Len() == 0 && !isASCIILetter(r) {
				continue
			}
			b.WriteRune(r)
			lastUnderscore = false
		case !lastUnderscore && b.Len() > 0:
			b.WriteRune('_')
			lastUnderscore = true
		}
	}

	out := strings.TrimRight(b.String(), "_")
	if out == "" {
		return defaultPackName
	}
	return out
}

// packScopedNameHashLen is the length of the hash that ends a pack-scoped
// name too long for AWS.
const packScopedNameHashLen = 8

// packScopedName is the name of a pack-level resource whose own identity comes
// from inside the pack — an eval's id — or that has none of its own: the pack's
// base name, an underscore, then suffix.
//
// Evaluators and policy engines used to be named after the eval or the prompt
// alone ("quality", "assistant_policy_engine"). Those names are account-wide,
// so two packs with an eval or prompt of the same name asked for the same
// resource, and apply's adopt-on-conflict path quietly took over the other
// pack's. Prefixing the pack keeps them apart and makes them findable by pack.
//
// A name over the AWS limit keeps its start and ends in a short hash of the
// whole, so two long names that share a prefix stay distinct rather than
// truncating to the same thing.
func packScopedName(pack *prompt.Pack, suffix string) string {
	name := sanitizeAWSChars(packBaseName(pack) + "_" + suffix)
	if len(name) <= maxAWSNameLen {
		return name
	}
	sum := sha256.Sum256([]byte(name))
	hash := hex.EncodeToString(sum[:])[:packScopedNameHashLen]
	return strings.TrimRight(name[:maxAWSNameLen-packScopedNameHashLen-1], "_") + "_" + hash
}

// policyEngineName is the name of the pack's one Cedar policy engine.
func policyEngineName(pack *prompt.Pack) string {
	return packScopedName(pack, "policy_engine")
}

// isASCIILetter reports whether r may start an AWS resource name.
func isASCIILetter(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
}

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

// gatewayTargetNamePattern is gatewayNamePattern with the target's limit.
const gatewayTargetNamePattern = `^([0-9a-zA-Z]-?){1,100}$`

var gatewayTargetNameRe = regexp.MustCompile(gatewayTargetNamePattern)

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
	collectToolNames(names, pack, cfg)
	collectAgentNames(names, pack)
	return names
}

// collectPackLevelNames adds memory and policy engine names.
func collectPackLevelNames(names map[string]string, pack *prompt.Pack, cfg *Config) {
	if cfg.HasMemory() {
		names[packBaseName(pack)+"_memory"] = ResTypeMemory
	}
	if len(gatewayBlockedTools(pack)) > 0 {
		names[policyEngineName(pack)] = ResTypeCedarPolicy
	}
}

// evaluatorName is the name of the evaluator created for pack.Evals[i]: the
// pack's base name and the eval's id, or eval_<index> when it has none (see
// packScopedName).
//
// Plan, validation and apply all take the name from here, so they cannot
// disagree about it. An evaluator deployed under the old, unscoped name is
// recorded under that name in state; the next apply creates the scoped one,
// re-points the online eval config at it, and deletes the old one.
func evaluatorName(pack *prompt.Pack, i int) string {
	id := pack.Evals[i].ID
	if id == "" {
		id = fmt.Sprintf("eval_%d", i)
	}
	return packScopedName(pack, id)
}

// collectEvalNames adds evaluator and online eval config names.
func collectEvalNames(names map[string]string, pack *prompt.Pack) {
	hasOnlineEval := false
	for i := range pack.Evals {
		switch pack.Evals[i].Type {
		case evalTypeLLMAsJudge:
			names[evaluatorName(pack, i)] = ResTypeEvaluator
			hasOnlineEval = true
		case evalTypeBuiltin:
			hasOnlineEval = true
		}
	}
	if hasOnlineEval {
		names[packBaseName(pack)+"_online_eval"] = ResTypeOnlineEvalConfig
	}
}

// collectToolNames adds tool gateway names.
//
// The tool name itself, because that is what apply records in state: the
// gateway target carries the tool's own name. Deriving a different name here
// meant plan and state never agreed on the same resource.
// Only tools bound for the Gateway are named here. A mock tool runs in the
// runtime and never becomes a Gateway resource, so holding its name to the
// Gateway's character rules would reject a pack that deploys perfectly well.
func collectToolNames(names map[string]string, pack *prompt.Pack, cfg *Config) {
	for _, toolName := range gatewayToolNames(pack, cfg) {
		names[toolName] = ResTypeToolGateway
	}
}

// a2aWiringName is the name of a multi-agent member's A2A wiring resource.
// Plan, validation and apply all take it from here.
func a2aWiringName(member string) string {
	return member + "_a2a"
}

// collectAgentNames adds runtime and a2a endpoint names — the names apply
// creates, so validation checks what is actually sent.
func collectAgentNames(names map[string]string, pack *prompt.Pack) {
	if adaptersdk.IsMultiAgent(pack) {
		for memberName := range pack.Agents.Members {
			names[memberName] = ResTypeAgentRuntime
			names[a2aWiringName(memberName)] = ResTypeA2AEndpoint
		}
		return
	}
	names[packBaseName(pack)] = ResTypeAgentRuntime
}

// validateResourceNames collects all derived resource names and validates them
// against the AWS naming pattern. Returns a list of validation errors, or nil
// if all names are valid.
func validateResourceNames(pack *prompt.Pack, cfg *Config) []string {
	errs := validateToolTargetKinds(pack, cfg)
	// The parent gateway a new deployment's tools go on.
	if gw := packGatewayName(pack); len(gatewayToolNames(pack, cfg)) > 0 && !gatewayNameRe.MatchString(gw) {
		errs = append(errs, fmt.Sprintf("gateway name %q is invalid: must match %s", gw, gatewayNamePattern))
	}
	derived := collectDerivedNames(pack, cfg)

	// Sort keys for deterministic error ordering.
	keys := make([]string, 0, len(derived))
	for k := range derived {
		keys = append(keys, k)
	}
	sort.Strings(keys)

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
// The target name that AWS actually validates is checked by
// validateToolTargetNames, against the pattern AWS actually uses. The parent
// Gateway is named after the pack (packGatewayName), which is always valid.
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
		// The tool name becomes the gateway *target* name, after sanitizing,
		// so check what will actually be sent. (The parent gateway is named
		// after the pack, not a tool: see packGatewayName.)
		targetName := sanitizeGatewayTargetName(name)
		if !gatewayTargetNameRe.MatchString(targetName) {
			errs = append(errs, fmt.Sprintf(
				"tool_targets: tool %q produces invalid gateway target name %q: must match %s",
				name, targetName, gatewayTargetNamePattern,
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

// validateToolTargetKinds rejects Gateway target types the runtime cannot call.
//
// A Lambda target carries the pack's own tool name into the Gateway, so the
// runtime can address it. OpenAPI, Smithy and API Gateway targets take their
// tool names from the schema or from path and method, so the name the pack
// uses is not the name the Gateway knows — the target would be created and
// every call would miss.
//
// Refused at plan rather than left to fail later: the deploy would otherwise
// succeed and produce an agent whose tool never answers.
func validateToolTargetKinds(pack *prompt.Pack, cfg *Config) []string {
	if pack == nil || cfg == nil {
		return nil
	}

	var names []string
	for name := range pack.Tools {
		spec := cfg.ArenaConfig.toolSpecForName(name)
		if spec != nil && spec.LambdaARN == "" && hasAWSTarget(spec) {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return nil
	}
	sort.Strings(names)

	errs := make([]string, 0, len(names))
	for _, name := range names {
		errs = append(errs, fmt.Sprintf(
			"tool %q uses an openapi, smithy or api_gateway target, which is not "+
				"supported yet: the Gateway names those tools from the schema rather "+
				"than from the pack, so the runtime cannot call them. Use lambda_arn, "+
				"or run the tool in the runtime with a mock or http config.", name))
	}
	return errs
}
