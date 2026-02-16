package main

import (
	"context"
	"fmt"

	"github.com/AltairaLabs/PromptKit/runtime/deploy"
)

// Plan generates a deployment plan for the given pack and config.
func (p *AgentCoreProvider) Plan(_ context.Context, _ *deploy.PlanRequest) (*deploy.PlanResponse, error) {
	return nil, fmt.Errorf("agentcore: Plan not yet implemented")
}
