package main

import (
	"context"

	"github.com/AltairaLabs/PromptKit/runtime/deploy"
)

// configSchema is the JSON Schema for the agentcore provider config.
const configSchema = `{
  "$schema": "http://json-schema.org/draft-07/schema#",
  "type": "object",
  "required": ["region", "runtime_role_arn"],
  "properties": {
    "region": {
      "type": "string",
      "pattern": "^[a-z]{2}-[a-z]+-\\d+$",
      "description": "AWS region for AgentCore deployment"
    },
    "runtime_role_arn": {
      "type": "string",
      "pattern": "^arn:aws:iam::\\d{12}:role/.+$",
      "description": "IAM role ARN for the AgentCore runtime"
    },
    "memory_store": {
      "type": "string",
      "enum": ["session", "persistent"],
      "description": "Memory store type for the agent"
    },
    "tools": {
      "type": "object",
      "properties": {
        "code_interpreter": { "type": "boolean" }
      }
    },
    "observability": {
      "type": "object",
      "properties": {
        "cloudwatch_log_group": { "type": "string" },
        "tracing_enabled": { "type": "boolean" }
      }
    }
  },
  "additionalProperties": false
}`

// AgentCoreProvider implements deploy.Provider for AWS Bedrock AgentCore.
type AgentCoreProvider struct{}

// NewAgentCoreProvider creates a new AgentCoreProvider.
func NewAgentCoreProvider() *AgentCoreProvider {
	return &AgentCoreProvider{}
}

// GetProviderInfo returns metadata about the agentcore adapter.
func (p *AgentCoreProvider) GetProviderInfo(_ context.Context) (*deploy.ProviderInfo, error) {
	return &deploy.ProviderInfo{
		Name:         "agentcore",
		Version:      Version,
		Capabilities: []string{"plan", "apply", "destroy", "status"},
		ConfigSchema: configSchema,
	}, nil
}

// ValidateConfig parses and validates the provider configuration.
func (p *AgentCoreProvider) ValidateConfig(_ context.Context, req *deploy.ValidateRequest) (*deploy.ValidateResponse, error) {
	cfg, err := parseConfig(req.Config)
	if err != nil {
		return &deploy.ValidateResponse{
			Valid:  false,
			Errors: []string{err.Error()},
		}, nil
	}

	errs := cfg.validate()
	return &deploy.ValidateResponse{
		Valid:  len(errs) == 0,
		Errors: errs,
	}, nil
}
