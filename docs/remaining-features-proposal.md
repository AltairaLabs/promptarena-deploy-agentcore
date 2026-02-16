# Proposal: Remaining Feature Implementation — AgentCore Deploy Adapter

**Status**: Proposal
**Created**: February 16, 2026
**Author**: PromptKit Team
**Baseline**: Current `main` at `9bd55ca`

---

## Current State

The adapter implements the full deploy lifecycle (Plan, Apply, Destroy, Status) via
PromptKit's JSON-RPC 2.0 adapter protocol. Real AWS SDK integration exists for
`CreateAgentRuntime`, `CreateGateway`, `CreateGatewayTarget`, and corresponding
delete/health-check operations. Multi-agent topology detection and per-agent runtime
creation are working.

### What works today

| Feature | File(s) | Notes |
|---|---|---|
| JSON-RPC adapter protocol | `provider.go`, `main.go` | `get_provider_info`, `validate_config`, `plan`, `apply`, `destroy`, `status` |
| Config validation + JSON Schema | `config.go` | Region, role ARN, memory store, observability, tools |
| Plan with state diffing | `plan.go` | CREATE/UPDATE/DELETE actions, multi-agent resource generation |
| Apply with 4-phase ordering | `apply.go` | tools → runtimes → a2a → evaluators, streaming progress |
| Destroy in reverse order | `status.go` | evaluators → a2a → runtimes → tools |
| Status with health checks | `status.go` | Per-resource health, aggregate deployed/degraded |
| Real AWS: CreateAgentRuntime | `aws_client_real.go` | Polls until READY |
| Real AWS: CreateGateway + targets | `aws_client_real.go` | Lazy gateway, MCP protocol |
| Real AWS: Delete + health checks | `aws_client_real.go` | Runtime + gateway |
| Multi-agent detection | `plan.go`, `apply.go` | Via `adaptersdk.IsMultiAgent()` + `ExtractAgents()` |
| Unit tests (simulated clients) | `*_test.go` (6 files) | Full coverage of all operations |
| Integration tests (real AWS) | `aws_client_integration_test.go` | Opt-in via env vars |

### What's placeholder or missing

| Feature | Current State | Gap |
|---|---|---|
| Container runtime | Hardcoded `public.ecr.aws/bedrock-agentcore/runtime:latest` | Should use PromptKit as the container runtime |
| A2A wiring | Synthetic ARN, no AWS call | No separate API exists yet; needs real endpoint config when available |
| Evaluator creation | Synthetic ARN, log message | SDK doesn't expose `CreateEvaluator` yet |
| Memory store wiring | Config field parsed but unused in Apply | Should pass to `CreateAgentRuntime` or generate memory resource |
| Observability wiring | Config fields parsed but unused | CloudWatch log group + X-Ray tracing not passed to runtime |
| Cedar policy generation | Not started | Validators + tool_policy → Cedar policies |
| CloudWatch metrics/dashboards | Not started | Eval metrics → CloudWatch custom metrics |
| Update (in-place) support | Plan detects UPDATE but Apply only handles CREATE | `UpdateAgentRuntime` not called |
| Dry-run mode | Not started | Plan preview without deploying |

---

## Runtime Architecture: PromptKit as the AgentCore Container

The original proposal assumed a Python runtime (`promptpack-agentcore`) running
inside AgentCore containers. This is unnecessary. AgentCore runs arbitrary
containers — we should run **PromptKit itself** as the container runtime.

### Why

PromptKit's Go SDK already provides everything needed to serve a pack as an
A2A-compliant HTTP agent:

| Component | Location | Status |
|---|---|---|
| A2A HTTP server | `sdk/a2a_server.go` | Done — `ListenAndServe()`, JSON-RPC 2.0, SSE streaming |
| Agent Card serving | `sdk/a2a_server.go` | Done — `GET /.well-known/agent.json` |
| Pack loader | `sdk/sdk.go` | Done — `sdk.Open(packPath, promptName)` |
| Agent tool resolver | `sdk/agent_resolver.go` | Done — resolves member refs to A2A tool calls |
| A2A client | `runtime/a2a/client.go` | Done — `Discover()`, `SendMessage()`, streaming |
| Tool bridge | `runtime/a2a/bridge.go` | Done — remote agent → ToolDescriptor |
| Container support | `Dockerfile.arena` | Done — Alpine-based, non-root |

