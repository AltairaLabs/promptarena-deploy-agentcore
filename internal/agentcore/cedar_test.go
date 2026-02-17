package agentcore

import (
	"strings"
	"testing"

	"github.com/AltairaLabs/PromptKit/runtime/prompt"
	"github.com/AltairaLabs/PromptKit/runtime/validators"
)

func boolPtr(b bool) *bool { return &b }

func TestCedarBannedWords(t *testing.T) {
	v := prompt.ValidatorConfig{
		ValidatorConfig: validators.ValidatorConfig{
			Type:   "banned_words",
			Params: map[string]interface{}{"words": []interface{}{"badword", "evil"}},
		},
	}
	result := generateCedarStatement([]prompt.ValidatorConfig{v}, nil)

	if !strings.Contains(result, `context.output like "*badword*"`) {
		t.Errorf("expected banned word 'badword' in output, got:\n%s", result)
	}
	if !strings.Contains(result, `context.output like "*evil*"`) {
		t.Errorf("expected banned word 'evil' in output, got:\n%s", result)
	}
	if !strings.Contains(result, "forbid") {
		t.Error("expected forbid keyword")
	}
}

func TestCedarBannedWords_ObserveOnly(t *testing.T) {
	v := prompt.ValidatorConfig{
		ValidatorConfig: validators.ValidatorConfig{
			Type:   "banned_words",
			Params: map[string]interface{}{"words": []interface{}{"test"}},
		},
		FailOnViolation: boolPtr(false),
	}
	result := generateCedarStatement([]prompt.ValidatorConfig{v}, nil)

	if !strings.Contains(result, "// observe-only") {
		t.Errorf("expected observe-only annotation, got:\n%s", result)
	}
}

func TestCedarMaxLength(t *testing.T) {
	v := prompt.ValidatorConfig{
		ValidatorConfig: validators.ValidatorConfig{
			Type:   "max_length",
			Params: map[string]interface{}{"max_characters": float64(500)},
		},
	}
	result := generateCedarStatement([]prompt.ValidatorConfig{v}, nil)

	if !strings.Contains(result, "context.output_length > 500") {
		t.Errorf("expected max_length rule, got:\n%s", result)
	}
}

func TestCedarRegexMatch(t *testing.T) {
	v := prompt.ValidatorConfig{
		ValidatorConfig: validators.ValidatorConfig{
			Type:   "regex_match",
			Params: map[string]interface{}{"pattern": `^[A-Z].*\.$`},
		},
	}
	result := generateCedarStatement([]prompt.ValidatorConfig{v}, nil)

	if !strings.Contains(result, "context.output.matches") {
		t.Errorf("expected regex_match rule, got:\n%s", result)
	}
}

func TestCedarJSONSchema(t *testing.T) {
	v := prompt.ValidatorConfig{
		ValidatorConfig: validators.ValidatorConfig{
			Type:   "json_schema",
			Params: map[string]interface{}{"schema": map[string]interface{}{"type": "object"}},
		},
	}
	result := generateCedarStatement([]prompt.ValidatorConfig{v}, nil)

	if !strings.Contains(result, "json_schema validation") {
		t.Errorf("expected json_schema placeholder, got:\n%s", result)
	}
}

func TestCedarToolBlocklist(t *testing.T) {
	tp := &prompt.ToolPolicyPack{
		Blocklist: []string{"dangerous_tool", "risky_tool"},
	}
	result := generateCedarStatement(nil, tp)

	if !strings.Contains(result, `resource.tool_name == "dangerous_tool"`) {
		t.Errorf("expected blocklist rule for dangerous_tool, got:\n%s", result)
	}
	if !strings.Contains(result, `resource.tool_name == "risky_tool"`) {
		t.Errorf("expected blocklist rule for risky_tool, got:\n%s", result)
	}
	if !strings.Contains(result, `Action::"invoke_tool"`) {
		t.Error("expected invoke_tool action")
	}
}

