package agentcore

import (
	"context"
	"fmt"

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
      "oneOf": [
        {"type": "string"},
        {"type": "array", "items": {"type": "string"}},
        {
          "type": "object",
          "properties": {
            "strategies": {"type": "array", "items": {"type": "string"}},
            "event_expiry_days": {"type": "integer", "minimum": 3, "maximum": 365},
            "encryption_key_arn": {"type": "string"}
          },
          "required": ["strategies"]
        }
      ],
      "description": "Memory config: string, array, or object with strategies"
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
    },
    "tags": {
      "type": "object",
      "additionalProperties": { "type": "string" },
      "description": "User-defined tags to apply to all created AWS resources"
    },
    "dry_run": {
      "type": "boolean",
      "description": "When true, Apply simulates resource creation without calling AWS APIs"
    },
    "a2a_auth": {
      "type": "object",
      "required": ["mode"],
      "properties": {
        "mode": {
          "type": "string",
          "enum": ["iam", "jwt"],
          "description": "A2A authentication mode"
        },
        "discovery_url": {
          "type": "string",
          "description": "OIDC discovery URL (required for jwt mode)"
        },
        "allowed_audience": {
          "type": "array",
          "items": { "type": "string" },
          "description": "Allowed JWT audiences"
        },
        "allowed_clients": {
          "type": "array",
          "items": { "type": "string" },
          "description": "Allowed JWT client IDs"
        }
      }
    },
    "container_image": {
      "type": "string",
      "description": "Container image URI for agent runtimes (default: ghcr.io/altairalabs/promptkit-agentcore:latest)"
    },
    "agent_overrides": {
      "type": "object",
      "additionalProperties": {
        "type": "object",
        "properties": {
          "container_image": {
            "type": "string",
            "description": "Container image URI override for this agent"
          }
        }
      },
      "description": "Per-agent configuration overrides keyed by agent name"
    }
  },
  "additionalProperties": false
}`

// awsClientFactory creates an awsClient for the given config.
type awsClientFactory func(ctx context.Context, cfg *Config) (awsClient, error)

// destroyerFactory creates a resourceDestroyer for the given config.
type destroyerFactory func(ctx context.Context, cfg *Config) (resourceDestroyer, error)

// checkerFactory creates a resourceChecker for the given config.
type checkerFactory func(ctx context.Context, cfg *Config) (resourceChecker, error)

// Provider implements deploy.Provider for AWS Bedrock AgentCore.
type Provider struct {
	awsClientFunc awsClientFactory
	destroyerFunc destroyerFactory
	checkerFunc   checkerFactory
}

// NewProvider creates a new Provider with the real AWS
// client factories. Credentials are resolved via the standard
// aws-sdk-go-v2/config chain.
func NewProvider() *Provider {
	return &Provider{
		awsClientFunc: newRealAWSClientFactory,
		destroyerFunc: newRealDestroyerFactory,
		checkerFunc:   newRealCheckerFactory,
	}
}

// GetProviderInfo returns metadata about the agentcore adapter.
func (p *Provider) GetProviderInfo(_ context.Context) (*deploy.ProviderInfo, error) {
	return &deploy.ProviderInfo{
		Name:         "agentcore",
		Version:      Version,
		Capabilities: []string{"plan", "apply", "destroy", "status", "diagnose"},
		ConfigSchema: configSchema,
	}, nil
}

// ValidateConfig parses and validates the provider configuration.
// In addition to hard errors, it runs diagnostic checks and appends
// non-fatal warnings so the user can fix common misconfigurations.
func (p *Provider) ValidateConfig(
	_ context.Context, req *deploy.ValidateRequest,
) (*deploy.ValidateResponse, error) {
	cfg, err := parseConfig(req.Config)
	if err != nil {
		return &deploy.ValidateResponse{
			Valid:  false,
			Errors: []string{err.Error()},
		}, nil
	}

	errs := cfg.validate()

	// Append diagnostic warnings as informational entries.
	warnings := DiagnoseConfig(cfg)
	for _, w := range warnings {
		errs = append(errs, "warning: "+w.String())
	}

	// Only validation errors (not warnings) affect the valid flag.
	validationErrorCount := len(errs) - len(warnings)
	return &deploy.ValidateResponse{
		Valid:  validationErrorCount == 0,
		Errors: errs,
	}, nil
}

// Import imports an existing AWS resource into the adapter state.
func (p *Provider) Import(
	_ context.Context, _ *deploy.ImportRequest,
) (*deploy.ImportResponse, error) {
	return nil, fmt.Errorf("agentcore: import is not yet supported")
}
