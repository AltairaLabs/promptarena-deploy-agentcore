package agentcore

import (
	"strings"
	"testing"

	"github.com/AltairaLabs/PromptKit/runtime/prompt"
	"github.com/AltairaLabs/PromptKit/runtime/validators"
)

func boolPtr(b bool) *bool { return &b }

// Validators should NOT produce Cedar — they are runtime-only.

func TestCedarValidatorsProduceNothing(t *testing.T) {
	vals := []prompt.ValidatorConfig{
		{
			ValidatorConfig: validators.ValidatorConfig{
				Type:   "banned_words",
				Params: map[string]interface{}{"words": []interface{}{"badword", "evil"}},
			},
		},
		{
			ValidatorConfig: validators.ValidatorConfig{
				Type:   "max_length",
				Params: map[string]interface{}{"max_characters": float64(500)},
			},
		},
		{
			ValidatorConfig: validators.ValidatorConfig{
				Type:   "regex_match",
				Params: map[string]interface{}{"pattern": `^[A-Z].*\.$`},
			},
		},
		{
			ValidatorConfig: validators.ValidatorConfig{
				Type:   "json_schema",
				Params: map[string]interface{}{"schema": map[string]interface{}{"type": "object"}},
			},
		},
	}
	blocks := generateCedarStatements(vals, nil, "", nil)
	if len(blocks) != 0 {
		t.Errorf("validators should not produce Cedar, got:\n%s", strings.Join(blocks, "\n\n"))
	}
}

// max_rounds and max_tool_calls_per_turn should NOT produce Cedar.

func TestCedarMaxRoundsProducesNothing(t *testing.T) {
	tp := &prompt.ToolPolicyPack{
		MaxRounds: 5,
	}
	blocks := generateCedarStatements(nil, tp, "", nil)
	if len(blocks) != 0 {
		t.Errorf("max_rounds should not produce Cedar, got:\n%s", strings.Join(blocks, "\n\n"))
	}
}

func TestCedarMaxToolCallsPerTurnProducesNothing(t *testing.T) {
	tp := &prompt.ToolPolicyPack{
		MaxToolCallsPerTurn: 3,
	}
	blocks := generateCedarStatements(nil, tp, "", nil)
	if len(blocks) != 0 {
		t.Errorf("max_tool_calls_per_turn should not produce Cedar, got:\n%s", strings.Join(blocks, "\n\n"))
	}
}

// Tool blocklist DOES produce Cedar with the correct AgentCore format.

func TestCedarToolBlocklist(t *testing.T) {
	tp := &prompt.ToolPolicyPack{
		Blocklist: []string{"dangerous_tool", "risky_tool"},
	}
	result := strings.Join(generateCedarStatements(nil, tp, "", nil), "\n\n")

	if !strings.Contains(result, `AgentCore::Action::"dangerous_tool__dangerous_tool"`) {
		t.Errorf("expected blocklist rule for dangerous_tool, got:\n%s", result)
	}
	if !strings.Contains(result, `AgentCore::Action::"risky_tool__risky_tool"`) {
		t.Errorf("expected blocklist rule for risky_tool, got:\n%s", result)
	}
	if !strings.Contains(result, "forbid") {
		t.Error("expected forbid keyword")
	}
}

func TestCedarToolBlocklistWithGatewayARN(t *testing.T) {
	tp := &prompt.ToolPolicyPack{
		Blocklist: []string{"exec"},
	}
	arn := "arn:aws:bedrock-agentcore:us-west-2:123456789012:gateway/my-gw"
	tools := map[string]bool{"exec": true}
	blocks := generateCedarStatements(nil, tp, arn, tools)

	if len(blocks) != 1 {
		t.Fatalf("expected 1 block, got %d", len(blocks))
	}
	if !strings.Contains(blocks[0], arn) {
		t.Errorf("expected gateway ARN in block, got:\n%s", blocks[0])
	}
	if !strings.Contains(blocks[0], `AgentCore::Gateway::`) {
		t.Errorf("expected AgentCore::Gateway:: resource, got:\n%s", blocks[0])
	}
}

func TestCedarToolBlocklistFiltersUnregisteredTools(t *testing.T) {
	tp := &prompt.ToolPolicyPack{
		Blocklist: []string{"exec", "shell", "search"},
	}
	arn := "arn:aws:bedrock-agentcore:us-west-2:123456789012:gateway/my-gw"
	// Only "search" is registered on the gateway.
	tools := map[string]bool{"search": true}
	blocks := generateCedarStatements(nil, tp, arn, tools)

	if len(blocks) != 1 {
		t.Fatalf("expected 1 block (only registered tool), got %d", len(blocks))
	}
	if !strings.Contains(blocks[0], `"search__search"`) {
		t.Errorf("expected search block, got:\n%s", blocks[0])
	}
}