func TestCedarMaxRounds(t *testing.T) {
	tp := &prompt.ToolPolicyPack{
		MaxRounds: 5,
	}
	result := generateCedarStatement(nil, tp)

	if !strings.Contains(result, "context.round_count > 5") {
		t.Errorf("expected max_rounds rule, got:\n%s", result)
	}
	if !strings.Contains(result, `Action::"tool_loop_continue"`) {
		t.Error("expected tool_loop_continue action")
	}
}

func TestCedarMaxToolCallsPerTurn(t *testing.T) {
	tp := &prompt.ToolPolicyPack{
		MaxToolCallsPerTurn: 3,
	}
	result := generateCedarStatement(nil, tp)

	if !strings.Contains(result, "context.tool_calls_this_turn > 3") {
		t.Errorf("expected max_tool_calls_per_turn rule, got:\n%s", result)
	}
}

func TestCedarMixed(t *testing.T) {
	validators := []prompt.ValidatorConfig{
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
	result := generateCedarStatement(validators, tp)

	// All four rules should be present.
	checks := []string{
		`context.output like "*secret*"`,
		"context.output_length > 1000",
		`resource.tool_name == "exec"`,
		"context.round_count > 10",
	}
	for _, check := range checks {
		if !strings.Contains(result, check) {
			t.Errorf("expected %q in output, got:\n%s", check, result)
		}
	}
}

func TestCedarEmpty(t *testing.T) {
	result := generateCedarStatement(nil, nil)
	if result != "" {
		t.Errorf("expected empty string for no rules, got:\n%s", result)
	}
}

func TestCedarEmptyToolPolicy(t *testing.T) {
	tp := &prompt.ToolPolicyPack{}
	result := generateCedarStatement(nil, tp)
	if result != "" {
		t.Errorf("expected empty string for empty tool policy, got:\n%s", result)
	}
}

func TestCedarUnsupportedValidator(t *testing.T) {
	v := prompt.ValidatorConfig{
		ValidatorConfig: validators.ValidatorConfig{
			Type:   "custom_unknown",
			Params: map[string]interface{}{},
		},
	}
	result := generateCedarStatement([]prompt.ValidatorConfig{v}, nil)

	if !strings.Contains(result, "unsupported validator type: custom_unknown") {
		t.Errorf("expected unsupported comment, got:\n%s", result)
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
	if len(names) != 2 {
		t.Fatalf("expected 2 names, got %d: %v", len(names), names)
	}
	// Should be sorted.
	if names[0] != "chat" || names[1] != "tooled" {
		t.Errorf("expected [chat, tooled], got %v", names)
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

func TestEscapeCedarLike(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"hello", "hello"},
		{"hello*world", `hello\*world`},
		{`back\slash`, `back\\slash`},
	}
	for _, tt := range tests {
		got := escapeCedarLike(tt.input)
		if got != tt.want {
			t.Errorf("escapeCedarLike(%q) = %q, want %q", tt.input, got, tt.want)
		}
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

func TestCedarBannedWords_MissingParams(t *testing.T) {
	v := prompt.ValidatorConfig{
		ValidatorConfig: validators.ValidatorConfig{
			Type:   "banned_words",
			Params: map[string]interface{}{},
		},
	}
	result := generateCedarStatement([]prompt.ValidatorConfig{v}, nil)
	if result != "" {
		t.Errorf("expected empty for missing words param, got:\n%s", result)
	}
}

func TestCedarMaxLength_MissingParams(t *testing.T) {
	v := prompt.ValidatorConfig{
		ValidatorConfig: validators.ValidatorConfig{
			Type:   "max_length",
			Params: map[string]interface{}{},
		},
	}
	result := generateCedarStatement([]prompt.ValidatorConfig{v}, nil)
	if result != "" {
		t.Errorf("expected empty for missing max_characters param, got:\n%s", result)
	}
}

func TestCedarRegexMatch_MissingPattern(t *testing.T) {
	v := prompt.ValidatorConfig{
		ValidatorConfig: validators.ValidatorConfig{
			Type:   "regex_match",
			Params: map[string]interface{}{},
		},
	}
	result := generateCedarStatement([]prompt.ValidatorConfig{v}, nil)
	if result != "" {
		t.Errorf("expected empty for missing pattern param, got:\n%s", result)
	}
}
