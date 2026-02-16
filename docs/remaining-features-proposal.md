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
| A2A wiring | Synthetic ARN, no AWS call | No separate API exists yet; needs real endpoint config when available |
| Evaluator creation | Synthetic ARN, log message | SDK doesn't expose `CreateEvaluator` yet |
| Container image config | Hardcoded `public.ecr.aws/bedrock-agentcore/runtime:latest` | Needs pack-specific or per-agent image URIs |
| Memory store wiring | Config field parsed but unused in Apply | Should pass to `CreateAgentRuntime` or generate memory resource |
| Observability wiring | Config fields parsed but unused | CloudWatch log group + X-Ray tracing not passed to runtime |
| Cedar policy generation | Not started | Validators + tool_policy → Cedar policies |
| CloudWatch metrics/dashboards | Not started | Eval metrics → CloudWatch custom metrics |
| Update (in-place) support | Plan detects UPDATE but Apply only handles CREATE | `UpdateAgentRuntime` not called |
| Dry-run mode | Not started | Plan preview without deploying |
| Per-agent container images | Not started | Different images per agent member |

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

### Milestone 1: Update Support and Container Configuration

Complete the deploy lifecycle by implementing in-place updates and making container
images configurable. This unblocks real iterative deployments.

#### Issue #1: Implement UpdateAgentRuntime in Apply

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

#### Issue #2: Configurable container image URI

**Labels**: `enhancement`, `priority/high`

Replace the hardcoded `public.ecr.aws/bedrock-agentcore/runtime:latest` with a
configurable image URI. Support per-agent overrides for multi-agent packs.

**Scope**:
- Add `container_image` field to `AgentCoreConfig` (top-level default)
- Add `agent_overrides` map to config: `{ "agent_name": { "container_image": "..." } }`
- Update JSON Schema in `provider.go`
- Pass image URI to `CreateAgentRuntime` / `UpdateAgentRuntime`
- Validate image URI format (must be a valid ECR or registry URI)
- Tests: config validation, Apply uses correct image per agent

**Acceptance criteria**: Users can specify custom container images. Different agents in
a multi-agent pack can use different images.

---

### Milestone 2: Memory and Observability Wiring

Wire the already-parsed config fields into actual AWS resource creation.

#### Issue #3: Wire memory store configuration to runtime creation

**Labels**: `enhancement`, `priority/medium`

The `memory_store` config field is validated but never used. Pass it to the runtime
so AgentCore provisions the appropriate memory backend.

**Scope**:
- Determine the correct AgentCore SDK field for memory configuration (may be `EnvironmentVariables` on the runtime, or a separate `CreateMemory` API if available)
- Pass `memory_store` value when creating/updating runtimes
- If AgentCore exposes a `CreateMemoryStore` API, add it to `awsClient` interface and implement
- Add `memory` resource type to Plan if a separate resource is needed
- Update Destroy to tear down memory resources
- Tests: plan includes memory resource, apply creates it, destroy removes it

**Acceptance criteria**: Deploying with `memory_store: "persistent"` results in a runtime
with persistent memory enabled. Status reflects memory resource health.

---

#### Issue #4: Wire observability configuration (CloudWatch + X-Ray)

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

### Milestone 3: Policy and Guardrails

Map PromptPack validators and tool policies to AgentCore's policy enforcement.

#### Issue #5: Generate Cedar policies from validators

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

#### Issue #6: Map tool_policy to Cedar enforcement

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

### Milestone 4: Evaluations Pipeline

Bridge PromptPack eval definitions to AgentCore's evaluation framework and CloudWatch.

#### Issue #7: Create evaluator resources from pack evals

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

#### Issue #8: Publish eval metrics to CloudWatch

**Labels**: `enhancement`, `priority/low`

PromptPack eval `metric` definitions (gauge, counter, histogram, boolean) should
publish to CloudWatch as custom metrics with pack/agent dimensions.

**Scope**:
- Add `cloudwatch.go` — metric publishing configuration generator
- Map `MetricDef` types to CloudWatch metric types
- Generate CloudWatch metric namespace: `PromptPack/Evals`
- Add dimensions: `pack_id`, `agent` (for multi-agent), `eval_id`
- Generate metric `range` definitions as CloudWatch Alarms (optional)
- This may be runtime-side config rather than deploy-side — determine whether the
  adapter generates config that the runtime container reads, or creates CloudWatch
  resources directly
- Tests: metric definition → CloudWatch config

**Acceptance criteria**: Eval metrics are publishable to CloudWatch with correct
namespaces and dimensions. Alarms fire when metrics exceed defined ranges.

---

#### Issue #9: Auto-generate CloudWatch dashboard

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

### Milestone 5: A2A Endpoint Hardening

Move A2A wiring from a logical placeholder to real endpoint configuration.

