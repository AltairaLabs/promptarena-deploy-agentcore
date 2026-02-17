package agentcore

import (
	"fmt"
	"sort"
	"strings"

	"github.com/AltairaLabs/PromptKit/runtime/prompt"
)

// generateCedarStatement builds a single Cedar policy statement from the
// given validators and tool policy. Returns an empty string if there are
// no rules to generate.
func generateCedarStatement(
	validators []prompt.ValidatorConfig,
	tp *prompt.ToolPolicyPack,
) string {
	var blocks []string

	for _, v := range validators {
		block := cedarFromValidator(v)
		if block != "" {
			blocks = append(blocks, block)
		}
	}

	if tp != nil {
		blocks = append(blocks, cedarFromToolPolicy(tp)...)
	}

	if len(blocks) == 0 {
		return ""
	}

	return strings.Join(blocks, "\n\n")
}

// cedarFromValidator dispatches to the appropriate Cedar generator based
// on validator type.
func cedarFromValidator(v prompt.ValidatorConfig) string {
	annotation := cedarAnnotation(v.FailOnViolation)

	switch v.Type {
	case "banned_words":
		return cedarBannedWords(v.Params, annotation)
	case "max_length":
		return cedarMaxLength(v.Params, annotation)
	case "regex_match":
		return cedarRegexMatch(v.Params, annotation)
	case "json_schema":
		return cedarJSONSchema(v.Params, annotation)
	default:
		return fmt.Sprintf(
			"%s// unsupported validator type: %s",
			annotation, v.Type,
		)
	}
}

// cedarAnnotation returns a comment prefix for observe-only rules.
func cedarAnnotation(failOnViolation *bool) string {
	if failOnViolation != nil && !*failOnViolation {
		return "// observe-only\n"
	}
	return ""
}

// cedarBannedWords generates a forbid block for each banned word.
func cedarBannedWords(params map[string]interface{}, annotation string) string {
	wordsRaw, ok := params["words"]
	if !ok {
		return ""
	}
	wordSlice, ok := wordsRaw.([]interface{})
	if !ok {
		return ""
	}

	var blocks []string
	for _, w := range wordSlice {
		word, ok := w.(string)
		if !ok {
			continue
		}
		blocks = append(blocks, fmt.Sprintf(
			`%sforbid (principal, action, resource)
when { context.output like "*%s*" };`,
			annotation, escapeCedarLike(word),
		))
	}
	return strings.Join(blocks, "\n\n")
}

// cedarMaxLength generates a forbid block for output length limits.
func cedarMaxLength(params map[string]interface{}, annotation string) string {
	maxChars, ok := extractInt(params, "max_characters")
	if !ok {
		return ""
	}
	return fmt.Sprintf(
		`%sforbid (principal, action, resource)
when { context.output_length > %d };`,
		annotation, maxChars,
	)
}

// cedarRegexMatch generates a forbid block for regex pattern violations.
func cedarRegexMatch(params map[string]interface{}, annotation string) string {
	pattern, ok := params["pattern"].(string)
	if !ok || pattern == "" {
		return ""
	}
	return fmt.Sprintf(
		`%sforbid (principal, action, resource)
when { !context.output.matches("%s") };`,
		annotation, escapeCedarString(pattern),
	)
}

// cedarJSONSchema generates a comment-only placeholder since Cedar has no
// native JSON Schema support.
func cedarJSONSchema(_ map[string]interface{}, annotation string) string {
	return fmt.Sprintf(
		`%s// json_schema validation: enforced at runtime (no native Cedar support)`,
		annotation,
	)
}

// cedarFromToolPolicy generates Cedar forbid blocks from tool policy fields.
func cedarFromToolPolicy(tp *prompt.ToolPolicyPack) []string {
	var blocks []string

	for _, tool := range tp.Blocklist {
		blocks = append(blocks, cedarToolBlocklist(tool))
	}

	if tp.MaxRounds > 0 {
		blocks = append(blocks, cedarMaxRounds(tp.MaxRounds))
	}

	if tp.MaxToolCallsPerTurn > 0 {
		blocks = append(blocks, cedarMaxToolCallsPerTurn(tp.MaxToolCallsPerTurn))
	}

	return blocks
}

// cedarToolBlocklist generates a forbid block for a blocked tool.
func cedarToolBlocklist(toolName string) string {
	return fmt.Sprintf(
		`forbid (principal, action == Action::"invoke_tool", resource)
when { resource.tool_name == "%s" };`,
		escapeCedarString(toolName),
	)
}

// cedarMaxRounds generates a forbid block for max agentic loop rounds.
func cedarMaxRounds(n int) string {
	return fmt.Sprintf(
		`forbid (principal, action == Action::"tool_loop_continue", resource)
when { context.round_count > %d };`,
		n,
	)
}

// cedarMaxToolCallsPerTurn generates a forbid block for max tool calls per turn.
func cedarMaxToolCallsPerTurn(n int) string {
	return fmt.Sprintf(
		`forbid (principal, action == Action::"invoke_tool", resource)
when { context.tool_calls_this_turn > %d };`,
		n,
	)
}

// policyResourceNames returns a sorted list of prompt names that have
// validators or tool_policy defined. Used by both plan and apply.
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

// hasPolicyRules returns true if the prompt has validators or a tool policy.
func hasPolicyRules(p *prompt.PackPrompt) bool {
	if len(p.Validators) > 0 {
		return true
	}
	if p.ToolPolicy != nil {
		tp := p.ToolPolicy
		if len(tp.Blocklist) > 0 || tp.MaxRounds > 0 || tp.MaxToolCallsPerTurn > 0 {
			return true
		}
	}
	return false
}

// escapeCedarLike escapes characters that have special meaning in Cedar
// like-expressions (wildcards * and %).
func escapeCedarLike(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `*`, `\*`)
	return s
}

// escapeCedarString escapes double quotes and backslashes in Cedar strings.
func escapeCedarString(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return s
}

// extractInt extracts an integer value from a map[string]interface{}.
// JSON numbers are typically decoded as float64.
func extractInt(params map[string]interface{}, key string) (int, bool) {
	v, ok := params[key]
	if !ok {
		return 0, false
	}
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	default:
		return 0, false
	}
}
