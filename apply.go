package main

import (
	"context"
	"fmt"

	"github.com/AltairaLabs/PromptKit/runtime/deploy"
)

// Apply executes a deployment plan, streaming progress events via the callback.
func (p *AgentCoreProvider) Apply(_ context.Context, _ *deploy.PlanRequest, _ deploy.ApplyCallback) (string, error) {
	return "", fmt.Errorf("agentcore: Apply not yet implemented")
}
