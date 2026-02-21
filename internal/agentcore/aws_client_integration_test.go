//go:build integration

package agentcore

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/apigateway"
	apigatewayTypes "github.com/aws/aws-sdk-go-v2/service/apigateway/types"
	"github.com/aws/aws-sdk-go-v2/service/bedrockagentcorecontrol"
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

	name := "promptkit_integ_tool"
	arn, err := client.CreateGatewayTool(ctx, name, cfg)
	if err != nil {
		t.Fatalf("CreateGatewayTool failed: %v", err)
	}
	t.Logf("Created gateway tool ARN: %s", arn)

	if arn == "" {
		t.Fatal("expected non-empty ARN")
	}

	// Wait for gateway to be ready before checking health.
	waitForGatewayReady(t, ctx, client, arn)

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

// waitForGatewayReady polls the gateway until it reaches READY status,
// giving targets time to finish provisioning.
func waitForGatewayReady(t *testing.T, ctx context.Context, client *realAWSClient, arn string) {
	t.Helper()
	id := extractResourceID(arn, "gateway")
	for i := 0; i < 30; i++ {
		out, err := client.client.GetGateway(ctx, &bedrockagentcorecontrol.GetGatewayInput{
			GatewayIdentifier: &id,
		})
		if err != nil {
			t.Logf("GetGateway poll: %v", err)
			time.Sleep(10 * time.Second)
			continue
		}
		t.Logf("Gateway status: %s", out.Status)
		if out.Status == "READY" {
			return
		}
		time.Sleep(10 * time.Second)
	}
	t.Log("Warning: gateway did not reach READY within poll window")
}

// TestIntegration_GatewayTargetTypes creates a gateway with multiple target
// types and verifies they are created in AWS. Logs details for manual
// inspection.
//
// Target types tested:
//   - MCP server (no credential provider needed)
//   - API Gateway (uses GATEWAY_IAM_ROLE credential provider)
//
// OpenAPI and Smithy targets require OAUTH or API_KEY credential providers
// which need pre-provisioned infrastructure, so they are not tested here.
func TestIntegration_GatewayTargetTypes(t *testing.T) {
	cfg := integrationConfig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	// Create a PetStore REST API in API Gateway for the test.
	restAPIID := createTestRestAPI(t, ctx, cfg.Region)
	t.Cleanup(func() { deleteTestRestAPI(t, cfg.Region, restAPIID) })

	client, err := newRealAWSClient(ctx, cfg)
	if err != nil {
		t.Fatalf("failed to create real client: %v", err)
	}

	// Configure ArenaConfig with different target types.
	cfg.ArenaConfig = &ArenaConfig{
		ToolSpecs: map[string]*ArenaToolSpec{
			"mcptool": {
				HTTPConfig: &ArenaHTTPConfig{URL: "https://example.com/mcp"},
			},
			"apigwtool": {
				APIGateway: &ArenaAPIGatewayConfig{
					RestAPIID: restAPIID,
					Stage:     "test",
					Filters: []ArenaAPIGatewayFilter{
						{Path: "/pets", Methods: []string{"GET"}},
					},
				},
				Credential: &ArenaCredentialConfig{Type: "GATEWAY_IAM_ROLE"},
			},
		},
	}

	toolNames := []string{"mcptool", "apigwtool"}
	var gwARN string

	for _, name := range toolNames {
		arn, err := client.CreateGatewayTool(ctx, name, cfg)
		if err != nil {
			t.Fatalf("CreateGatewayTool(%s) failed: %v", name, err)
		}
		t.Logf("Created target %s, gateway ARN: %s", name, arn)
		gwARN = arn
	}

	// Wait for gateway to stabilize so targets finish provisioning.
	waitForGatewayReady(t, ctx, client, gwARN)

	// List targets for inspection.
	gwID := extractResourceID(gwARN, "gateway")
	targets, err := client.client.ListGatewayTargets(ctx, &bedrockagentcorecontrol.ListGatewayTargetsInput{
		GatewayIdentifier: &gwID,
	})
	if err != nil {
		t.Fatalf("ListGatewayTargets: %v", err)
	}

	t.Logf("Gateway %s has %d targets:", gwID, len(targets.Items))
	for _, tgt := range targets.Items {
		detail, _ := client.client.GetGatewayTarget(ctx, &bedrockagentcorecontrol.GetGatewayTargetInput{
			GatewayIdentifier: &gwID,
			TargetId:          tgt.TargetId,
		})
		credInfo := ""
		if detail != nil && len(detail.CredentialProviderConfigurations) > 0 {
			credInfo = " cred=" + string(detail.CredentialProviderConfigurations[0].CredentialProviderType)
		}
		t.Logf("  Target: %-20s  ID: %-15s  Status: %-10s%s",
			*tgt.Name, *tgt.TargetId, tgt.Status, credInfo)
	}

	if len(targets.Items) != len(toolNames) {
		t.Errorf("expected %d targets, got %d", len(toolNames), len(targets.Items))
	}

	// Clean up.
	t.Log("Cleaning up gateway...")
	err = client.DeleteResource(ctx, ResourceState{
		Type: "tool_gateway",
		Name: toolNames[0],
		ARN:  gwARN,
	})
	if err != nil {
		t.Fatalf("DeleteResource (gateway) failed: %v", err)
	}
	t.Log("Gateway and all targets deleted successfully")
}

