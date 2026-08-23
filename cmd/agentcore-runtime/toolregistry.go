package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"

	"github.com/AltairaLabs/PromptKit/runtime/prompt"
	"github.com/AltairaLabs/PromptKit/runtime/tools"
	"github.com/AltairaLabs/PromptKit/sdk"
)

// buildToolRegistry assembles the registry the conversation will use.
//
// Supplying a registry means the pack's own tool repository is not attached,
// so this owns the entire tool surface: a tool missing from here is a tool the
// model never sees, with nothing logged and no error raised. That is why it
// walks the pack and reports what it could not wire, rather than walking the
// specs and trusting them to be complete.
//
// Returns nil when the pack declares no tools, so the SDK builds its own
// registry from the pack exactly as before.
func buildToolRegistry(
	pack *prompt.Pack, specs map[string]toolSpec,
	region string, creds aws.CredentialsProvider,
	gatewayURL, gatewayAuth string, log *slog.Logger,
) *tools.Registry {
	if pack == nil || len(pack.Tools) == 0 {
		return nil
	}

	registry := tools.NewRegistry()
	targets := gatewayTargets(specs)

	if len(targets) > 0 {
		if gatewayURL == "" {
			log.Error("tools are deployed behind a gateway but no gateway endpoint was provided",
				"tools", len(targets))
		} else {
			registry.RegisterExecutor(newGatewayExecutor(
				gatewayURL, region, gatewayAuth, creds, targets))
		}
	}

	var wired, skipped int
	for name := range pack.Tools {
		spec := specs[name]
		desc, err := descriptorFor(name, pack.Tools[name], &spec)
		if err != nil {
			// Named rather than dropped: a tool that silently disappears
			// looks to an operator like the model choosing not to call it.
			log.Error("tool will not be available to the agent",
				"tool", name, "error", err)
			skipped++
			continue
		}
		if err := registry.Register(desc); err != nil {
			log.Error("tool will not be available to the agent",
				"tool", name, "error", err)
			skipped++
			continue
		}
		wired++
	}

	log.Info("tools registered", "wired", wired, "skipped", skipped,
		"gateway_backed", len(targets))
	return registry
}

// gatewayTargets maps pack tool names to their Gateway target names.
func gatewayTargets(specs map[string]toolSpec) map[string]string {
	targets := make(map[string]string)
	for name := range specs {
		if specs[name].Mode == toolModeGateway && specs[name].GatewayTarget != "" {
			targets[name] = specs[name].GatewayTarget
		}
	}
	return targets
}

// descriptorFor merges the two halves of a tool definition.
//
// The pack holds the schema the model is shown; the spec holds what happens
// when it is called. Neither half is sufficient alone.
func descriptorFor(
	name string, packTool *prompt.PackTool, spec *toolSpec,
) (*tools.ToolDescriptor, error) {
	if packTool == nil {
		return nil, fmt.Errorf("not declared in the pack")
	}

	schema, err := json.Marshal(packTool.Parameters)
	if err != nil {
		return nil, fmt.Errorf("encode parameters: %w", err)
	}

	desc := &tools.ToolDescriptor{
		Name:        name,
		Description: packTool.Description,
		InputSchema: schema,
	}

	switch spec.Mode {
	case toolModeGateway:
		if spec.GatewayTarget == "" {
			return nil, fmt.Errorf("deployed as a gateway tool but names no target")
		}
		desc.Mode = gatewayExecutorName

	case toolModeLive:
		if spec.URL == "" {
			return nil, fmt.Errorf("mode %q without a url", toolModeLive)
		}
		desc.Mode = toolModeLive
		desc.HTTPConfig = &tools.HTTPConfig{
			URL:    spec.URL,
			Method: spec.Method,
		}

	case toolModeMock, "":
		desc.Mode = toolModeMock
		if spec.MockTemplate != "" {
			desc.MockTemplate = spec.MockTemplate
			break
		}
		if spec.MockResult != nil {
			raw, mErr := json.Marshal(spec.MockResult)
			if mErr != nil {
				return nil, fmt.Errorf("encode mock_result: %w", mErr)
			}
			desc.MockResult = raw
			break
		}
		return nil, fmt.Errorf("mode %q with neither mock_result nor mock_template",
			toolModeMock)

	default:
		return nil, fmt.Errorf("unsupported mode %q", spec.Mode)
	}

	return desc, nil
}

// awsCredentials resolves the credentials a gateway call is signed with.
//
// Returns nil when they cannot be loaded, which the executor reports at call
// time. A gateway that authorizes with NONE never needs them, so failing
// startup here would break a configuration that works.
func awsCredentials(
	ctx context.Context, cfg *runtimeConfig, log *slog.Logger,
) aws.CredentialsProvider {
	if cfg.ToolGatewayURL == "" {
		return nil
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(cfg.AWSRegion))
	if err != nil {
		log.Warn("could not load AWS credentials for gateway calls", "error", err)
		return nil
	}
	return awsCfg.Credentials
}

// withToolRegistry adds the tool registry option when the pack has tools.
//
// Separated from startup so that assembling the tool surface — which has its
// own failure modes worth reading in one place — does not thread through the
// server's own setup.
func withToolRegistry(
	opts []sdk.Option, cfg *runtimeConfig, pack *prompt.Pack, log *slog.Logger,
) ([]sdk.Option, error) {
	specs, err := parseToolSpecs(cfg.ToolSpecsJSON)
	if err != nil {
		return nil, fmt.Errorf("tool specs: %w", err)
	}

	reg := buildToolRegistry(pack, specs, cfg.AWSRegion,
		awsCredentials(context.Background(), cfg, log),
		cfg.ToolGatewayURL, cfg.ToolGatewayAuth, log)
	if reg == nil {
		return opts, nil
	}
	return append(opts, sdk.WithToolRegistry(reg)), nil
}
