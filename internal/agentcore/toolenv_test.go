package agentcore

import (
	"encoding/json"
	"testing"

	"github.com/AltairaLabs/PromptKit/runtime/packspec"
	"github.com/AltairaLabs/PromptKit/runtime/prompt"
)

// packWithTools builds a pack declaring the named tools.
func packWithTools(names ...string) *prompt.Pack {
	pack := &prompt.Pack{Pack: packspec.Pack{ID: "p", Tools: map[string]*prompt.PackTool{}}}
	for _, n := range names {
		pack.Tools[n] = &prompt.PackTool{Name: n, Description: n}
	}
	return pack
}

// TestBuildToolSpecs_DescribesEveryPackTool is the adapter half of the
// every-tool-or-none rule.
//
// The runtime owns the whole tool surface once a registry is supplied, so a
// tool the adapter fails to describe is one the deployed agent cannot use.
func TestBuildToolSpecs_DescribesEveryPackTool(t *testing.T) {
	pack := packWithTools("lambda_tool", "mock_tool", "http_tool", "undeclared")
	cfg := &Config{ArenaConfig: arenaFromJSON(t, `{"tool_specs":{
		"lambda_tool":{"name":"lambda_tool","lambda_arn":"arn:aws:lambda:us-west-2:1:function:f"},
		"mock_tool":{"name":"mock_tool","mode":"mock","mock_result":{"ok":true}},
		"http_tool":{"name":"http_tool","http":{"url":"https://api.example.com/x"}}
	}}`)}

	specs := buildToolSpecs(pack, cfg)
	for name := range pack.Tools {
		if _, ok := specs[name]; !ok {
			t.Errorf("tool %q has no spec; the deployed agent could not use it", name)
		}
	}

	if got := specs["lambda_tool"].Mode; got != toolModeGateway {
		t.Errorf("lambda_tool mode = %q, want %q", got, toolModeGateway)
	}
	if specs["lambda_tool"].GatewayTarget == "" {
		t.Error("a gateway tool needs a target name the runtime can call")
	}
	if got := specs["http_tool"].Mode; got != toolModeLive {
		t.Errorf("http_tool mode = %q, want %q", got, toolModeLive)
	}
	if got := specs["mock_tool"].Mode; got != toolModeMock {
		t.Errorf("mock_tool mode = %q, want %q", got, toolModeMock)
	}
}

// TestToolSpecForName_ReadsFileDeclaredTools covers the tool source that used
// to be invisible.
//
// The arena writes an inline tool_specs entry into loaded_tools as well, but a
// `tools: [./x.tool.yaml]` reference lands only in loaded_tools. Reading
// tool_specs alone made those tools look undeclared, which took their mock
// data with them and left the agent unable to call them.
func TestToolSpecForName_ReadsFileDeclaredTools(t *testing.T) {
	manifest := []byte(`
kind: Tool
metadata:
  name: lookup_order
spec:
  name: lookup_order
  mode: mock
  mock_result:
    status: shipped
`)
	raw, err := json.Marshal(map[string]any{
		"loaded_tools": []map[string]any{{"file_path": "./lookup.tool.yaml", "data": manifest}},
	})
	if err != nil {
		t.Fatalf("marshal arena config: %v", err)
	}

	arena := arenaFromJSON(t, string(raw))
	spec := arena.toolSpecForName("lookup_order")
	if spec == nil {
		t.Fatal("a file-declared tool is invisible to the adapter")
	}
	if spec.Mode != "mock" {
		t.Errorf("mode = %q, want mock", spec.Mode)
	}
	if spec.MockResult == nil {
		t.Error("mock data did not survive; the tool would answer with nothing")
	}
}

// TestToolSpecForName_InlineWinsOverFile pins the precedence, since the arena
// copies an inline spec into both places.
func TestToolSpecForName_InlineWinsOverFile(t *testing.T) {
	manifest := []byte("kind: Tool\nspec:\n  name: t\n  mode: mock\n  mock_template: from-file\n")
	raw, _ := json.Marshal(map[string]any{
		"tool_specs": map[string]any{
			"t": map[string]any{"name": "t", "mode": "mock", "mock_template": "from-inline"},
		},
		"loaded_tools": []map[string]any{{"data": manifest}},
	})

	spec := arenaFromJSON(t, string(raw)).toolSpecForName("t")
	if spec == nil || spec.MockTemplate != "from-inline" {
		t.Errorf("inline spec should win, got %+v", spec)
	}
}