What's missing is a thin `cmd/` entry point (~100 lines) that:

1. Reads env vars: `PROMPTPACK_FILE`, `PROMPTPACK_AGENT`, `PROMPTPACK_AGENTS` (peer endpoints)
2. Calls `sdk.Open()` to load the pack for the assigned agent
3. Configures `AgentToolResolver` with peer agent endpoints from `PROMPTPACK_AGENTS`
4. Wraps in `sdk.NewA2AServer()` and calls `ListenAndServe()` on port 9000

### Benefits over Python runtime

1. **Single language** — Go adapter deploys Go runtime. No Python SDK to build/maintain.
2. **Native A2A** — PromptKit's A2A implementation is battle-tested (arena, demos).
3. **Smaller image** — Static Go binary vs Python + pip dependencies.
4. **Feature parity** — Template rendering, fragment resolution, validators, tool
   policy, multimodal support all come for free.
5. **No duplication** — Agent Card generation, tool bridging, and A2A protocol
   handling are already implemented. The Python proposal would reimplement all of this.

### Container image strategy

```
promptkit-agentcore:v1.3.0     ← base image: Go binary + A2A server
  └── pack embedded at build    ← or mounted at runtime via volume/env
```

The adapter supports three modes (configurable via `container_image`):

1. **Default**: Prebuilt PromptKit image with pack file injected as env/config
2. **Custom**: User-provided image URI (for non-PromptKit runtimes, Python, etc.)
3. **Build**: Adapter generates Dockerfile, builds image, pushes to ECR (future)

---

## Code Quality Requirements

All new code must pass the existing CI pipeline **and** SonarCloud analysis using
**SonarWay** defaults:

| Gate Metric | SonarWay Threshold | Notes |
|---|---|---|
| **Coverage on new code** | >= 80% | Measured by SonarCloud on PR diff |
| **Duplicated lines on new code** | < 3% | |
| **Maintainability rating** | A | No new code smells |
| **Reliability rating** | A | No new bugs |
| **Security rating** | A | No new vulnerabilities |
| **Security hotspots reviewed** | 100% | |

Additionally, all code must pass:
- `golangci-lint run` with the project's `.golangci.yml` (25 linters, 120-char line limit, `gocognit` min 15)
- `go test ./... -v -race -count=1` with race detector
- `GOWORK=off go build` (sibling repo layout)

### Testing patterns to follow

- **Unit tests**: Use `simulatedAWSClient` / `failingAWSClient` injection via `awsClientFunc` factory
- **Table-driven tests** for config validation, resource planning
- **Event-driven tests** for Apply/Destroy (collect callback events, assert ordering)
- **Integration tests**: Guard with env-var checks (`AGENTCORE_TEST_REGION`), use `//go:build integration`
- **No `_test.go` linter exclusions** are free — `mnd`, `gocognit`, etc. are already relaxed for tests

---

## Milestones and Issues

### Milestone 1: PromptKit Container Runtime

Replace the hardcoded third-party container image with PromptKit running natively
as the AgentCore runtime. This is the highest-impact change — it gives the adapter
a real, working agent inside every container it deploys.

#### Issue #1: Create PromptKit A2A server entry point (in PromptKit repo)

**Labels**: `enhancement`, `priority/high`
**Repo**: `AltairaLabs/PromptKit`

Add a `cmd/agentcore-runtime/` entry point that serves a pack as an A2A agent
on port 9000.

**Scope**:
- New `cmd/agentcore-runtime/main.go` (~100 lines)
- Read configuration from environment variables:
  - `PROMPTPACK_FILE` — path to `.pack.json` inside container
  - `PROMPTPACK_AGENT` — which prompt/agent to serve (for multi-agent packs)
  - `PROMPTPACK_AGENTS` — JSON map of peer agent names → endpoints for A2A discovery
  - `PROMPTPACK_PORT` — listen port (default 9000)
