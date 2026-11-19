package agentcore

import (
	"fmt"
	"log"
	"sort"
	"strings"

	"github.com/AltairaLabs/PromptKit/runtime/v2/prompt"
)

// generateCedarStatements builds individual Cedar policy statements from
// the given tool policy's blocklist. Returns nil if there are no rules.
//
// Only tool blocklist entries produce Cedar policies — validators
// (banned_words, max_length, regex_match, json_schema) and tool policy
// limits (max_rounds, max_tool_calls_per_turn) are enforced at runtime
// by PromptKit middleware, not by Cedar.
//
// Each element is a standalone forbid block suitable for a single
// CreatePolicy call (AWS does not accept multiple policies per statement).
func generateCedarStatements(
	_ []*prompt.Validator,
	tp *prompt.ToolPolicyPack,
	gatewayARN string,
	registeredTools map[string]bool,
) []string {
	if tp == nil || len(tp.Blocklist) == 0 {
		return nil
	}
	return cedarFromToolPolicy(tp, gatewayARN, registeredTools)
}

// cedarFromToolPolicy generates Cedar forbid blocks from the tool policy
// blocklist. Only blocklist entries are supported — max_rounds and
// max_tool_calls_per_turn are enforced at runtime.
//
// AWS requires both a specific gateway resource AND that the action exists
// in the gateway's Cedar schema. Tools not registered on the gateway cannot
// be blocked (and cannot be invoked anyway). The registeredTools set filters
// the blocklist to only tools that exist on the gateway.
//
// An AgentCore policy engine denies by default: once one is attached to the
// gateway, a tool call that no permit matches is refused with "No policy
// applies to the request (denied by default)". A blocklist made only of
// forbids therefore blocked every tool on the gateway, not just the listed
// ones. So when there is a gateway, the forbids are followed by one permit for
// every action on it; a forbid overrides a permit in Cedar, so the blocklisted
// tools stay denied and the rest are allowed.
func cedarFromToolPolicy(tp *prompt.ToolPolicyPack, gatewayARN string, registeredTools map[string]bool) []string {
	var blocks []string
	for _, tool := range tp.Blocklist {
		if registeredTools != nil && !registeredTools[tool] {
			log.Printf("agentcore: skipping blocklist entry %q — not registered on gateway (cannot be invoked)", tool)
			continue
		}
		blocks = append(blocks, cedarToolBlocklist(tool, gatewayARN))
	}
	if len(blocks) > 0 && gatewayARN != "" {
		blocks = append(blocks, cedarGatewayPermit(gatewayARN))
	}
	return blocks
}

// cedarGatewayPermit allows every action on the gateway, so that the policy
// engine's default deny applies only where a forbid says so.
func cedarGatewayPermit(gatewayARN string) string {
	return fmt.Sprintf(`permit (principal, action, resource == AgentCore::Gateway::%q);`, gatewayARN)
}

// cedarToolBlocklist generates a forbid block for a blocked tool.
// Uses the AgentCore Cedar action format: AgentCore::Action::"<target>___<tool>"
// (three underscores), where <target> is the gateway target's name and <tool>
// the tool's name in the target's inline schema. The target is named by
// sanitizeGatewayTargetName, which turns underscores into hyphens, so a tool
// called search_d is the action "search-d___search_d". The resource is
// constrained to the specific gateway ARN as required by AWS.
func cedarToolBlocklist(toolName, gatewayARN string) string {
	escapedTarget := escapeCedarString(sanitizeGatewayTargetName(toolName))
	escapedName := escapeCedarString(toolName)
	resourceClause := "resource"
	if gatewayARN != "" {
		resourceClause = fmt.Sprintf("resource == AgentCore::Gateway::%q", gatewayARN)
	}
	return fmt.Sprintf(
		`forbid (principal, action == AgentCore::Action::"%s___%s", %s);`,
		escapedTarget, escapedName, resourceClause,
	)
}

// policyResourceNames returns a sorted list of prompt names that have
// tool policy blocklist entries. Used by both plan and apply.
func policyResourceNames(pack *prompt.Pack) []string {
	var names []string
	for name, p := range pack.Prompts {
		if hasPolicyRules(p) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// hasPolicyRules returns true if the prompt has a tool policy with
// blocklist entries. Validators and max_rounds/max_tool_calls_per_turn
// are enforced at runtime and do not produce Cedar policies.
func hasPolicyRules(p *prompt.PackPrompt) bool {
	if p.ToolPolicy != nil && len(p.ToolPolicy.Blocklist) > 0 {
		return true
	}
	return false
}

// escapeCedarString escapes double quotes and backslashes in Cedar strings.
func escapeCedarString(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return s
}
