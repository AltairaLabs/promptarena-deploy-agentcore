package agentcore

import (
	"fmt"
	"log"
	"slices"
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
// tool policy blocklist entries.
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

// gatewayBlockedTools returns, sorted, the blocklisted pack tools that no
// prompt in the pack may call. These are the tools the gateway can refuse.
//
// A gateway takes one policy engine, and every prompt of the pack calls its
// tools through the same gateway, which cannot tell one prompt from another.
// Giving each prompt its own engine meant each association replaced the last,
// so only the last prompt's blocklist was ever enforced. A single engine that
// forbade the union would be wrong the other way: a tool one prompt blocks and
// another uses would be refused to both.
//
// So a tool is forbidden at the gateway when every prompt either blocklists it
// or cannot use it. A prompt that lists no tools is taken to be able to use
// any, which errs toward leaving a tool callable. A tool that some prompt
// blocks and another may use is left to the runtime, which enforces each
// prompt's own blocklist on its tool calls (see gatewayRuntimeOnlyTools).
func gatewayBlockedTools(pack *prompt.Pack) []string {
	blocked, _ := splitBlocklistedTools(pack)
	return blocked
}

// gatewayRuntimeOnlyTools returns, sorted, the blocklisted pack tools that
// some other prompt may still call, so the gateway cannot refuse them.
func gatewayRuntimeOnlyTools(pack *prompt.Pack) []string {
	_, runtimeOnly := splitBlocklistedTools(pack)
	return runtimeOnly
}

// splitBlocklistedTools partitions every blocklisted pack tool into those the
// gateway can refuse and those only the runtime can. Blocklist entries that
// name no pack tool are dropped: such a tool cannot be called anyway.
func splitBlocklistedTools(pack *prompt.Pack) (blocked, runtimeOnly []string) {
	if pack == nil {
		return nil, nil
	}
	candidates := map[string]bool{}
	for _, name := range policyResourceNames(pack) {
		for _, tool := range pack.Prompts[name].ToolPolicy.Blocklist {
			if _, ok := pack.Tools[tool]; ok {
				candidates[tool] = true
			}
		}
	}
	for tool := range candidates {
		if anyPromptMayCall(pack, tool) {
			runtimeOnly = append(runtimeOnly, tool)
		} else {
			blocked = append(blocked, tool)
		}
	}
	sort.Strings(blocked)
	sort.Strings(runtimeOnly)
	return blocked, runtimeOnly
}

// anyPromptMayCall reports whether some prompt of the pack can use tool and
// does not blocklist it.
func anyPromptMayCall(pack *prompt.Pack, tool string) bool {
	for _, p := range pack.Prompts {
		if p == nil {
			continue
		}
		if len(p.Tools) > 0 && !slices.Contains(p.Tools, tool) {
			continue
		}
		if p.ToolPolicy != nil && slices.Contains(p.ToolPolicy.Blocklist, tool) {
			continue
		}
		return true
	}
	return false
}

// packCedarStatements builds the statements of the pack's one policy engine:
// a forbid for each tool in gatewayBlockedTools and, when there is a gateway,
// the permit that lets every other tool through.
func packCedarStatements(pack *prompt.Pack, gatewayARN string) []string {
	blocked := gatewayBlockedTools(pack)
	if len(blocked) == 0 {
		return nil
	}
	return generateCedarStatements(nil, &prompt.ToolPolicyPack{Blocklist: blocked}, gatewayARN, nil)
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
