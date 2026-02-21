//go:build integration

package agentcore

import (
	"testing"

	"github.com/AltairaLabs/PromptKit/runtime/deploy"
)

// TestIntegration_ApplyOnly creates resources and leaves them running for inspection.
// Run with: go test -tags=integration -v -run TestIntegration_ApplyOnly -timeout=30m ./internal/agentcore/
func TestIntegration_ApplyOnly(t *testing.T) {
	deployConfig := integrationDeployConfig(t)
	arenaConfig := integrationArenaConfig(t)
	provider := NewProvider()
	ctx := integrationContext(t)

	// --- Messaround pack (6 resources) ---
	t.Log("=== Applying messaround pack ===")
	messaroundPack := loadMessaroundPack(t)
	_, messState := collectApplyEvents(t, ctx, provider, &deploy.PlanRequest{
		PackJSON:     messaroundPack,
		DeployConfig: deployConfig,
		ArenaConfig:  arenaConfig,
	})
	state := unmarshalAdapterState(t, messState)
	t.Logf("Messaround: %d resources", len(state.Resources))
	for _, r := range state.Resources {
		t.Logf("  %s/%s  arn=%s", r.Type, r.Name, r.ARN)
	}

	// --- Multi-agent pack (5 resources) ---
	t.Log("=== Applying multi-agent pack ===")
	multiPack := integrationMultiAgentPackJSON()
	_, multiState := collectApplyEvents(t, ctx, provider, &deploy.PlanRequest{
		PackJSON:     multiPack,
		DeployConfig: deployConfig,
		ArenaConfig:  arenaConfig,
	})
	state2 := unmarshalAdapterState(t, multiState)
	t.Logf("Multi-agent: %d resources", len(state2.Resources))
	for _, r := range state2.Resources {
		t.Logf("  %s/%s  arn=%s", r.Type, r.Name, r.ARN)
	}

	t.Log("")
	t.Log("=== Resources left running for inspection ===")
	t.Log("Run the following to destroy when done:")
	t.Log("  go test -tags=integration -v -run TestIntegration_DestroyAll -timeout=30m ./internal/agentcore/")
}
