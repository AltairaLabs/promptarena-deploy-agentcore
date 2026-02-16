package main

import (
	"context"
	"fmt"

	"github.com/AltairaLabs/PromptKit/runtime/deploy"
)

// Destroy tears down deployed resources, streaming progress events via the callback.
func (p *AgentCoreProvider) Destroy(_ context.Context, _ *deploy.DestroyRequest, _ deploy.DestroyCallback) error {
	return fmt.Errorf("agentcore: Destroy not yet implemented")
}

// Status returns the current deployment status.
func (p *AgentCoreProvider) Status(_ context.Context, _ *deploy.StatusRequest) (*deploy.StatusResponse, error) {
	return nil, fmt.Errorf("agentcore: Status not yet implemented")
}
