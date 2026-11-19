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

The adapter reads configuration from your arena config's `spec.deploy` section. Everything under `deploy.config` is opaque to PromptKit and passed straight to this adapter:

```yaml
apiVersion: promptkit.altairalabs.ai/v1alpha1
kind: Arena
metadata:
  name: my-agent
spec:
  providers:
    - file: providers/sonnet.provider.yaml

  defaults:
    temperature: 0.1
    max_tokens: 512

  deploy:
    provider: agentcore
    config:
      region: us-west-2
      runtime_binary_path: /path/to/promptkit-runtime   # cross-compiled runtime binary (or runtime_override.image)
      runtime_role_arn: arn:aws:iam::123456789012:role/my-agent-role

      providers:                   # what to deploy, and in what role
        - name: default            # "default" is the primary provider
          role: llm
          arena_provider: sonnet   # id of a provider from spec.providers

      memory_store: session        # optional: "session", "persistent", or compound form
      tools:                       # optional
        code_interpreter: true
      observability:               # optional
        cloudwatch_log_group: /aws/agentcore/my-agent
        tracing_enabled: true
```

> The adapter config goes under `deploy.config`. `deploy.agentcore` is not a valid key — PromptKit's arena schema accepts only `provider`, `config` and `environments` under `deploy`.

### Deployment Types

The adapter deploys a runtime in one of two ways:

- **Code deploy** (default): Packages the runtime binary with a launcher script, uploads to S3, and deploys via AgentCore's code deployment. The runtime binary is cross-compiled for Linux ARM64 (`make build-runtime-arm64`).
- **Container**: Deploys a container image named by [`runtime_override`](#run-another-container). `runtime_binary_path` is then not needed.

## Run another container

`runtime_override` deploys a container of your choosing in place of the
adapter's runtime. The adapter still creates one runtime per agent, still sets
its [environment variables](docs/src/content/docs/reference/environment-variables.md),
and hands the container the pack: `PROMPTPACK_PACK_JSON` (the pack as
single-line JSON) when it fits in one environment value, otherwise
`PROMPTPACK_PACK_URI` (an `s3://` URI the adapter staged it to), plus
`PROMPTPACK_AGENT`.

| Field | On AgentCore |
|---|---|
| `image` | Deployed as a container artifact instead of the code package. Must be in Amazon ECR (below) and built for `linux/arm64` |
| `env` | Added to the runtime's environment. A name may not start with `PROMPTPACK_` or `AWS_`, or be `OTEL_EXPORTER_OTLP_ENDPOINT`. A value may be at most 5,000 characters, with no control characters: write JSON on one line |
| `secret_env` | Refused. AgentCore's environment takes plain values only, and they are readable by anyone who can describe the runtime. Have the container read a secret from Secrets Manager with the runtime role instead |
| `command`, `args` | Refused. AgentCore runs an image's own entrypoint and has no way to change it |

**The image must be in Amazon ECR.** AgentCore pulls only from a private ECR
repository (`<account>.dkr.ecr.<region>.amazonaws.com/...`) or ECR Public
(`public.ecr.aws/...`), and validation refuses anything else. To run an image
published elsewhere, copy it into a repository in the runtime's region:

```bash
aws ecr create-repository --region us-east-1 --repository-name my-runtime
aws ecr get-login-password --region us-east-1 \
  | crane auth login 123456789012.dkr.ecr.us-east-1.amazonaws.com -u AWS --password-stdin
crane copy ghcr.io/example/my-runtime:v1 \
  123456789012.dkr.ecr.us-east-1.amazonaws.com/my-runtime:v1
```

The runtime role must be able to pull it:

```json
{
  "Effect": "Allow",
  "Action": ["ecr:BatchGetImage", "ecr:GetDownloadUrlForLayer"],
  "Resource": "arn:aws:ecr:us-east-1:123456789012:repository/my-runtime"
},
{ "Effect": "Allow", "Action": "ecr:GetAuthorizationToken", "Resource": "*" }
```

**The container must serve AgentCore's runtime contract.** AgentCore sends
`POST /invocations` and `GET /ping` to port 8080, and decides from them whether
the container is healthy. It starts containers for runtime sessions (an
`InvokeAgentRuntime` call starts one) and stops them when the session ends; do
not rely on a container running while no session is active. A container that
serves neither route still starts and runs, but every invocation fails with a
424, and AgentCore may stop and start it repeatedly.

Every apply updates each existing runtime, so a change to `runtime_override`
takes effect on the next apply. The plan names the override image on each
runtime it creates or updates.

### Run omnia-dialhome

[omnia-dialhome](https://github.com/AltairaLabs/omnia-dialhome) runs a pack as
the runtime of an agent in an Omnia workspace: the container opens one
outbound connection to the agent's relay and serves its conversations over it,
with policy, sessions, evals and memory staying in Omnia. It reads the pack and
the agent from the adapter's `PROMPTPACK_PACK_JSON` and `PROMPTPACK_AGENT`.

1. Copy the image into ECR, as above:
   `ghcr.io/altairalabs/omnia-dialhome:<version>`, v0.2.1 or later. It is
   published for `linux/arm64`.
2. Deploy with the override. Omnia issues the dial-home URL and key together,
   in the agent's `<agent>-dial-home-key` Secret (keys `url` and `key`):

   ```yaml
   deploy:
     provider: agentcore
     config:
       region: us-east-1
       runtime_role_arn: arn:aws:iam::123456789012:role/my-agent-role
       providers:
         - { name: default, role: llm, type: claude, model: claude-sonnet-4 }
       runtime_override:
         image: 123456789012.dkr.ecr.us-east-1.amazonaws.com/omnia-dialhome:v0.2.1
         env:
           OMNIA_DIAL_HOME_URL: wss://my-agent-dial-home.example.com/v1/channel
           OMNIA_DIAL_HOME_KEY: <key>
           OMNIA_DIALHOME_PORT: "8080"
   ```

`OMNIA_DIALHOME_PORT` makes it listen on the port AgentCore expects.
`providers` is still required by the adapter, but omnia-dialhome takes its
model provider from Omnia over the channel and ignores `PROMPTPACK_PROVIDERS`.

Things to know:

- **The key is stored in plain text** on the runtime, because AgentCore has no
  secret environment variables. Anyone who can call `GetAgentRuntime` can read
  it. Scope that permission accordingly.
- **A session starts it.** Nothing runs, and nothing dials home, until
  something calls `InvokeAgentRuntime` to start a runtime session. That call
  fails with a 424, because omnia-dialhome answers `/invocations` with a 501:
  conversations reach it through Omnia, over the channel, never through
  `InvokeAgentRuntime`. The session's container keeps running, and keeps
  dialling home, after the 424.
- **Use v0.2.1 or later.** Earlier versions do not answer AgentCore's
  `/ping`, and AgentCore stops and restarts their containers repeatedly.
- A pack over 5,000 characters is staged to S3 as `PROMPTPACK_PACK_URI`, and
  omnia-dialhome refuses a staged pack. Keep the pack small, or build an image
  `FROM` omnia-dialhome with the pack copied in and set `OMNIA_DIALHOME_PACK`
  in `env`.

## Architecture

### Deploy Adapter

The deploy adapter is a JSON-RPC 2.0 subprocess that PromptKit discovers and invokes. It manages the full lifecycle of AgentCore resources:

**Deploy Phases (Apply)**:

1. **Tools** (0-17%): Create gateway tools for each pack tool
2. **Policies** (17-33%): Create the pack's Cedar policy engine and its policies
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

- Go 1.26+
- `golangci-lint`
- `goimports` (`go install golang.org/x/tools/cmd/goimports@latest`)

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
