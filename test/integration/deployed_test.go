//go:build integration

// Package integration holds tests that deploy to a real AWS account.
//
// They are excluded from normal builds by the integration build tag and skip
// unless AGENTCORE_TEST_REGION, AGENTCORE_TEST_ROLE_ARN and
// AGENTCORE_TEST_BINARY_PATH are set. Running them creates billable Bedrock
// AgentCore runtimes; each test deletes what it created, including on failure.
//
// These replace an older set that lived in internal/agentcore and had drifted:
// they loaded a pack fixture from a sibling repository by relative path, and
// the invocation test required a runtime someone had already deployed by hand,
// so it skipped in every automated run. This suite deploys what it invokes and
// carries its own fixtures, matching the vertex and foundry suites.
package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/bedrockagentcore"

	"github.com/AltairaLabs/promptarena/deploy"

	"github.com/AltairaLabs/promptarena-deploy-agentcore/internal/agentcore"
)

const (
	envRegion    = "AGENTCORE_TEST_REGION"
	envRoleARN   = "AGENTCORE_TEST_ROLE_ARN"
	envBinary    = "AGENTCORE_TEST_BINARY_PATH"
	envModel     = "AGENTCORE_TEST_MODEL"
	envEvalModel = "AGENTCORE_TEST_EVAL_MODEL"
	envLambdaARN = "AGENTCORE_TEST_LAMBDA_ARN"
)

// defaultModel is the Bedrock model the deployed agent talks to.
//
// A Bedrock model id, not a vendor model name: this reaches bedrock-runtime
// verbatim, and a vendor name comes back "The provided model identifier is
// invalid" on the first turn. Claude 4.5 is served on demand through a
// cross-region inference profile, which is what the "us." prefix is.
//
// Nothing checks this before the deploy — only eval models are preflighted —
// so an id that is wrong here costs a full apply before it surfaces.
const defaultModel = "us.anthropic.claude-haiku-4-5-20251001-v1:0"

// defaultEvalModel is the full Bedrock id for the judge eval. Judge evals take
// a model id rather than a short name.
const defaultEvalModel = "anthropic.claude-haiku-4-5-20251001-v1:0"

// applyTimeout bounds a full deploy. A code deploy uploads a ~20MB package and
// waits for the runtime to reach READY, which is minutes rather than seconds.
const applyTimeout = 25 * time.Minute

// invokeTimeout is generous: the first call to a fresh runtime cold-starts it.
const invokeTimeout = 300 * time.Second

// featurePack is the pack most tests deploy. It has no tools, and that is a
// platform difference rather than a gap in coverage.
//
// vertex and foundry execute tools inside the runtime, so their suites use a
// mock tool spec and assert the model called it. AgentCore routes tools through
// a Gateway whose targets must point at real infrastructure — a Lambda, an API
// Gateway, an OpenAPI or Smithy document, or an MCP server URL. A mock has none
// of those, so it reaches buildMCPServerTargetConfig and the API rejects it:
//
//	ValidationException: The MCP server endpoint URL is malformed or has no
//	extractable host.
//
// Tool coverage therefore needs a real target, which TestDeployed_ToolCalling
// asks for by environment and skips without.
const featurePack = `{
  "$schema": "https://promptpack.org/schema/latest/promptpack.schema.json",
  "id": "agentcore-integration",
  "name": "AgentCore Integration Pack",
  "version": "1.0.0",
  "template_engine": { "version": "v1", "syntax": "{{variable}}" },
  "prompts": {
    "main": {
      "id": "main",
      "name": "Support Agent",
      "version": "1.0.0",
      "system_template": "You are a terse support agent. Answer in one short sentence."
    }
  }
}`

