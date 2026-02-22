# AgentCore Deploy Adapter - Claude Code Project Instructions

## Git Workflow

- **Never push directly to main** — use feature branches.
- Branch naming: `feat/<description>`, `fix/<description>`, or `feature/<issue-number>-<short-description>`.
- Standard flow: branch → commit → push with `-u` → create PR via `gh pr create` → monitor CI → merge via `gh pr merge --squash`.
- When continuing a previous session, check `git status`, `git log --oneline -5`, and any existing plan files before taking action.

## Build & Test Commands

```bash
# Build adapter (requires PromptKit sibling checkout at ../promptkit)
GOWORK=off go build -o promptarena-deploy-agentcore .

# Build runtime binary (native, for local testing)
make build-runtime

# Cross-compile runtime for AgentCore (Linux ARM64)
make build-runtime-arm64

# Test with race detector
GOWORK=off go test ./... -v -race -count=1

# Test with coverage
GOWORK=off go test ./... -coverprofile=coverage.out
go tool cover -func=coverage.out

# Lint (25 linters, see .golangci.yml)
golangci-lint run

# Integration tests — invoke a deployed runtime (requires AWS credentials + deployed runtime)
AGENTCORE_TEST_REGION=us-west-2 \
AGENTCORE_TEST_RUNTIME_ARN=arn:aws:bedrock-agentcore:us-west-2:123:runtime/my-runtime \
  GOWORK=off go test -tags=integration -v ./internal/agentcore/

# Run the adapter (JSON-RPC over stdio)
echo '{"jsonrpc":"2.0","method":"get_provider_info","id":1}' | ./promptarena-deploy-agentcore
```

## Local Development Setup

Install the pre-commit hook to catch issues before they reach CI:

```bash
make install-hooks
```

This configures git to use `.githooks/pre-commit`, which runs on every commit that includes Go files:
1. **goimports** — checks formatting (fails if files need formatting)
2. **golangci-lint** — runs all 25 linters
3. **go test** — runs tests with race detector
4. **go build** — verifies the binary compiles

Makefile targets available individually:

| Target | Description |
|--------|-------------|
| `make fmt` | Format code with goimports |
| `make lint` | Run golangci-lint |
| `make test` | Run tests with race detector |
| `make build` | Build adapter binary |
| `make build-runtime` | Build runtime binary (native) |
| `make build-runtime-arm64` | Cross-compile runtime for Linux ARM64 |
| `make check` | Run all checks (fmt + lint + test + build) |
| `make install-hooks` | Install the pre-commit hook |

Prerequisites: `go`, `golangci-lint`, `goimports` (`go install golang.org/x/tools/cmd/goimports@latest`), and sibling `../promptkit` checkout.

## Sibling Repo Dependency

This repo depends on `github.com/AltairaLabs/PromptKit/runtime` via `replace` directives in `go.mod` pointing to `../promptkit/runtime`. This is temporary until the next PromptKit release tags `runtime/v1.3.0`. Once released, the `update-deps.yml` workflow will auto-create a PR to drop the replace directives.

For local development, ensure `../promptkit` is checked out:
```bash
git clone git@github.com:AltairaLabs/PromptKit.git ../promptkit
```

## SonarCloud Quality Gate (CI)

SonarCloud runs on every PR and enforces the **Sonar Way** quality profile. The quality gate checks **new code only** (changes in the PR):

| Metric | Threshold | What it means |
|--------|-----------|---------------|
| Coverage | >= 80% | New/changed lines must be tested |
| Duplicated lines | <= 3% | Avoid copy-paste code |
| Reliability rating | A | No new bugs |
| Security rating | A | No new vulnerabilities |
| Maintainability rating | A | No new code smells (includes cognitive complexity) |
| Security hotspots reviewed | 100% | All hotspots must be triaged |

**Cognitive complexity** is the most common CI failure. SonarCloud uses a threshold of **15** (rule `go:S3776`). This is stricter than the local golangci-lint `gocognit` threshold of 15 (they happen to match in this project). Functions above 15 will create code smell issues that fail the quality gate.

**Duplicated string literals** (`go:S1192`): SonarCloud flags strings duplicated 3+ times. Extract to constants.

## Go Code Standards

- **Cognitive complexity**: Keep functions below **15**. Proactively extract helper functions.
- **Line length**: Max 120 characters (golangci-lint `lll`).
- **Magic numbers**: The `mnd` linter flags magic numbers in arguments, cases, conditions, operations, returns, and assignments. Extract to named constants.
- **Test coverage**: All changed files must have >= 80% coverage. Write tests for error paths and edge cases, not just happy paths.
- **Duplicated strings**: Extract string literals used 3+ times into constants.
- **Formatting**: `gofmt` and `goimports` are enforced (local prefix: `github.com/AltairaLabs/promptarena-deploy-agentcore`).
- **Test exclusions**: The `.golangci.yml` relaxes `mnd`, `gocognit`, `goconst`, `gosec`, `lll`, `gocritic`, `gochecknoinits`, `unused`, `errcheck`, `staticcheck`, `govet`, `whitespace`, and `revive` for `*_test.go` files.

## Testing Patterns

