//go:build integration

package agentcore

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/bedrockagentcore"
)

// TestIntegration_InvokeAgentRuntime invokes a deployed agent runtime and
// verifies we get a non-empty LLM response back.
//
// Required env vars:
//
//	AGENTCORE_TEST_REGION      — AWS region (e.g. us-west-2)
//	AGENTCORE_TEST_RUNTIME_ARN — full ARN of the deployed runtime
//
// Run with:
//
//	GOWORK=off go test -tags=integration -v -run TestIntegration_InvokeAgentRuntime ./internal/agentcore/
func TestIntegration_InvokeAgentRuntime(t *testing.T) {
	region := os.Getenv("AGENTCORE_TEST_REGION")
	runtimeARN := os.Getenv("AGENTCORE_TEST_RUNTIME_ARN")
	if region == "" || runtimeARN == "" {
		t.Skip("AGENTCORE_TEST_REGION and AGENTCORE_TEST_RUNTIME_ARN must be set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
	if err != nil {
		t.Fatalf("failed to load AWS config: %v", err)
	}
	client := bedrockagentcore.NewFromConfig(awsCfg)

	tests := []struct {
		name    string
		payload map[string]interface{}
	}{
		{
			name:    "prompt field",
			payload: map[string]interface{}{"prompt": "Explain machine learning in one sentence."},
		},
		{
			name: "input field (AWS example format)",
			payload: map[string]interface{}{
				"input":   "What is the capital of France?",
				"user_id": "test-user",
				"context": map[string]interface{}{
					"language": "en",
				},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			invokeAndVerify(t, ctx, client, runtimeARN, tc.payload)
		})
	}
}

func invokeAndVerify(
	t *testing.T, ctx context.Context,
	client *bedrockagentcore.Client, runtimeARN string,
	payload map[string]interface{},
) {
	t.Helper()

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("failed to marshal payload: %v", err)
	}

	sessionID := "integ-test-session-" + time.Now().Format("20060102150405.000000000")
	t.Logf("Session ID: %s", sessionID)
	t.Logf("Payload: %s", string(payloadBytes))

	resp, err := client.InvokeAgentRuntime(ctx, &bedrockagentcore.InvokeAgentRuntimeInput{
		AgentRuntimeArn:  &runtimeARN,
		RuntimeSessionId: &sessionID,
		Payload:          payloadBytes,
		ContentType:      aws.String("application/json"),
		Accept:           aws.String("application/json"),
	})
	if err != nil {
		t.Fatalf("InvokeAgentRuntime failed: %v", err)
	}
	defer resp.Response.Close()

	body, err := io.ReadAll(resp.Response)
	if err != nil {
		t.Fatalf("failed to read response body: %v", err)
	}

	t.Logf("Status code: %d", aws.ToInt32(resp.StatusCode))
	t.Logf("Response body: %.500s", string(body))

	if len(body) == 0 {
		t.Fatal("expected non-empty response body")
	}

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("response is not valid JSON: %v\nbody: %s", err, string(body))
	}

	if status, ok := result["status"].(string); ok && status == "error" {
		t.Fatalf("runtime returned error: %v", result["response"])
	}

	content := extractResponseContent(result)
	if content == "" {
		responseStr, _ := json.MarshalIndent(result, "", "  ")
		t.Fatalf("could not extract LLM content from response:\n%s", string(responseStr))
	}

	t.Logf("Got LLM response (%d chars): %.300s", len(content), content)
}

// extractResponseContent tries multiple response shapes to find the LLM text.
func extractResponseContent(result map[string]interface{}) string {
	// Shape 1: {"response": "...", "status": "success"}
	if resp, ok := result["response"].(string); ok && resp != "" {
		return resp
	}

	// Shape 2: A2A JSON-RPC result with artifacts
	if r, ok := result["result"].(map[string]interface{}); ok {
		if artifacts, ok := r["artifacts"].([]interface{}); ok {
			for _, a := range artifacts {
				if art, ok := a.(map[string]interface{}); ok {
					if parts, ok := art["parts"].([]interface{}); ok {
						for _, p := range parts {
							if part, ok := p.(map[string]interface{}); ok {
								if text, ok := part["text"].(string); ok && text != "" {
									return text
								}
							}
						}
					}
				}
			}
		}
	}

	// Shape 3: direct {"content": "..."}
	if content, ok := result["content"].(string); ok && content != "" {
		return content
	}

	return ""
}