// toolPack adds a tool for the one test that has somewhere real to point it.
const toolPack = `{
  "$schema": "https://promptpack.org/schema/latest/promptpack.schema.json",
  "id": "agentcore-integration",
  "name": "AgentCore Integration Pack",
  "version": "1.0.0",
  "template_engine": { "version": "v1", "syntax": "{{variable}}" },
  "prompts": {
    "main": {
      "id": "main",
      "name": "Support Agent",
      "version": "1.0.0",
      "system_template": "You are a terse support agent. Answer in one short sentence.",
      "tools": ["lookup_order"]
    }
  },
  "tools": {
    "lookup_order": {
      "name": "lookup_order",
      "description": "Look up an order by its id",
      "parameters": {
        "type": "object",
        "properties": { "order_id": { "type": "string", "description": "The order id" } },
        "required": ["order_id"]
      }
    }
  }
}`

// featureArena is the arena config the CLI would hand the adapter.
//
// The type is the model's vendor, not Bedrock. Bedrock is a hosting platform
// and the runtime applies it separately, from the deployment region — writing
// "bedrock" here yields an agent that deploys, reaches ready, and then fails
// its first turn with "unsupported provider type".
const featureArena = `{
  "loaded_providers": {
    "integration-llm": {
      "id": "integration-llm",
      "type": "claude",
      "model": "` + defaultModel + `"
    }
  }
}`

// mockToolArena gives the tool a canned answer and no AWS target, so the
// runtime executes it and the Gateway never hears about it.
func mockToolArena() string {
	return `{
  "loaded_providers": {
    "integration-llm": {
      "id": "integration-llm",
      "type": "claude",
      "model": "` + defaultModel + `"
    }
  },
  "tool_specs": {
    "lookup_order": {
      "name": "lookup_order",
      "mode": "mock",
      "mock_result": {"order_id": "A-4471", "status": "shipped"}
    }
  }
}`
}

// toolArena points the tool at a real Lambda, supplied by the environment.
func toolArena(lambdaARN string) string {
	return `{
  "loaded_providers": {
    "integration-llm": {
      "id": "integration-llm",
      "type": "claude",
      "model": "` + defaultModel + `"
    }
  },
  "tool_specs": {
    "lookup_order": { "name": "lookup_order", "lambda_arn": "` + lambdaARN + `" }
  }
}`
}

// testEnv holds the resolved configuration for a run.
type testEnv struct {
	Region     string
	RoleARN    string
	BinaryPath string
	Model      string
	EvalModel  string
}

// requireEnv skips the test unless the required variables are present, so a
// plain `go test ./...` can never create billable resources.
func requireEnv(t *testing.T) testEnv {
	t.Helper()

	region := os.Getenv(envRegion)
	roleARN := os.Getenv(envRoleARN)
	binary := os.Getenv(envBinary)
	if region == "" || roleARN == "" || binary == "" {
		t.Skipf("set %s, %s and %s to run deployed integration tests",
			envRegion, envRoleARN, envBinary)
	}
	if _, err := os.Stat(binary); err != nil {
		t.Fatalf("%s=%s is not readable: %v", envBinary, binary, err)
	}

	return testEnv{
		Region:     region,
		RoleARN:    roleARN,
		BinaryPath: binary,
		Model:      envOr(envModel, defaultModel),
		EvalModel:  envOr(envEvalModel, defaultEvalModel),
	}
}

func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

// deployConfig builds the adapter's deploy config JSON.
//
// memory_store is on because AgentCore Memory is what makes a conversation
// carry between turns, and that is a behaviour worth proving rather than
// assuming.
func deployConfig(t *testing.T, env testEnv) string {
	t.Helper()

	cfg := map[string]any{
		"region":              env.Region,
		"runtime_role_arn":    env.RoleARN,
		"runtime_binary_path": env.BinaryPath,
		"memory_store":        "semantic",
		"providers": []map[string]string{
			{"name": "default", "role": agentcore.RoleLLM, "arena_provider": "integration-llm"},
		},
	}
	encoded, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal deploy config: %v", err)
	}
	return string(encoded)
}