// TestGatewayToolNames_OnlyAWSBackedTools keeps mock and HTTP tools out of the
// Gateway, which cannot route to them.
func TestGatewayToolNames_OnlyAWSBackedTools(t *testing.T) {
	pack := packWithTools("lambda_tool", "mock_tool")
	cfg := &Config{ArenaConfig: arenaFromJSON(t, `{"tool_specs":{
		"lambda_tool":{"name":"lambda_tool","lambda_arn":"arn:aws:lambda:us-west-2:1:function:f"},
		"mock_tool":{"name":"mock_tool","mode":"mock","mock_result":{"ok":true}}
	}}`)}

	got := gatewayToolNames(pack, cfg)
	if len(got) != 1 || got[0] != "lambda_tool" {
		t.Errorf("gatewayToolNames = %v, want [lambda_tool]", got)
	}
}

// TestInjectToolEnvVars_CarriesTheGatewayEndpoint checks the runtime is told
// where its gateway tools live, since it cannot call them otherwise.
func TestInjectToolEnvVars_CarriesTheGatewayEndpoint(t *testing.T) {
	pack := packWithTools("lambda_tool")
	cfg := &Config{
		ArenaConfig: arenaFromJSON(t, `{"tool_specs":{
			"lambda_tool":{"name":"lambda_tool","lambda_arn":"arn:aws:lambda:us-west-2:1:function:f"}
		}}`),
		GatewayURL:  "https://gw.gateway.bedrock-agentcore.us-west-2.amazonaws.com",
		GatewayAuth: "AWS_IAM",
	}

	env := map[string]string{}
	injectToolEnvVars(env, pack, cfg)

	if env[EnvToolSpecs] == "" {
		t.Fatal("no tool specs were injected")
	}
	if env[EnvToolGatewayURL] != cfg.GatewayURL {
		t.Errorf("gateway url = %q, want %q", env[EnvToolGatewayURL], cfg.GatewayURL)
	}
	if env[EnvToolGatewayAuth] != "AWS_IAM" {
		t.Errorf("gateway auth = %q, want AWS_IAM", env[EnvToolGatewayAuth])
	}

	var specs map[string]RuntimeToolSpec
	if err := json.Unmarshal([]byte(env[EnvToolSpecs]), &specs); err != nil {
		t.Fatalf("injected specs do not decode: %v", err)
	}
	if specs["lambda_tool"].GatewayTarget == "" {
		t.Error("the runtime was not told which target to call")
	}
}

// TestValidateToolTargetKinds_RefusesUnaddressableTargets covers the Gateway
// target types whose tool names the runtime cannot work out.
//
// The Gateway derives names from the schema for these, so the pack's name is
// not what it answers to. Creating the target anyway produced an agent whose
// tool call always missed, which is worse than refusing the deploy.
func TestValidateToolTargetKinds_RefusesUnaddressableTargets(t *testing.T) {
	for _, tt := range []struct {
		name   string
		spec   string
		refuse bool
	}{
		{"lambda", `{"name":"t","lambda_arn":"arn:aws:lambda:us-west-2:1:function:f"}`, false},
		{"openapi", `{"name":"t","openapi":{"s3_uri":"s3://b/k"}}`, true},
		{"smithy", `{"name":"t","smithy":{"s3_uri":"s3://b/k"}}`, true},
		{"api gateway", `{"name":"t","api_gateway":{"rest_api_id":"x","stage":"prod"}}`, true},
		{"mock", `{"name":"t","mode":"mock","mock_result":{"ok":true}}`, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{ArenaConfig: arenaFromJSON(t, `{"tool_specs":{"t":`+tt.spec+`}}`)}
			errs := validateToolTargetKinds(packWithTools("t"), cfg)
			if tt.refuse && len(errs) == 0 {
				t.Error("expected the deploy to be refused")
			}
			if !tt.refuse && len(errs) > 0 {
				t.Errorf("expected no error, got %v", errs)
			}
		})
	}
}
