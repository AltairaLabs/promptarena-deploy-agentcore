# promptarena-deploy-agentcore

[![CI](https://github.com/AltairaLabs/promptarena-deploy-agentcore/workflows/CI/badge.svg)](https://github.com/AltairaLabs/promptarena-deploy-agentcore/actions/workflows/ci.yml)
[![Quality Gate Status](https://sonarcloud.io/api/project_badges/measure?project=AltairaLabs_promptarena-deploy-agentcore&metric=alert_status)](https://sonarcloud.io/summary/new_code?id=AltairaLabs_promptarena-deploy-agentcore)
[![Coverage](https://sonarcloud.io/api/project_badges/measure?project=AltairaLabs_promptarena-deploy-agentcore&metric=coverage)](https://sonarcloud.io/summary/new_code?id=AltairaLabs_promptarena-deploy-agentcore)
[![Go Report Card](https://goreportcard.com/badge/github.com/AltairaLabs/promptarena-deploy-agentcore)](https://goreportcard.com/report/github.com/AltairaLabs/promptarena-deploy-agentcore)
[![Go Reference](https://pkg.go.dev/badge/github.com/AltairaLabs/promptarena-deploy-agentcore.svg)](https://pkg.go.dev/github.com/AltairaLabs/promptarena-deploy-agentcore)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)

AWS Bedrock AgentCore deploy adapter for [PromptKit](https://github.com/AltairaLabs/PromptKit).

This adapter implements the PromptKit `deploy.Provider` interface, communicating via JSON-RPC 2.0 over stdio. It is discovered and launched by the `promptarena deploy` command. It also includes a **runtime binary** (`agentcore-runtime`) that runs inside AgentCore, serving the PromptKit agent via both the HTTP protocol contract (port 8080) and the A2A protocol (port 9000).

## Installation

```bash
promptarena deploy adapter install agentcore
```

Or build from source:

```bash
# Build the deploy adapter
GOWORK=off go build -o promptarena-deploy-agentcore .

# Build the runtime binary (for local testing)
make build-runtime

# Cross-compile runtime for AgentCore (Linux ARM64)
make build-runtime-arm64
```

## Configuration

The adapter reads configuration from your arena config's `deploy` section:

```yaml
deploy:
  provider: agentcore
  agentcore:
    region: us-west-2
    runtime_binary_path: /path/to/promptkit-runtime   # path to cross-compiled runtime binary
    model: claude-3-5-haiku-20241022                  # Bedrock model ID
    runtime_role_arn: arn:aws:iam::123456789012:role/my-agent-role
    memory_store: session          # optional: "session", "persistent", or compound form
    tools:                         # optional
      code_interpreter: true
    observability:                 # optional
      cloudwatch_log_group: /aws/agentcore/my-agent
      tracing_enabled: true
```

### Deployment Types

The adapter supports **code deploy** for runtime deployment:

- **Code deploy**: Packages the runtime binary with a launcher script, uploads to S3, and deploys via AgentCore's code deployment. The runtime binary is cross-compiled for Linux ARM64 (`make build-runtime-arm64`).

## Architecture

### Deploy Adapter

The deploy adapter is a JSON-RPC 2.0 subprocess that PromptKit discovers and invokes. It manages the full lifecycle of AgentCore resources:

**Deploy Phases (Apply)**:

1. **Tools** (0-17%): Create gateway tools for each pack tool
2. **Policies** (17-33%): Create Cedar policy engine and policies per prompt
3. **Runtimes** (33-50%): Create agent runtime (polls until READY)
4. **A2A** (50-67%): Wire A2A endpoint per agent
5. **Evaluators** (67-83%): Create evaluators (`llm_as_judge`)
6. **Online Eval Config** (83-100%): Wire evaluators to traces

**Destroy** runs in reverse dependency order.

### Runtime Binary

The `agentcore-runtime` binary runs inside AgentCore and serves two protocols:

| Protocol | Port | Endpoint | Purpose |
|----------|------|----------|---------|
| HTTP | 8080 | `POST /invocations` | External callers (SDK, console, CLI) |
| HTTP | 8080 | `GET /ping` | Health check |
| A2A | 9000 | `POST /a2a` | Agent-to-agent communication |
| A2A | 9000 | `GET /.well-known/agent.json` | Agent card discovery |

The **HTTP bridge** on port 8080 translates `/invocations` requests into A2A `message/send` calls on port 9000. It accepts payloads with either `prompt` or `input` fields, matching the AWS ecosystem convention:

```json
{"prompt": "Explain machine learning in one sentence."}
```

Response format:

```json
{"response": "Machine learning is...", "status": "success"}
```

The **A2A server** on port 9000 provides the full [A2A protocol](https://a2a-protocol.org/) for multi-agent deployments, including streaming (`message/stream`), task management, conversation context, and agent card discovery.

## Status

All adapter lifecycle methods are implemented:

- **GetProviderInfo**: Returns adapter metadata and config schema
- **ValidateConfig**: Validates deploy configuration
- **Plan**: Generates resource plan (diff desired vs prior state)
- **Apply**: Creates/updates AgentCore resources
- **Destroy**: Tears down resources in reverse dependency order
- **Status**: Health checks deployed resources

## Development

### Prerequisites

- Go 1.25+
- `golangci-lint`
- `goimports` (`go install golang.org/x/tools/cmd/goimports@latest`)
- Sibling `../promptkit` checkout (`git clone git@github.com:AltairaLabs/PromptKit.git ../promptkit`)

### Build & Test

```bash
# Build adapter
GOWORK=off go build -o promptarena-deploy-agentcore .

# Build runtime (local)
make build-runtime

# Cross-compile runtime for AgentCore
make build-runtime-arm64

# Test with race detector
GOWORK=off go test ./... -v -race -count=1

# Lint
golangci-lint run

# Integration tests (requires AWS credentials)
AGENTCORE_TEST_REGION=us-west-2 \
AGENTCORE_TEST_RUNTIME_ARN=arn:aws:bedrock-agentcore:us-west-2:123:runtime/my-runtime \
  GOWORK=off go test -tags=integration -v ./internal/agentcore/

# JSON-RPC handshake
echo '{"jsonrpc":"2.0","method":"get_provider_info","id":1}' | ./promptarena-deploy-agentcore

# Install pre-commit hook
make install-hooks
```

### Makefile Targets

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

## License

MIT