// packNamed returns the feature pack with a unique id, which seeds the runtime
// name. Every test needs its own: sharing one means one test's teardown races
// the next one's create, and the failure lands on whichever test ran second.
func packNamed(t *testing.T) string {
	t.Helper()

	id := strings.ToLower(t.Name())
	for _, cut := range []string{"test", "deployed"} {
		id = strings.ReplaceAll(id, cut, "")
	}
	id = strings.Trim(strings.ReplaceAll(id, "__", "_"), "_")
	if len(id) > 40 {
		id = id[:40]
	}
	// AgentCore runtime names allow letters, digits and underscores only.
	id = strings.ReplaceAll(id, "-", "_")
	return packFrom(t, featurePack)
}

// packFrom gives a pack a unique id derived from the running test.
//
// Hyphenated, deliberately. The pack schema requires ^[a-z][a-z0-9-]*$ and the
// runtime refuses a pack that fails it, while AWS resource names take
// underscores and refuse hyphens — so the adapter has to translate. Using the
// form a pack author would actually write keeps that translation covered on
// every run rather than only in unit tests.
func packFrom(t *testing.T, pack string) string {
	t.Helper()

	id := strings.ToLower(t.Name())
	for _, cut := range []string{"test", "deployed", "_"} {
		id = strings.ReplaceAll(id, cut, "")
	}
	id = strings.Trim(id, "-")
	if len(id) > 36 {
		id = strings.Trim(id[:36], "-")
	}
	return strings.Replace(pack, `"id": "agentcore-integration"`, `"id": "it-`+id+`"`, 1)
}

// stateShape is the subset of adapter state these tests read.
//
// Tests that re-apply must pass the ORIGINAL state string as PriorState, never
// a re-encode of this struct: it omits the hashes, and without them the adapter
// sees a changed pack and redeploys.
type stateShape struct {
	PackID    string `json:"pack_id"`
	Resources []struct {
		Type   string `json:"type"`
		Name   string `json:"name"`
		ARN    string `json:"arn"`
		Status string `json:"status"`
	} `json:"resources"`
}

func parseState(t *testing.T, state string) stateShape {
	t.Helper()

	var parsed stateShape
	if err := json.Unmarshal([]byte(state), &parsed); err != nil {
		t.Fatalf("parse adapter state: %v", err)
	}
	return parsed
}

// runtimeARN pulls the deployed agent runtime's ARN out of state.
func runtimeARN(t *testing.T, state string) string {
	t.Helper()

	for _, r := range parseState(t, state).Resources {
		if r.Type == agentcore.ResTypeAgentRuntime && r.ARN != "" {
			return r.ARN
		}
	}
	t.Fatalf("state records no agent runtime ARN: %s", state)
	return ""
}

// applyPack deploys packJSON and returns the raw state, registering cleanup
// that destroys it even when the test fails.
func applyPack(t *testing.T, env testEnv, packJSON string) string {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), applyTimeout)
	defer cancel()

	cfgJSON := deployConfig(t, env)
	state, err := agentcore.NewProvider().Apply(ctx, &deploy.PlanRequest{
		PackJSON:     packJSON,
		DeployConfig: cfgJSON,
		ArenaConfig:  featureArena,
	}, nil)
	if err != nil {
		// State comes back even on partial failure, so clean up what landed.
		if state != "" {
			destroyQuietly(t, cfgJSON, state)
		}
		t.Fatalf("Apply: %v", err)
	}

	t.Cleanup(func() { destroyQuietly(t, cfgJSON, state) })
	return state
}

// destroyQuietly tears down whatever the state records, reporting failures
// loudly enough that a leaked billable runtime is not missed.
func destroyQuietly(t *testing.T, cfgJSON, state string) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), applyTimeout)
	defer cancel()

	if err := agentcore.NewProvider().Destroy(ctx, &deploy.DestroyRequest{
		DeployConfig: cfgJSON,
		PriorState:   state,
	}, nil); err != nil {
		t.Errorf("cleanup: destroy failed (%v) — CHECK THE AWS CONSOLE FOR LEAKED RESOURCES", err)
	}
}

