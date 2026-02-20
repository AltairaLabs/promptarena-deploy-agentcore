package agentcore

import (
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockagentcorecontrol/types"
)

// buildTargetConfig returns the SDK TargetConfiguration for a gateway tool.
// It uses the ArenaConfig tool spec (if available) to determine the endpoint.
// Falls back to a placeholder MCP endpoint if no spec or HTTP config is found.
func buildTargetConfig(name string, cfg *Config) *types.TargetConfigurationMemberMcp {
	spec := cfg.ArenaConfig.toolSpecForName(name)
	endpoint := resolveToolEndpoint(name, spec)
	return &types.TargetConfigurationMemberMcp{
		Value: &types.McpTargetConfigurationMemberMcpServer{
			Value: types.McpServerTargetConfiguration{
				Endpoint: aws.String(endpoint),
			},
		},
	}
}

// resolveToolEndpoint returns the endpoint URL for a gateway tool target.
// If the tool spec has an HTTP URL, use that. Otherwise fall back to a
// placeholder.
func resolveToolEndpoint(name string, spec *ArenaToolSpec) string {
	if spec != nil && spec.HTTPConfig != nil && spec.HTTPConfig.URL != "" {
		return spec.HTTPConfig.URL
	}
	return fmt.Sprintf("https://%s.mcp.local", name)
}