#### Issue #10: Configure A2A endpoint discovery on runtimes

**Labels**: `enhancement`, `priority/medium`

Currently A2A wiring returns a synthetic ARN. The entry agent needs to know the
endpoints of member agents for A2A `message/send` calls.

**Scope**:
- After all runtimes are created, collect their endpoints/ARNs
- Inject member agent endpoints into the entry agent's runtime configuration
  (environment variables, or update runtime with discovery config)
- If AgentCore provides a native A2A registration/discovery API, use it
- Otherwise, generate a discovery manifest and pass via runtime env vars:
  `PROMPTPACK_AGENTS={"researcher":"https://...","analyst":"https://..."}`
- Update Apply ordering: create all runtimes first, then configure A2A discovery
- This may require a two-pass Apply or an `UpdateAgentRuntime` call after initial creation
- Tests: multi-agent apply → entry agent has member endpoints in config

**Acceptance criteria**: The entry agent runtime can discover and invoke member agent
runtimes via A2A protocol.

**Depends on**: Issue #1 (UpdateAgentRuntime)

---

#### Issue #11: Configure A2A authentication between runtimes

**Labels**: `enhancement`, `priority/medium`

Inter-agent A2A calls need authentication. Configure OAuth 2.0 or SigV4 between
runtimes.

**Scope**:
- Determine AgentCore's supported A2A auth mechanisms (OAuth 2.0, SigV4, IAM)
- Add `a2a_auth_mode` config field (or derive from runtime role)
- Configure auth on each runtime's A2A endpoint
- If IAM-based, ensure the runtime role has permission to invoke sibling runtimes
- Tests: config with auth → correct auth configuration on runtimes

**Acceptance criteria**: A2A calls between runtimes are authenticated. Unauthenticated
calls are rejected.

---

### Milestone 6: Dry-Run and Operational Tooling

#### Issue #12: Implement dry-run mode

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

#### Issue #13: Add resource tagging support

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

#### Issue #14: Improve error messages and deployment diagnostics

**Labels**: `enhancement`, `priority/low`, `dx`

Improve the developer experience when deployments fail.

**Scope**:
- Include `FailureReason` from AWS responses in error events
- Add a `diagnose` capability that checks IAM permissions before deploying
- Emit warnings for common misconfigurations (wrong region, insufficient permissions)
- Include cost estimation hints in plan output (optional)
- Tests: failure scenarios produce actionable error messages

**Acceptance criteria**: Failed deployments produce clear, actionable error messages
that tell the user what to fix.

---

### Milestone 7: CI and Quality Infrastructure

#### Issue #15: Add SonarCloud analysis to CI pipeline

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

#### Issue #16: Reach 80% test coverage on existing code

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

| # | Milestone | Issues | Priority |
|---|---|---|---|
| M1 | Update Support & Container Config | #1, #2 | High |
| M2 | Memory & Observability Wiring | #3, #4 | Medium |
| M3 | Policy & Guardrails | #5, #6 | Medium |
| M4 | Evaluations Pipeline | #7, #8, #9 | Medium/Low (partially blocked) |
| M5 | A2A Endpoint Hardening | #10, #11 | Medium |
| M6 | Dry-Run & Operational Tooling | #12, #13, #14 | Low |
| M7 | CI & Quality Infrastructure | #15, #16 | High |

### Recommended execution order

1. **M7** (CI + coverage) — establish the quality gate first so all subsequent work is measured
2. **M1** (updates + container config) — unblocks real iterative deployments
3. **M2** (memory + observability) — wires existing config fields
4. **M5** (A2A hardening) — depends on M1 for UpdateAgentRuntime
5. **M3** (policies) — new resource type, independent of others
6. **M4** (evals) — partially blocked on AWS SDK; start with CloudWatch metrics
7. **M6** (DX tooling) — polish

---

## Dependency Graph

```
M7 (CI + Quality)
 │
 ▼
M1 (Update + Container) ──────────┐
 │                                 │
 ├──▶ M2 (Memory + Observability)  │
 │                                 │
 └──▶ M5 (A2A Hardening) ◄────────┘
      │
      ▼
M3 (Policy + Guardrails)
 │
 ▼
M4 (Evaluations) ← blocked on AWS SDK for #7
 │
 ▼
M6 (Dry-Run + DX)
```

---

## Out of Scope

These items from the original proposal are **not** part of this adapter's responsibility.
They belong to the Python runtime package (`promptpack-agentcore` in `promptpack-python`):

- Agent Card generation from prompt metadata
- A2A JSON-RPC 2.0 server implementation
- A2A tool bridge (resolve prompt-key tool refs as A2A calls)
- Template rendering inside the container
- Per-agent prompt routing
- LLM inference calls

This Go adapter creates and manages the **infrastructure**. The Python runtime code
runs **inside** the containers this adapter deploys.