- Use `sdk.Open()` to load the pack and create a conversation factory
- Configure `AgentToolResolver` with `MapEndpointResolver` from `PROMPTPACK_AGENTS`
- Wrap with `sdk.NewA2AServer()`, serve on configured port
- Handle SIGTERM/SIGINT for graceful shutdown (AgentCore sends SIGTERM)
- Health check endpoint at `GET /health` for AgentCore probes
- Tests: start server, verify agent card at `/.well-known/agent.json`,
  send a message, verify response

**Acceptance criteria**: `go run ./cmd/agentcore-runtime` loads a pack and serves
it as an A2A-compliant HTTP agent. Agent card reflects the pack prompt's metadata.

---

#### Issue #2: Build and publish PromptKit AgentCore container image (in PromptKit repo)

**Labels**: `enhancement`, `priority/high`, `ci`
**Repo**: `AltairaLabs/PromptKit`

Add a Dockerfile and CI pipeline to build and publish the container image to a
public registry.

**Scope**:
- `Dockerfile.agentcore` — multi-stage build:
  - Builder stage: compile `cmd/agentcore-runtime` as static binary
  - Runtime stage: `scratch` or Alpine, copy binary, expose port 9000
- Publish to `public.ecr.aws/altairalabs/promptkit-agentcore:VERSION`
  (or GitHub Container Registry `ghcr.io/altairalabs/promptkit-agentcore:VERSION`)
- Add to PromptKit release pipeline: build + push image after tagging
- Image should accept pack file via:
  - Volume mount: `-v ./pack.json:/app/pack.json`
  - Environment variable: `PROMPTPACK_FILE=/app/pack.json`
- Tests: build image, run container, verify A2A endpoint responds

**Acceptance criteria**: `docker run -e PROMPTPACK_FILE=/app/pack.json -v ./pack.json:/app/pack.json ghcr.io/altairalabs/promptkit-agentcore:v1.3.0`
starts a working A2A agent.

---

#### Issue #3: Use PromptKit image as default container in adapter

**Labels**: `enhancement`, `priority/high`
**Repo**: `AltairaLabs/promptarena-deploy-agentcore`

Update the adapter to use the PromptKit container image by default and inject
pack configuration via environment variables.

**Scope**:
- Change default `ContainerUri` from `public.ecr.aws/bedrock-agentcore/runtime:latest`
  to `ghcr.io/altairalabs/promptkit-agentcore:VERSION` (version from pack or config)
- Add `container_image` field to `AgentCoreConfig` for user override
- Add `agent_overrides` map to config: `{ "agent_name": { "container_image": "..." } }`
- When creating runtimes, set environment variables on the container:
  - `PROMPTPACK_FILE` — path to pack file (bundled or fetched at startup)
  - `PROMPTPACK_AGENT` — agent name (from pack `agents.members` key)
  - `PROMPTPACK_AGENTS` — peer endpoint map (populated after all runtimes are created)
- Update JSON Schema in `provider.go`
- Validate image URI format
- Tests: config validation, default image used, per-agent override, env vars set

**Acceptance criteria**: `apply` creates runtimes using the PromptKit image. Each
agent container receives the correct environment variables to load its prompt.

---

### Milestone 2: Update Support and Lifecycle Completion

Complete the deploy lifecycle by implementing in-place updates.

#### Issue #4: Implement UpdateAgentRuntime in Apply

**Labels**: `enhancement`, `priority/high`

Plan already detects `UPDATE` actions for changed resources. Apply needs to call
`UpdateAgentRuntime` instead of `CreateAgentRuntime` when the action is UPDATE.

**Scope**:
- Add `UpdateRuntime` to the `awsClient` interface
- Implement `UpdateRuntime` in `realAWSClient` using `bedrockagentcorecontrol.UpdateAgentRuntime`
- Add `UpdateGatewayTarget` for tool changes
- Modify Apply's per-phase loops to branch on `ActionCreate` vs `ActionUpdate`
- Add `simulatedAWSClient.UpdateRuntime` and `UpdateGatewayTarget` for tests
- Add unit tests: `TestApply_UpdateExistingRuntime`, `TestApply_MixedCreateAndUpdate`
- Add integration test: `TestIntegration_UpdateRuntime`