// createTestRestAPI creates a minimal PetStore REST API in API Gateway
// with a /pets GET method and a "test" stage, returning the REST API ID.
func createTestRestAPI(t *testing.T, ctx context.Context, region string) string {
	t.Helper()

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
	if err != nil {
		t.Fatalf("load AWS config: %v", err)
	}
	apigw := apigateway.NewFromConfig(awsCfg)

	// Create REST API.
	api, err := apigw.CreateRestApi(ctx, &apigateway.CreateRestApiInput{
		Name:        aws.String("promptkit-integ-petstore"),
		Description: aws.String("Integration test PetStore API"),
	})
	if err != nil {
		t.Fatalf("CreateRestApi: %v", err)
	}
	apiID := *api.Id
	t.Logf("Created REST API: %s", apiID)

	// Get root resource ID.
	resources, err := apigw.GetResources(ctx, &apigateway.GetResourcesInput{
		RestApiId: &apiID,
	})
	if err != nil {
		t.Fatalf("GetResources: %v", err)
	}
	var rootID string
	for _, r := range resources.Items {
		if aws.ToString(r.Path) == "/" {
			rootID = *r.Id
			break
		}
	}

	// Create /pets resource.
	pets, err := apigw.CreateResource(ctx, &apigateway.CreateResourceInput{
		RestApiId: &apiID,
		ParentId:  &rootID,
		PathPart:  aws.String("pets"),
	})
	if err != nil {
		t.Fatalf("CreateResource /pets: %v", err)
	}

	// Create GET method on /pets.
	_, err = apigw.PutMethod(ctx, &apigateway.PutMethodInput{
		RestApiId:         &apiID,
		ResourceId:        pets.Id,
		HttpMethod:        aws.String("GET"),
		AuthorizationType: aws.String("NONE"),
		OperationName:     aws.String("listPets"),
	})
	if err != nil {
		t.Fatalf("PutMethod GET /pets: %v", err)
	}

	// Add mock integration so we can create a deployment.
	_, err = apigw.PutIntegration(ctx, &apigateway.PutIntegrationInput{
		RestApiId:  &apiID,
		ResourceId: pets.Id,
		HttpMethod: aws.String("GET"),
		Type:       apigatewayTypes.IntegrationTypeMock,
		RequestTemplates: map[string]string{
			"application/json": `{"statusCode": 200}`,
		},
	})
	if err != nil {
		t.Fatalf("PutIntegration: %v", err)
	}

	// Create deployment + stage.
	_, err = apigw.CreateDeployment(ctx, &apigateway.CreateDeploymentInput{
		RestApiId: &apiID,
		StageName: aws.String("test"),
	})
	if err != nil {
		t.Fatalf("CreateDeployment: %v", err)
	}
	t.Logf("REST API %s deployed to 'test' stage", apiID)

	return apiID
}

// deleteTestRestAPI removes the test REST API.
func deleteTestRestAPI(t *testing.T, region, apiID string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
	if err != nil {
		t.Logf("Warning: could not load AWS config for cleanup: %v", err)
		return
	}
	apigw := apigateway.NewFromConfig(awsCfg)
	_, err = apigw.DeleteRestApi(ctx, &apigateway.DeleteRestApiInput{
		RestApiId: &apiID,
	})
	if err != nil {
		t.Logf("Warning: DeleteRestApi(%s): %v", apiID, err)
	} else {
		t.Logf("Deleted REST API %s", apiID)
	}
}
