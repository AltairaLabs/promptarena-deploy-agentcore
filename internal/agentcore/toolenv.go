package agentcore

import (
	"encoding/json"
	"log"
	"sort"

	"github.com/AltairaLabs/PromptKit/runtime/prompt"
)

// Environment variables describing how the runtime should execute each tool.
const (
	// EnvToolSpecs carries per-tool execution config as a JSON object keyed
	// by the pack's tool name.
	//
	// The compiled pack holds a tool's name, description and parameters and
	// nothing else, so how to actually run one — mock data, an HTTP URL, or
	// the Gateway target it lives behind — has to reach the runtime some
	// other way.
	EnvToolSpecs = "PROMPTPACK_TOOL_SPECS"

	// EnvToolGatewayURL is the Gateway's MCP endpoint.
	EnvToolGatewayURL = "PROMPTPACK_TOOL_GATEWAY_URL"

	// EnvToolGatewayAuth is how the Gateway authenticates callers, as
	// reported by the Gateway itself rather than assumed: AWS_IAM, CUSTOM_JWT,
	// AUTHENTICATE_ONLY or NONE.
	EnvToolGatewayAuth = "PROMPTPACK_TOOL_GATEWAY_AUTH"
)

// Tool execution modes the runtime understands.
const (
	toolModeMock    = "mock"
	toolModeLive    = "live"
	toolModeGateway = "gateway"

	// toolModeSchemaGateway marks a Gateway tool whose name comes from a
	// schema rather than from the pack — OpenAPI, Smithy or API Gateway. The
	// target is created and the tool is reachable through the Gateway, but
	// the runtime cannot yet map the pack's name onto the Gateway's.
	toolModeSchemaGateway = "gateway_schema"
)

// RuntimeToolSpec is the executable half of a tool definition.
//
// The pack carries the schema; this carries what to do when the model calls
// it. Both halves are needed to build a descriptor, and only the adapter sees
// both at deploy time.
type RuntimeToolSpec struct {
	Mode string `json:"mode"`

	// MockResult and MockTemplate back a mock tool. Template wins when both
	// are set, matching the arena's precedence.
	MockResult   any    `json:"mock_result,omitempty"`
	MockTemplate string `json:"mock_template,omitempty"`

	// URL and Method back a plain HTTP tool.
	URL    string `json:"url,omitempty"`
	Method string `json:"method,omitempty"`

	// GatewayTarget is the name this tool goes by inside the Gateway.
	//
	// Sent explicitly rather than re-derived in the runtime: the sanitiser
	// that produces it lives in the adapter, and a second copy would drift
	// from the first. Two implementations of one derived name is what made
	// plan and apply disagree about tool resources before.
	GatewayTarget string `json:"gateway_target,omitempty"`
}

// buildToolSpecs describes every tool in the pack for the runtime.
//
// Every tool, not only the interesting ones: supplying a tool registry means
// the runtime owns the whole tool surface, so a tool missing here is a tool the
// model never sees. That failure is silent, which is why this walks the pack
// rather than the arena config.
func buildToolSpecs(pack *prompt.Pack, cfg *Config) map[string]RuntimeToolSpec {
	if pack == nil || len(pack.Tools) == 0 {
		return nil
	}

	specs := make(map[string]RuntimeToolSpec, len(pack.Tools))
	names := make([]string, 0, len(pack.Tools))
	for name := range pack.Tools {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		specs[name] = runtimeSpecFor(name, cfg)
	}
	return specs
}

// runtimeSpecFor decides how one tool runs.
func runtimeSpecFor(name string, cfg *Config) RuntimeToolSpec {
	spec := cfg.ArenaConfig.toolSpecForName(name)

	// Only a Lambda target carries the pack's own tool name into the Gateway:
	// its inline schema is built from the pack tool, so the Gateway lists it
	// as <target>___<tool name>. An OpenAPI, Smithy or API Gateway target
	// takes its tool names from the schema or from path and method instead,
	// so the pack name is not what the Gateway knows it by and calling it
	// that way would miss. Those need their names discovered from the
	// Gateway, which this does not yet do.
	if spec != nil && spec.LambdaARN != "" {
		return RuntimeToolSpec{
			Mode:          toolModeGateway,
			GatewayTarget: sanitizeGatewayTargetName(name),
		}
	}
	if hasAWSTarget(spec) {
		return RuntimeToolSpec{Mode: toolModeSchemaGateway}
	}

	if spec == nil {
		// Declared in the pack with no execution config anywhere. Mock with
		// no data is what the arena does with the same input, and it fails
		// at call time with a message naming the tool rather than vanishing.
		return RuntimeToolSpec{Mode: toolModeMock}
	}

	if spec.HTTPConfig != nil && spec.HTTPConfig.URL != "" {
		return RuntimeToolSpec{
			Mode:   toolModeLive,
			URL:    spec.HTTPConfig.URL,
			Method: spec.HTTPConfig.Method,
		}
	}

	return RuntimeToolSpec{
		Mode:         toolModeMock,
		MockResult:   spec.MockResult,
		MockTemplate: spec.MockTemplate,
	}
}

// injectToolEnvVars adds the tool execution config to the runtime environment.
func injectToolEnvVars(env map[string]string, pack *prompt.Pack, cfg *Config) {
	specs := buildToolSpecs(pack, cfg)
	if len(specs) == 0 {
		return
	}

	b, err := json.Marshal(specs)
	if err != nil {
		// Nothing to fall back to: without this the deployed agent has no
		// tools at all, so say so rather than deploying a silently toolless
		// agent.
		log.Printf("agentcore: could not encode tool specs (%v) — "+
			"the deployed agent will have no tools", err)
		return
	}
	env[EnvToolSpecs] = string(b)

	if cfg.GatewayURL != "" {
		env[EnvToolGatewayURL] = cfg.GatewayURL
	}
	if cfg.GatewayAuth != "" {
		env[EnvToolGatewayAuth] = cfg.GatewayAuth
	}
}