func TestCedarMixed_OnlyBlocklistProducesCedar(t *testing.T) {
	vals := []prompt.ValidatorConfig{
		{
			ValidatorConfig: validators.ValidatorConfig{
				Type:   "banned_words",
				Params: map[string]interface{}{"words": []interface{}{"secret"}},
			},
		},
		{
			ValidatorConfig: validators.ValidatorConfig{
				Type:   "max_length",
				Params: map[string]interface{}{"max_characters": float64(1000)},
			},
		},
	}
	tp := &prompt.ToolPolicyPack{
		Blocklist: []string{"exec"},
		MaxRounds: 10,
	}
	result := strings.Join(generateCedarStatements(vals, tp, "", nil), "\n\n")

	// Only the blocklist entry should produce Cedar.
	if !strings.Contains(result, `AgentCore::Action::"exec__exec"`) {
		t.Errorf("expected blocklist rule for exec, got:\n%s", result)
	}

	// Validators and max_rounds should NOT appear.
	if strings.Contains(result, "context.output") {
		t.Errorf("validators should not produce Cedar, got:\n%s", result)
	}
	if strings.Contains(result, "round_count") {
		t.Errorf("max_rounds should not produce Cedar, got:\n%s", result)
	}
}

func TestCedarEmpty(t *testing.T) {
	blocks := generateCedarStatements(nil, nil, "", nil)
	if len(blocks) != 0 {
		t.Errorf("expected no blocks for no rules, got:\n%s", strings.Join(blocks, "\n\n"))
	}
}

func TestCedarEmptyToolPolicy(t *testing.T) {
	tp := &prompt.ToolPolicyPack{}
	blocks := generateCedarStatements(nil, tp, "", nil)
	if len(blocks) != 0 {
		t.Errorf("expected no blocks for empty tool policy, got:\n%s", strings.Join(blocks, "\n\n"))
	}
}

// hasPolicyRules should only return true for blocklist.

func TestHasPolicyRules_ValidatorsOnly(t *testing.T) {
	p := &prompt.PackPrompt{
		Validators: []prompt.ValidatorConfig{
			{ValidatorConfig: validators.ValidatorConfig{Type: "banned_words"}},
		},
	}
	if hasPolicyRules(p) {
		t.Error("validators alone should not trigger policy rules")
	}
}

func TestHasPolicyRules_MaxRoundsOnly(t *testing.T) {
	p := &prompt.PackPrompt{
		ToolPolicy: &prompt.ToolPolicyPack{MaxRounds: 5},
	}
	if hasPolicyRules(p) {
		t.Error("max_rounds alone should not trigger policy rules")
	}
}

func TestHasPolicyRules_BlocklistPresent(t *testing.T) {
	p := &prompt.PackPrompt{
		ToolPolicy: &prompt.ToolPolicyPack{Blocklist: []string{"bad"}},
	}
	if !hasPolicyRules(p) {
		t.Error("blocklist should trigger policy rules")
	}
}

func TestPolicyResourceNames(t *testing.T) {
	pack := &prompt.Pack{
		Prompts: map[string]*prompt.PackPrompt{
			"chat": {
				Validators: []prompt.ValidatorConfig{
					{ValidatorConfig: validators.ValidatorConfig{Type: "banned_words"}},
				},
			},
			"nopolicy": {},
			"tooled": {
				ToolPolicy: &prompt.ToolPolicyPack{
					Blocklist: []string{"bad"},
				},
			},
		},
	}

	names := policyResourceNames(pack)
	// Only "tooled" has a blocklist — "chat" only has validators (runtime-only).
	if len(names) != 1 {
		t.Fatalf("expected 1 name, got %d: %v", len(names), names)
	}
	if names[0] != "tooled" {
		t.Errorf("expected [tooled], got %v", names)
	}
}

func TestPolicyResourceNames_NoPolicies(t *testing.T) {
	pack := &prompt.Pack{
		Prompts: map[string]*prompt.PackPrompt{
			"chat": {},
		},
	}
	names := policyResourceNames(pack)
	if len(names) != 0 {
		t.Errorf("expected 0 names, got %d", len(names))
	}
}

func TestEscapeCedarString(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"hello", "hello"},
		{`say "hi"`, `say \"hi\"`},
		{`back\slash`, `back\\slash`},
	}
	for _, tt := range tests {
		got := escapeCedarString(tt.input)
		if got != tt.want {
			t.Errorf("escapeCedarString(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
