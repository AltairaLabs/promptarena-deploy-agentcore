# promptarena-deploy-agentcore

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
- **Plan**: Stub (not yet implemented)
- **Apply**: Stub (not yet implemented)
- **Destroy**: Stub (not yet implemented)
- **Status**: Stub (not yet implemented)

## Development

```bash
# Build
go build -o promptarena-deploy-agentcore .

# Test
go test ./... -v -race -count=1

# JSON-RPC handshake
echo '{"jsonrpc":"2.0","method":"get_provider_info","id":1}' | ./promptarena-deploy-agentcore
```

## License

Apache-2.0