**Acceptance criteria**: Apply correctly updates existing resources without recreating them.
Plan → Apply → Plan shows no pending changes.

---

### Milestone 3: A2A Endpoint Hardening

Move A2A wiring from a logical placeholder to real endpoint configuration.

#### Issue #5: Configure A2A endpoint discovery on runtimes

**Labels**: `enhancement`, `priority/medium`

Currently A2A wiring returns a synthetic ARN. The entry agent needs to know the
endpoints of member agents for A2A `message/send` calls.

**Scope**:
- After all runtimes are created, collect their endpoints/ARNs
- Inject member agent endpoints into the entry agent's runtime via
  `PROMPTPACK_AGENTS` environment variable (JSON map: `{"researcher":"https://..."}`)
- This requires a two-pass Apply: create all runtimes first, then update the entry
  agent's env vars with the discovered endpoints (uses UpdateAgentRuntime from Issue #4)
- If AgentCore provides a native A2A registration/discovery API, use it instead
- Update Apply phase ordering to accommodate the two-pass approach
- Tests: multi-agent apply → entry agent has member endpoints in env vars

**Acceptance criteria**: The entry agent runtime can discover and invoke member agent
runtimes via A2A protocol using the `PROMPTPACK_AGENTS` endpoint map.

**Depends on**: Issue #4 (UpdateAgentRuntime)

---

#### Issue #6: Configure A2A authentication between runtimes

**Labels**: `enhancement`, `priority/medium`

Inter-agent A2A calls need authentication. Configure OAuth 2.0 or SigV4 between
runtimes.

**Scope**:
- Determine AgentCore's supported A2A auth mechanisms (OAuth 2.0, SigV4, IAM)
- Add `a2a_auth_mode` config field (or derive from runtime role)
- Configure auth on each runtime's A2A endpoint
- If IAM-based, ensure the runtime role has permission to invoke sibling runtimes
- Pass auth configuration to PromptKit runtime via env vars
  (`PROMPTPACK_A2A_AUTH_MODE`, `PROMPTPACK_A2A_AUTH_ROLE`)
- Tests: config with auth → correct auth configuration on runtimes

**Acceptance criteria**: A2A calls between runtimes are authenticated. Unauthenticated
calls are rejected.

---

### Milestone 4: Memory and Observability Wiring

Wire the already-parsed config fields into actual AWS resource creation.

#### Issue #7: Wire memory store configuration to runtime creation

**Labels**: `enhancement`, `priority/medium`

The `memory_store` config field is validated but never used. Pass it to the runtime
so AgentCore provisions the appropriate memory backend.

**Scope**:
- Determine the correct AgentCore SDK field for memory configuration (may be
  environment variables on the runtime, or a separate `CreateMemory` API if available)
- Pass `memory_store` value when creating/updating runtimes
- If AgentCore exposes a `CreateMemoryStore` API, add it to `awsClient` interface
- Add `memory` resource type to Plan if a separate resource is needed
- Update Destroy to tear down memory resources
- Tests: plan includes memory resource, apply creates it, destroy removes it

**Acceptance criteria**: Deploying with `memory_store: "persistent"` results in a runtime
with persistent memory enabled. Status reflects memory resource health.

---

#### Issue #8: Wire observability configuration (CloudWatch + X-Ray)

**Labels**: `enhancement`, `priority/medium`

Pass `observability.cloudwatch_log_group` and `observability.tracing_enabled` to
runtime creation so that agent logs and traces are routed correctly.

**Scope**:
- Map `cloudwatch_log_group` to runtime environment or AgentCore observability config
- Map `tracing_enabled` to X-Ray / OpenTelemetry configuration
- If these are runtime environment variables, pass them in `CreateAgentRuntime`
- If they require separate resources (log group creation), add to Plan/Apply/Destroy
- Tests: config with observability → correct runtime parameters

