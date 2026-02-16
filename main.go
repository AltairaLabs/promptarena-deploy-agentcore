// Package main implements the promptarena-deploy-agentcore binary,
// an AWS Bedrock AgentCore deploy adapter for PromptKit.
package main

import (
	"fmt"
	"os"

	"github.com/AltairaLabs/PromptKit/runtime/deploy/adaptersdk"
)

func main() {
	provider := NewAgentCoreProvider()
	if err := adaptersdk.Serve(provider); err != nil {
		fmt.Fprintf(os.Stderr, "agentcore: %v\n", err)
		os.Exit(1)
	}
}
