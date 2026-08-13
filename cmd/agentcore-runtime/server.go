package main

import (
	"fmt"
	"log/slog"
	"net/http"

	pkgconfig "github.com/AltairaLabs/PromptKit/pkg/config"
	"github.com/AltairaLabs/PromptKit/runtime/a2a"
	"github.com/AltairaLabs/PromptKit/runtime/prompt"
	"github.com/AltairaLabs/PromptKit/runtime/prompt/agentcard"
	"github.com/AltairaLabs/PromptKit/runtime/statestore"
	"github.com/AltairaLabs/PromptKit/sdk"

	"github.com/AltairaLabs/promptarena-deploy-agentcore/internal/agentcore"
)

// resolveAgentName determines which agent/prompt to serve.
// Priority: PROMPTPACK_AGENT env var > agents.entry > single prompt.
func resolveAgentName(cfg *runtimeConfig, pack *prompt.Pack) (string, error) {
	if cfg.AgentName != "" {
		return cfg.AgentName, nil
	}

	if pack.Agents != nil && pack.Agents.Entry != "" {
		return pack.Agents.Entry, nil
	}

	if len(pack.Prompts) == 1 {
		for name := range pack.Prompts {
			return name, nil
		}
	}

	return "", fmt.Errorf(
		"cannot determine agent name: set %s, define agents.entry in the pack, "+
			"or ensure the pack has exactly one prompt",
		envAgentName,
	)
}

// buildSDKOptions creates SDK options from runtime configuration.
func buildSDKOptions(cfg *runtimeConfig) []sdk.Option {
	var opts []sdk.Option

	opts = append(opts, providerOptions(cfg)...)
	opts = append(opts, sdk.WithStateStore(buildStateStore(cfg)))

	if len(cfg.AgentEndpoints) > 0 {
		opts = append(opts, sdk.WithAgentEndpoints(&sdk.MapEndpointResolver{
			Endpoints: cfg.AgentEndpoints,
		}))
	}

	return opts
}

// providerOptions builds one SDK option per provider binding. The primary
// LLM binding goes through WithBedrock so it keeps the platform credential
// chain; the remaining roles are wired through the capability-specific
// WithXProvider options.
func providerOptions(cfg *runtimeConfig) []sdk.Option {
	if cfg.AWSRegion == "" {
		return nil
	}

	primary := cfg.primaryProvider()

	// No bindings at all: preserve the historical behavior of configuring
	// Bedrock from the (possibly empty) legacy provider fields.
	if primary == nil && len(cfg.Providers) == 0 {
		return []sdk.Option{sdk.WithBedrock(cfg.AWSRegion, cfg.ProviderType, cfg.Model)}
	}

	var opts []sdk.Option
	if primary != nil {
		opts = append(opts, sdk.WithBedrock(cfg.AWSRegion, primary.Type, primary.Model))
	}

	for i := range cfg.Providers {
		p := cfg.Providers[i]
		if primary != nil && p.Name == primary.Name {
			continue
		}
		opt, ok := roleOption(p, cfg.AWSRegion)
		if !ok {
			slog.Warn("ignoring provider binding with unrecognized role",
				"binding", p.Name, "role", p.Role)
			continue
		}
		opts = append(opts, opt)
	}
	return opts
}

// roleOption maps a resolved binding to the SDK option for its capability.
// Returns false for a role the SDK has no provider option for.
func roleOption(p agentcore.ResolvedProvider, region string) (sdk.Option, bool) {
	spec := bedrockProviderSpec(p, region)
	switch p.Role {
	case agentcore.RoleLLM:
		return sdk.WithLLMProvider(spec), true
	case agentcore.RoleEmbedding:
		return sdk.WithEmbeddingProvider(spec), true
	case agentcore.RoleTTS:
		return sdk.WithTTSProvider(spec), true
	case agentcore.RoleSTT:
		return sdk.WithSTTProvider(spec), true
	case agentcore.RoleImage:
		return sdk.WithImageProvider(spec), true
	case agentcore.RoleInference:
		return sdk.WithInferenceProvider(spec), true
	default:
		return nil, false
	}
}

// bedrockProviderSpec builds the SDK provider spec for a binding, carrying
// the Bedrock platform config so the provider uses the AWS credential chain
// rather than expecting an API key.
func bedrockProviderSpec(p agentcore.ResolvedProvider, region string) sdk.ProviderSpec {
	return sdk.ProviderSpec{
		ID:    p.Name,
		Type:  p.Type,
		Model: p.Model,
		Platform: &pkgconfig.PlatformConfig{
			Type:   "bedrock",
			Region: region,
		},
	}
}

// buildStateStore creates the appropriate state store based on config.
// If a memory ID and AWS region are configured, it uses the AgentCore
// data-plane SDK. Otherwise it falls back to a volatile in-memory store.
func buildStateStore(cfg *runtimeConfig) statestore.Store {
	if cfg.MemoryID != "" && cfg.AWSRegion != "" {
		dpClient, err := agentcore.NewDataPlaneClient(cfg.AWSRegion)
		if err != nil {
			slog.Warn("AgentCore memory init failed, using in-memory store",
				"error", err)
			return statestore.NewMemoryStore()
		}
		return agentcore.NewStateStore(cfg.MemoryID, dpClient)
	}
	return statestore.NewMemoryStore()
}

// buildAgentCard generates an A2A AgentCard for the named agent from the pack.
// Falls back to a minimal card if the pack has no agents section.
func buildAgentCard(pack *prompt.Pack, agentName string) *a2a.AgentCard {
	cards := agentcard.GenerateAgentCards(pack)
	if card, ok := cards[agentName]; ok {
		return card
	}

	// Fallback: minimal card from pack metadata.
	description := ""
	if p, ok := pack.Prompts[agentName]; ok {
		description = p.Description
	}
	return &a2a.AgentCard{
		Name:        agentName,
		Description: description,
		Version:     pack.Version,
	}
}

// buildMux creates the HTTP mux with A2A and health routes.
func buildMux(a2aHandler, healthH http.Handler) *http.ServeMux {
	mux := http.NewServeMux()
	mux.Handle("/health", healthH)
	mux.Handle("/ping", healthH) // AgentCore health check endpoint
	// A2AServer.Handler() registers /.well-known/agent.json and /a2a internally.
	mux.Handle("/", a2aHandler)
	return mux
}