**Acceptance criteria**: Deployed runtimes send logs to the specified CloudWatch log group.
X-Ray tracing is enabled when configured.

---

### Milestone 5: Policy and Guardrails

Map PromptPack validators and tool policies to AgentCore's policy enforcement.

#### Issue #9: Generate Cedar policies from validators

**Labels**: `enhancement`, `priority/medium`

PromptPack validators (`banned_words`, `max_length`, `regex_match`, `json_schema`)
should produce Cedar policy documents that AgentCore enforces at runtime.

**Scope**:
- Add a `policy` resource type to the adapter
- Implement `cedar.go` — a Cedar policy generator that maps each validator type:
  - `banned_words` → Cedar `forbid` on response content containing words
  - `max_length` → Cedar `forbid` when content length exceeds limit
  - `regex_match` → Cedar `forbid`/`permit` with pattern match
  - `json_schema` → Cedar policy + inline validation reference
- Add `CreatePolicy` to `awsClient` interface (or use Bedrock Guardrails API)
- In Plan, generate one `policy` resource per validator with `fail_on_violation: true`
- Validators with `fail_on_violation: false` become observability-only (log, don't block)
- In multi-agent packs, generate per-agent policies from prompt-level validators
- Add to Apply phase ordering (policies before runtimes, so runtimes reference them)
- Add to Destroy ordering
- Tests: validator → Cedar policy string, plan includes policy resources, round-trip

**Acceptance criteria**: A pack with `banned_words` validator deploys a Cedar policy.
The policy ARN is stored in state. Destroy removes it.

---

#### Issue #10: Map tool_policy to Cedar enforcement

**Labels**: `enhancement`, `priority/medium`

PromptPack's `tool_policy` (blocklist, max_rounds, max_tool_calls_per_turn) should
map to AgentCore policy enforcement.

**Scope**:
- Extend Cedar policy generation to include tool-level restrictions
- `tool_policy.blocklist` → Cedar `forbid` for listed tool names
- `tool_policy.max_rounds` → Runtime configuration or Cedar policy
- `tool_policy.max_tool_calls_per_turn` → Runtime configuration or Cedar policy
- Merge tool policies with validator policies into a single policy document per agent
- Tests: tool_policy config → Cedar policy content, combined validator + tool policy

**Acceptance criteria**: A pack with `tool_policy.blocklist: ["dangerous_tool"]` deploys
a policy that prevents invocation of that tool.

---

### Milestone 6: Evaluations Pipeline

Bridge PromptPack eval definitions to AgentCore's evaluation framework and CloudWatch.

#### Issue #11: Create evaluator resources from pack evals

**Labels**: `enhancement`, `priority/medium`, `blocked`

Currently `CreateEvaluator` returns a placeholder. When the AgentCore SDK ships
evaluator support, implement real creation.

**Scope**:
- Monitor `bedrockagentcorecontrol` SDK for evaluator API availability
- Implement `CreateEvaluator` in `realAWSClient` using the real API
- Map pack eval types to AgentCore evaluator types:
  - `tools_called` → Built-in Tool Selection Accuracy evaluator
  - `llm_judge` → Custom model-based evaluator
  - Others → Custom evaluator with Lambda or inline logic
- Map eval triggers (`every_turn`, `on_session_complete`, `sample_turns`) to AgentCore config
- Implement `DeleteEvaluator` and `CheckEvaluator` in `realAWSClient`
- Tests: eval config → evaluator creation, health check, destroy

**Acceptance criteria**: Pack evals deploy as real AgentCore evaluators. Status reflects
evaluator health. Destroy removes them.

**Blocked by**: AWS SDK availability for evaluator APIs.

---

#### Issue #12: Publish eval metrics to CloudWatch

**Labels**: `enhancement`, `priority/low`

PromptPack eval `metric` definitions (gauge, counter, histogram, boolean) should
publish to CloudWatch as custom metrics with pack/agent dimensions.

**Scope**:
- Add `cloudwatch.go` — metric publishing configuration generator
- Map `MetricDef` types to CloudWatch metric types
- Generate CloudWatch metric namespace: `PromptPack/Evals`
- Add dimensions: `pack_id`, `agent` (for multi-agent), `eval_id`
- Generate metric `range` definitions as CloudWatch Alarms (optional)
- This is runtime-side config — the adapter generates a metric config that the
  PromptKit runtime container reads via env var (`PROMPTPACK_METRICS_CONFIG`)
- Tests: metric definition → CloudWatch config

**Acceptance criteria**: Eval metrics are publishable to CloudWatch with correct
namespaces and dimensions. Alarms fire when metrics exceed defined ranges.

---

#### Issue #13: Auto-generate CloudWatch dashboard

**Labels**: `enhancement`, `priority/low`

Create a CloudWatch dashboard showing all agents, their eval metrics, and
inter-agent call patterns.

**Scope**:
- Add `dashboard` resource type
- Generate CloudWatch Dashboard JSON body from pack structure:
  - One widget per agent showing key metrics
  - One widget for inter-agent A2A call latency (if observable)
  - One widget per eval metric with threshold lines from `range`
- Use `cloudwatch:PutDashboard` API (may need additional IAM permissions)
- Add to Plan/Apply/Destroy lifecycle
- Tests: pack with evals → dashboard JSON structure

**Acceptance criteria**: Deploying a multi-agent pack with evals creates a CloudWatch
dashboard. Destroy removes it.

---

### Milestone 7: Dry-Run and Operational Tooling

#### Issue #14: Implement dry-run mode

**Labels**: `enhancement`, `priority/low`

Allow users to preview what would be deployed without creating resources.

**Scope**:
- Add `dry_run` boolean to `AgentCoreConfig`
- When `dry_run` is true, Apply runs Plan logic and emits resource events with
  `status: "planned"` instead of calling AWS APIs
- Return a state snapshot showing what *would* be created
- Update JSON Schema
- Tests: dry-run apply → no AWS calls, correct planned output

**Acceptance criteria**: `dry_run: true` produces a deployment preview without
creating any AWS resources.

---

#### Issue #15: Add resource tagging support

**Labels**: `enhancement`, `priority/low`

Tag all created AWS resources with pack metadata for cost allocation and management.

**Scope**:
- Add tags to `CreateAgentRuntime`, `CreateGateway`: `promptpack:pack-id`,
  `promptpack:version`, `promptpack:agent` (for multi-agent)
- Add optional `tags` map to config for user-defined tags
- Merge default + user tags
- Tests: created resources include expected tags

**Acceptance criteria**: All AWS resources are tagged with pack metadata. Custom tags
from config are applied.

---

#### Issue #16: Improve error messages and deployment diagnostics

**Labels**: `enhancement`, `priority/low`, `dx`

Improve the developer experience when deployments fail.

**Scope**:
- Include `FailureReason` from AWS responses in error events
- Add a `diagnose` capability that checks IAM permissions before deploying
- Emit warnings for common misconfigurations (wrong region, insufficient permissions)
- Tests: failure scenarios produce actionable error messages

**Acceptance criteria**: Failed deployments produce clear, actionable error messages
that tell the user what to fix.

---

### Milestone 8: CI and Quality Infrastructure

#### Issue #17: Add SonarCloud analysis to CI pipeline

**Labels**: `ci`, `priority/high`

The SonarCloud badge exists in README but no analysis step runs in CI.

**Scope**:
- Add `sonar-project.properties` with project key `AltairaLabs_promptarena-deploy-agentcore`
- Add SonarCloud scan step to CI workflow (after test step, using coverage output)
- Generate coverage profile: `go test ./... -coverprofile=coverage.out`
- Configure SonarWay quality gate as the default
- Ensure PR decoration works (SonarCloud comments on PRs)
- Set `sonar.go.coverage.reportPaths=coverage.out`
- Set `sonar.sources=.` and `sonar.exclusions=**/*_test.go`

**Acceptance criteria**: Every PR gets SonarCloud analysis. Quality gate blocks merge
if thresholds are not met.

---

#### Issue #18: Reach 80% test coverage on existing code

**Labels**: `testing`, `priority/high`

Ensure existing code meets the SonarWay 80% coverage threshold before enforcing it
on new code.

**Scope**:
- Run `go test ./... -coverprofile=coverage.out && go tool cover -func=coverage.out`
  to measure current coverage
- Identify under-covered files (likely `aws_client_real.go` — integration-only,
  `main.go`, helper functions)
- Add unit tests for `extractResourceID`, `isNotFound`, `isInDestroyOrder`
- Add tests for edge cases in `parseConfig`, `parseAdapterState`
- Consider refactoring `aws_client_real.go` to make polling logic testable
  (extract `waitForReady` with injectable sleep/poll functions)
- Exclude integration test file from coverage if needed (`sonar.exclusions`)

**Acceptance criteria**: `go tool cover -func=coverage.out` shows >= 80% overall.
SonarCloud quality gate passes on main.

---

## Issue Summary by Milestone

| # | Milestone | Issues | Priority | Repo |
|---|---|---|---|---|
| M1 | PromptKit Container Runtime | #1, #2, #3 | High | PromptKit + this repo |
| M2 | Update Support & Lifecycle | #4 | High | This repo |
| M3 | A2A Endpoint Hardening | #5, #6 | Medium | This repo |
| M4 | Memory & Observability Wiring | #7, #8 | Medium | This repo |
| M5 | Policy & Guardrails | #9, #10 | Medium | This repo |
| M6 | Evaluations Pipeline | #11, #12, #13 | Medium/Low (partially blocked) | This repo |
| M7 | Dry-Run & Operational Tooling | #14, #15, #16 | Low | This repo |
| M8 | CI & Quality Infrastructure | #17, #18 | High | This repo |

### Recommended execution order

1. **M8** (CI + coverage) — establish the quality gate first so all subsequent work is measured
2. **M1** (PromptKit runtime) — the core architectural change; makes everything else real
3. **M2** (update support) — unblocks iterative deployments
4. **M3** (A2A hardening) — depends on M1 + M2; wires up multi-agent communication
5. **M4** (memory + observability) — wires existing config fields
6. **M5** (policies) — new resource type, independent of others
7. **M6** (evals) — partially blocked on AWS SDK; start with CloudWatch metrics
8. **M7** (DX tooling) — polish

---

## Dependency Graph

```
M8 (CI + Quality)
 │
 ▼
M1 (PromptKit Container Runtime) ←── Issues #1, #2 in PromptKit repo
 │
 ▼
M2 (Update Support) ─────────────┐
 │                                │
 ├──▶ M4 (Memory + Observability) │
 │                                │
 └──▶ M3 (A2A Hardening) ◄───────┘
      │
      ▼
M5 (Policy + Guardrails)
 │
 ▼
M6 (Evaluations) ← blocked on AWS SDK for #11
 │
 ▼
M7 (Dry-Run + DX)
```

---

## What This Replaces

The original proposal envisioned a separate **Python** runtime package
(`promptpack-agentcore` in `promptpack-python`). That package is no longer needed.
By running PromptKit as the AgentCore container runtime, we get:

| Original Python Proposal Item | Replaced By |
|---|---|
| `promptpack-agentcore` Python package | PromptKit `cmd/agentcore-runtime` Go binary |
| Agent Card generation (Python) | `sdk.NewA2AServer()` — already serves cards |
| A2A JSON-RPC 2.0 server (Python) | `sdk/a2a_server.go` — already implemented |
| A2A tool bridge (Python) | `runtime/a2a/bridge.go` — already implemented |
| Template rendering (Python) | `sdk.Open()` — already handles templates + fragments |
| Per-agent prompt routing (Python) | `PROMPTPACK_AGENT` env var → `sdk.Open(pack, agentName)` |
| LLM inference (Python) | PromptKit SDK auto-detects provider from env vars |

The Python SDK (`promptpack-python`) remains useful for LangChain integration and
local development, but it is not needed for AgentCore deployment.