// ask invokes the deployed runtime and returns the text it answered with.
//
// session binds a conversation: AgentCore Memory keys off the runtime session
// id, so two calls sharing one should share history.
func ask(t *testing.T, env testEnv, arn, session, message string) string {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), invokeTimeout)
	defer cancel()

	cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(env.Region))
	if err != nil {
		t.Fatalf("aws config: %v", err)
	}

	payload, err := json.Marshal(map[string]any{"prompt": message})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	resp, err := bedrockagentcore.NewFromConfig(cfg).InvokeAgentRuntime(ctx,
		&bedrockagentcore.InvokeAgentRuntimeInput{
			AgentRuntimeArn:  aws.String(arn),
			RuntimeSessionId: aws.String(session),
			Payload:          payload,
			ContentType:      aws.String("application/json"),
			Accept:           aws.String("application/json"),
		})
	if err != nil {
		t.Fatalf("InvokeAgentRuntime: %v", err)
	}
	defer func() { _ = resp.Response.Close() }()

	body, err := io.ReadAll(resp.Response)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("response is not JSON: %v\nbody: %.500s", err, body)
	}
	if status, _ := result["status"].(string); status == "error" {
		t.Fatalf("runtime returned an error: %v", result["response"])
	}

	text := responseText(result)
	if text == "" {
		t.Fatalf("no text in response: %.500s", body)
	}
	return text
}

// responseText pulls the answer out of the shapes the bridge can return.
func responseText(result map[string]any) string {
	for _, key := range []string{"response", "output", "content"} {
		if s, ok := result[key].(string); ok && s != "" {
			return s
		}
	}
	return ""
}

// minSessionIDLen is the shortest runtimeSessionId AgentCore accepts.
//
// Undocumented anywhere the adapter can see, and enforced: a shorter id comes
// back "Member must have length greater than or equal to 33" from
// InvokeAgentRuntime, after the deploy has already succeeded.
const minSessionIDLen = 33

// newSession returns a session id unique to this run, so a rerun never reuses
// a conversation from the last one, padded to the length AgentCore requires.
func newSession(prefix string) string {
	id := fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
	if len(id) < minSessionIDLen {
		id += strings.Repeat("0", minSessionIDLen-len(id))
	}
	return id
}

// --- Deploy lifecycle -------------------------------------------------------

// TestDeployed_ApplyCreatesRuntime is the base case: a deploy must leave an
// agent runtime with an ARN. Everything else here builds on it.
func TestDeployed_ApplyCreatesRuntime(t *testing.T) {
	env := requireEnv(t)

	state := parseState(t, applyPack(t, env, packNamed(t)))

	var found bool
	for _, r := range state.Resources {
		if r.Type == agentcore.ResTypeAgentRuntime {
			found = true
			if r.ARN == "" {
				t.Errorf("agent runtime %q has no ARN", r.Name)
			}
			if r.Status == "failed" {
				t.Errorf("agent runtime %q came back failed", r.Name)
			}
		}
	}
	if !found {
		t.Fatalf("no agent runtime in state: %+v", state.Resources)
	}
}

// TestDeployed_StatusReportsDeployed checks the adapter's own view agrees with
// AWS's. Apply succeeding says nothing about whether the runtime is healthy.
func TestDeployed_StatusReportsDeployed(t *testing.T) {
	env := requireEnv(t)
	state := applyPack(t, env, packNamed(t))

	ctx, cancel := context.WithTimeout(context.Background(), applyTimeout)
	defer cancel()

	resp, err := agentcore.NewProvider().Status(ctx, &deploy.StatusRequest{
		DeployConfig: deployConfig(t, env),
		PriorState:   state,
	})
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if resp.Status != "deployed" {
		t.Errorf("Status = %q, want deployed (resources: %+v)", resp.Status, resp.Resources)
	}
	// The console link and the invoke command are the whole "how do I use this"
	// story for an adapter with no HTTPS endpoint.
	var linked bool
	for _, r := range resp.Resources {
		if len(r.Links) > 0 {
			linked = true
		}
	}
	if !linked {
		t.Error("Status carried no links; there is no way to reach the console from here")
	}
}

// --- Invocation -------------------------------------------------------------