- **Unit tests**: Use `simulatedAWSClient` / `failingAWSClient` injection via `awsClientFunc` factory on `Provider`.
- **Table-driven tests** for config validation and resource planning.
- **Event-driven tests** for Apply/Destroy — collect callback events, assert ordering and content.
- **Integration tests**: Guard with env-var checks (`AGENTCORE_TEST_REGION`), use `//go:build integration` tag.
- **JSON-RPC protocol tests**: Use `adaptersdk.ServeIO()` to run the adapter in-process.

## Project Structure

### Deploy Adapter (`internal/agentcore/`)

| Path | Purpose |
|------|---------|
| `main.go` | Entry point — thin wrapper calling `adaptersdk.Serve(provider)` |
| `internal/agentcore/provider.go` | `Provider`, factories, `GetProviderInfo`, `ValidateConfig` |
| `internal/agentcore/config.go` | Config parsing, validation, JSON Schema definition |
| `internal/agentcore/arena_config.go` | Arena config deploy section parsing (region, model, binary path) |
| `internal/agentcore/plan.go` | Plan generation — diffs desired resources vs prior state |
| `internal/agentcore/apply.go` | Apply — creates resources in dependency-ordered phases |
| `internal/agentcore/codedeploy.go` | Code deploy — ZIP packaging with launcher script, S3 upload |
| `internal/agentcore/status.go` | Destroy + Status — teardown and health checks |
| `internal/agentcore/state.go` | `AdapterState` and `ResourceState` type definitions |
| `internal/agentcore/envvars.go` | Runtime environment variable generation |
| `internal/agentcore/gateway.go` | Tool gateway resource management |
| `internal/agentcore/cedar.go` | Cedar policy resource management |
| `internal/agentcore/aws_client.go` | `awsClient`, `resourceDestroyer`, `resourceChecker` interfaces |
| `internal/agentcore/aws_client_real.go` | Real AWS SDK implementation (`bedrockagentcorecontrol`) |
| `internal/agentcore/aws_client_simulated_test.go` | Simulated clients for unit tests |

### Runtime Binary (`cmd/agentcore-runtime/`)

| Path | Purpose |
|------|---------|
| `cmd/agentcore-runtime/main.go` | Runtime entrypoint — A2A server (port 9000) + HTTP bridge (port 8080) |
| `cmd/agentcore-runtime/config.go` | Runtime config from environment variables |
| `cmd/agentcore-runtime/server.go` | Server setup — A2A mux, agent card, state store |
| `cmd/agentcore-runtime/http_bridge.go` | HTTP bridge — translates `/invocations` to A2A `message/send` |
| `cmd/agentcore-runtime/health.go` | Health check handler (`/ping`) |
| `cmd/agentcore-runtime/otel.go` | OpenTelemetry tracing setup |
| `cmd/agentcore-runtime/version.go` | Version metadata (injected via ldflags) |

### Build & CI

| Path | Purpose |
|------|---------|
| `Makefile` | Build targets: fmt, lint, test, build, build-runtime, build-runtime-arm64, check |
| `.githooks/pre-commit` | Pre-commit hook — runs formatting, lint, test, build |
| `.golangci.yml` | Linter configuration (25 linters) |
| `.github/workflows/ci.yml` | CI pipeline (test + lint + SonarCloud) |
| `.github/workflows/release.yml` | Release pipeline (GoReleaser) |
| `.github/workflows/update-deps.yml` | Auto-update PromptKit dependency on release |

## Architecture

This project has two components:

### 1. Deploy Adapter

A JSON-RPC 2.0 subprocess that plugs into PromptKit's deploy framework. It is NOT a CLI tool — PromptKit discovers and invokes it as a subprocess.

**Interfaces:**

```
awsClient           — Create resources (runtime, gateway, a2a, evaluator, online_eval_config)
resourceDestroyer   — Delete resources (reverse dependency order)
resourceChecker     — Health check resources (healthy/unhealthy/missing)
```

All three are implemented by `realAWSClient` for production and by simulated/failing variants for tests. Dependency injection is via factory functions on `Provider`.

**Deploy Phases (Apply):**

1. **Tools** (0-17%): `CreateGatewayTool` for each pack tool (lazy parent gateway)
2. **Policies** (17-33%): `CreatePolicyEngine` + `CreateCedarPolicy` per prompt with validators
3. **Runtimes** (33-50%): `CreateRuntime` per agent member (polls until READY)
4. **A2A** (50-67%): `CreateA2AWiring` per agent (logical resource)
5. **Evaluators** (67-83%): `CreateEvaluator` per eval (`llm_as_judge` only)
6. **Online Eval Config** (83-100%): `CreateOnlineEvaluationConfig` (wires evaluators to traces)

**Destroy Order (reverse):**

online_eval_config → tool_gateway → cedar_policy → evaluator → a2a_endpoint → agent_runtime → memory

### 2. Runtime Binary

Runs inside AgentCore, serving the PromptKit agent via two protocols:

| Protocol | Port | Purpose |
|----------|------|---------|
| A2A | 9000 | Full A2A protocol — multi-agent, streaming, task management, agent cards |
| HTTP | 8080 | AgentCore HTTP contract — `/invocations` (external callers), `/ping` (health) |

The HTTP bridge on port 8080 translates `/invocations` requests (simple `{"prompt":"..."}` payloads) into A2A `message/send` calls on port 9000. It supports both `prompt` and `input` fields for compatibility with the AWS ecosystem convention.

For multi-agent deployments, other agents call directly via A2A on port 9000 — the HTTP bridge is only for external invocations (SDK, console, CLI).
