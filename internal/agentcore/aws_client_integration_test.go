//go:build integration

package agentcore

import (
	"context"
	"os"
	"testing"
	"time"
)

// Integration tests require real AWS credentials and the following env vars:
//
//	AGENTCORE_TEST_REGION   — AWS region (e.g. us-west-2)
//	AGENTCORE_TEST_ROLE_ARN — IAM role ARN for the AgentCore runtime
//
// Run with:
//
//	GOWORK=off go test -tags=integration -v -run TestIntegration ./...

func integrationConfig(t *testing.T) *Config {
	t.Helper()
	region := os.Getenv("AGENTCORE_TEST_REGION")
	roleARN := os.Getenv("AGENTCORE_TEST_ROLE_ARN")
	if region == "" || roleARN == "" {
		t.Skip("AGENTCORE_TEST_REGION and AGENTCORE_TEST_ROLE_ARN must be set")
	}
	return &Config{
		Region:         region,
		RuntimeRoleARN: roleARN,
	}
}

func TestIntegration_CreateAndDeleteRuntime(t *testing.T) {
	cfg := integrationConfig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	client, err := newRealAWSClient(ctx, cfg)
	if err != nil {
		t.Fatalf("failed to create real client: %v", err)
	}

	name := "promptkit-integration-test"
	arn, err := client.CreateRuntime(ctx, name, cfg)
	if err != nil {
		t.Fatalf("CreateRuntime failed: %v", err)
	}
	t.Logf("Created runtime ARN: %s", arn)

	if arn == "" {
		t.Fatal("expected non-empty ARN")
	}

	// Check health.
	status, err := client.CheckResource(ctx, ResourceState{
		Type: "agent_runtime",
		Name: name,
		ARN:  arn,
	})
	if err != nil {
		t.Fatalf("CheckResource failed: %v", err)
	}
	t.Logf("Runtime status: %s", status)
	if status != "healthy" {
		t.Errorf("expected healthy, got %s", status)
	}

	// Clean up.
	err = client.DeleteResource(ctx, ResourceState{
		Type: "agent_runtime",
		Name: name,
		ARN:  arn,
	})
	if err != nil {
		t.Fatalf("DeleteResource failed: %v", err)
	}
	t.Log("Deleted runtime successfully")
}

func TestIntegration_CreateAndDeleteGateway(t *testing.T) {
	cfg := integrationConfig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	client, err := newRealAWSClient(ctx, cfg)
	if err != nil {
		t.Fatalf("failed to create real client: %v", err)
	}

	name := "promptkit-integ-tool"
	arn, err := client.CreateGatewayTool(ctx, name, cfg)
	if err != nil {
		t.Fatalf("CreateGatewayTool failed: %v", err)
	}
	t.Logf("Created gateway tool ARN: %s", arn)

	if arn == "" {
		t.Fatal("expected non-empty ARN")
	}

	// Check health.
	status, err := client.CheckResource(ctx, ResourceState{
		Type: "tool_gateway",
		Name: name,
		ARN:  arn,
	})
	if err != nil {
		t.Fatalf("CheckResource failed: %v", err)
	}
	t.Logf("Gateway status: %s", status)

	// Clean up.
	err = client.DeleteResource(ctx, ResourceState{
		Type: "tool_gateway",
		Name: name,
		ARN:  arn,
	})
	if err != nil {
		t.Fatalf("DeleteResource failed: %v", err)
	}
	t.Log("Deleted gateway successfully")
}