// TestDeployed_UnaryInvocation proves the deployed runtime serves the HTTP
// bridge and the pack's system prompt reached the model.
func TestDeployed_UnaryInvocation(t *testing.T) {
	env := requireEnv(t)
	arn := runtimeARN(t, applyPack(t, env, packNamed(t)))

	answer := ask(t, env, arn, newSession("unary"),
		"Say the word acknowledged and nothing else.")

	if !strings.Contains(strings.ToLower(answer), "acknowledged") {
		t.Errorf("answer %q does not contain the requested word", answer)
	}
}

// TestDeployed_ToolCalling exercises a tool the runtime executes itself.
//
// A mock tool needs no AWS tool infrastructure: it declares no Lambda, so no
// Gateway target is created for it and the runtime answers the call in
// process. That makes tool calling testable on every run rather than only
// where someone has staged a Lambda.
//
// TestDeployed_GatewayToolCalling covers the other path, which does need one.
func TestDeployed_ToolCalling(t *testing.T) {
	env := requireEnv(t)

	ctx, cancel := context.WithTimeout(context.Background(), applyTimeout)
	defer cancel()

	cfgJSON := deployConfig(t, env)
	state, err := agentcore.NewProvider().Apply(ctx, &deploy.PlanRequest{
		PackJSON:     packFrom(t, toolPack),
		DeployConfig: cfgJSON,
		ArenaConfig:  mockToolArena(),
	}, nil)
	if err != nil {
		if state != "" {
			destroyQuietly(t, cfgJSON, state)
		}
		t.Fatalf("Apply with a tool: %v", err)
	}
	t.Cleanup(func() { destroyQuietly(t, cfgJSON, state) })

	answer := ask(t, env, runtimeARN(t, state), newSession("tool"),
		"What is the status of order A-4471? Use the lookup_order tool.")
	t.Logf("answer: %s", answer)

	// The mock answers "shipped", so a turn that used the tool says so and a
	// turn that ignored it cannot.
	if !strings.Contains(strings.ToLower(answer), "shipped") {
		t.Errorf("answer %q does not carry the tool result; the tool was not called", answer)
	}
}

// TestDeployed_GatewayToolCalling exercises a tool behind an AgentCore Gateway.
//
// This one cannot be faked — a Gateway target points at real infrastructure by
// definition — so it needs a Lambda the runtime role can invoke.
func TestDeployed_GatewayToolCalling(t *testing.T) {
	env := requireEnv(t)

	lambdaARN := os.Getenv(envLambdaARN)
	if lambdaARN == "" {
		t.Skipf("set %s to a Lambda the runtime role can invoke to exercise the gateway path",
			envLambdaARN)
	}

	ctx, cancel := context.WithTimeout(context.Background(), applyTimeout)
	defer cancel()

	cfgJSON := deployConfig(t, env)
	state, err := agentcore.NewProvider().Apply(ctx, &deploy.PlanRequest{
		PackJSON:     packFrom(t, toolPack),
		DeployConfig: cfgJSON,
		ArenaConfig:  toolArena(lambdaARN),
	}, nil)
	if err != nil {
		if state != "" {
			destroyQuietly(t, cfgJSON, state)
		}
		t.Fatalf("Apply with a gateway tool: %v", err)
	}
	t.Cleanup(func() { destroyQuietly(t, cfgJSON, state) })

	answer := ask(t, env, runtimeARN(t, state), newSession("gwtool"),
		"What is the status of order A-4471? Use the lookup_order tool.")
	t.Logf("answer: %s", answer)

	if answer == "" {
		t.Error("no answer from a gateway tool-calling turn")
	}
}

// TestDeployed_MemoryCarriesConversation is where agentcore differs from its
// siblings. vertex and foundry open a fresh conversation per request and keep
// no store, so their suites pin that turns are independent. This adapter
// configures AgentCore Memory, so a session should remember.
//
// If this starts failing, the memory wiring has broken — not the test.
func TestDeployed_MemoryCarriesConversation(t *testing.T) {
	env := requireEnv(t)
	arn := runtimeARN(t, applyPack(t, env, packNamed(t)))
	session := newSession("memory")

	first := ask(t, env, arn, session, "Remember this number: 8675309. Just acknowledge it.")
	t.Logf("turn 1: %s", first)

	second := ask(t, env, arn, session, "What number did I ask you to remember?")
	t.Logf("turn 2: %s", second)

	if !strings.Contains(second, "8675309") {
		t.Errorf("second turn %q did not recall the first; the session did not carry", second)
	}
}

