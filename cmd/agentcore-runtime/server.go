package main

import (
	"fmt"
	"log/slog"
	"net/http"

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

	if cfg.AWSRegion != "" {
		// Provider type and model come from the arena config via env vars.
		opts = append(opts, sdk.WithBedrock(cfg.AWSRegion, cfg.ProviderType, cfg.Model))
	}

	opts = append(opts, sdk.WithStateStore(buildStateStore(cfg)))

	if len(cfg.AgentEndpoints) > 0 {
		opts = append(opts, sdk.WithAgentEndpoints(&sdk.MapEndpointResolver{
			Endpoints: cfg.AgentEndpoints,
		}))
	}

	return opts
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
