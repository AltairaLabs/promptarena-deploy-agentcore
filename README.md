# promptarena-deploy-agentcore

[![CI](https://github.com/AltairaLabs/promptarena-deploy-agentcore/workflows/CI/badge.svg)](https://github.com/AltairaLabs/promptarena-deploy-agentcore/actions/workflows/ci.yml)
[![Quality Gate Status](https://sonarcloud.io/api/project_badges/measure?project=AltairaLabs_promptarena-deploy-agentcore&metric=alert_status)](https://sonarcloud.io/summary/new_code?id=AltairaLabs_promptarena-deploy-agentcore)
[![Coverage](https://sonarcloud.io/api/project_badges/measure?project=AltairaLabs_promptarena-deploy-agentcore&metric=coverage)](https://sonarcloud.io/summary/new_code?id=AltairaLabs_promptarena-deploy-agentcore)
[![Go Report Card](https://goreportcard.com/badge/github.com/AltairaLabs/promptarena-deploy-agentcore)](https://goreportcard.com/report/github.com/AltairaLabs/promptarena-deploy-agentcore)
[![Go Reference](https://pkg.go.dev/badge/github.com/AltairaLabs/promptarena-deploy-agentcore.svg)](https://pkg.go.dev/github.com/AltairaLabs/promptarena-deploy-agentcore)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)

AWS Bedrock AgentCore deploy adapter for [PromptKit](https://github.com/AltairaLabs/PromptKit).

This binary implements the PromptKit `deploy.Provider` interface, communicating via JSON-RPC 2.0 over stdio. It is discovered and launched by the `promptarena deploy` command.

## Installation

```bash
promptarena deploy adapter install agentcore
```

Or build from source:

```bash
go build -o promptarena-deploy-agentcore .
```

## Configuration

The adapter requires the following configuration in your deploy config:

```yaml
provider: agentcore
config:
  region: us-west-2
  runtime_role_arn: arn:aws:iam::123456789012:role/my-agent-role
  memory_store: session          # optional: "session" or "persistent"
  tools:                         # optional
    code_interpreter: true
  observability:                 # optional
    cloudwatch_log_group: /aws/agentcore/my-agent
    tracing_enabled: true
```

## Status

- **GetProviderInfo**: Implemented
- **ValidateConfig**: Implemented
- **Plan**: Implemented
- **Apply**: Implemented
- **Destroy**: Implemented
- **Status**: Implemented

## Development

```bash
# Build
go build -o promptarena-deploy-agentcore .

# Test
go test ./... -v -race -count=1

# Integration tests (requires AWS credentials)
AGENTCORE_TEST_REGION=us-west-2 AGENTCORE_TEST_ROLE_ARN=arn:aws:iam::123456789012:role/test \
  go test -tags=integration -v ./...

# JSON-RPC handshake
echo '{"jsonrpc":"2.0","method":"get_provider_info","id":1}' | ./promptarena-deploy-agentcore
```

## License

MIT