// --- Re-apply and drift -----------------------------------------------------

// TestDeployed_ReapplyIsIdempotent re-applies the same pack and config. An
// unchanged deploy must not churn the runtime, or every no-op costs a rollout.
func TestDeployed_ReapplyIsIdempotent(t *testing.T) {
	env := requireEnv(t)
	pack := packNamed(t)
	firstRaw := applyPack(t, env, pack)
	firstARN := runtimeARN(t, firstRaw)

	ctx, cancel := context.WithTimeout(context.Background(), applyTimeout)
	defer cancel()

	second, err := agentcore.NewProvider().Apply(ctx, &deploy.PlanRequest{
		PackJSON:     pack,
		DeployConfig: deployConfig(t, env),
		ArenaConfig:  featureArena,
		PriorState:   firstRaw,
	}, nil)
	if err != nil {
		t.Fatalf("second Apply: %v", err)
	}

	if got := runtimeARN(t, second); got != firstARN {
		t.Errorf("runtime ARN changed on re-apply: %q -> %q", firstARN, got)
	}
}

// TestDeployed_DriftIsDetected destroys the deployment behind the adapter's
// back and checks Plan notices. This is the case the shared drift contract
// exists for, and the only place it is proven against a real control plane
// rather than a fake that answers however we told it to.
func TestDeployed_DriftIsDetected(t *testing.T) {
	env := requireEnv(t)
	pack := packNamed(t)
	cfgJSON := deployConfig(t, env)
	state := applyPack(t, env, pack)

	ctx, cancel := context.WithTimeout(context.Background(), applyTimeout)
	defer cancel()

	provider := agentcore.NewProvider()
	if err := provider.Destroy(ctx, &deploy.DestroyRequest{
		DeployConfig: cfgJSON, PriorState: state,
	}, nil); err != nil {
		t.Fatalf("out-of-band destroy: %v", err)
	}

	plan, err := provider.Plan(ctx, &deploy.PlanRequest{
		PackJSON:     pack,
		DeployConfig: cfgJSON,
		ArenaConfig:  featureArena,
		PriorState:   state,
	})
	if err != nil {
		t.Fatalf("Plan after out-of-band destroy: %v", err)
	}

	var sawDrift, sawCreate bool
	for _, c := range plan.Changes {
		switch c.Action {
		case deploy.ActionDrift:
			sawDrift = true
		case deploy.ActionCreate:
			sawCreate = true
		}
	}
	if !sawDrift {
		t.Errorf("Plan did not report drift for a destroyed deployment: %+v", plan.Changes)
	}
	if !sawCreate {
		t.Errorf("Plan did not fall back to creating the runtime: %+v", plan.Changes)
	}
}

// TestDeployed_DestroyIsIdempotent checks destroy converges. A retried
// teardown must not fail, or every interrupted destroy becomes manual cleanup
// in the console.
func TestDeployed_DestroyIsIdempotent(t *testing.T) {
	env := requireEnv(t)
	cfgJSON := deployConfig(t, env)
	state := applyPack(t, env, packNamed(t))

	ctx, cancel := context.WithTimeout(context.Background(), applyTimeout)
	defer cancel()

	provider := agentcore.NewProvider()
	if err := provider.Destroy(ctx, &deploy.DestroyRequest{
		DeployConfig: cfgJSON, PriorState: state,
	}, nil); err != nil {
		t.Fatalf("first destroy: %v", err)
	}
	if err := provider.Destroy(ctx, &deploy.DestroyRequest{
		DeployConfig: cfgJSON, PriorState: state,
	}, nil); err != nil {
		t.Errorf("destroying an already-destroyed deploy must be a no-op, got: %v", err)
	}
}
