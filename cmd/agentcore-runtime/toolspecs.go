package main

import (
	"encoding/json"
	"fmt"
)

// Tool execution modes this runtime understands. They mirror the adapter's
// constants; the adapter decides which one a tool gets at deploy time.
const (
	toolModeMock    = "mock"
	toolModeLive    = "live"
	toolModeGateway = "gateway"
)

// toolSpec is the executable half of a tool definition.
//
// The compiled pack carries a tool's name, description and parameters and
// nothing else, so how to run one arrives separately through
// PROMPTPACK_TOOL_SPECS.
type toolSpec struct {
	Mode string `json:"mode"`

	MockResult   any    `json:"mock_result,omitempty"`
	MockTemplate string `json:"mock_template,omitempty"`

	URL    string `json:"url,omitempty"`
	Method string `json:"method,omitempty"`

	// GatewayTarget is the name this tool goes by inside the Gateway.
	//
	// Supplied by the adapter rather than derived here. The sanitiser that
	// produces it lives in the adapter, and a second copy in the runtime
	// would be free to drift from the first.
	GatewayTarget string `json:"gateway_target,omitempty"`
}

// parseToolSpecs decodes the injected tool specs. An empty string means the
// pack declares no tools.
func parseToolSpecs(raw string) (map[string]toolSpec, error) {
	if raw == "" {
		return nil, nil
	}
	var specs map[string]toolSpec
	if err := json.Unmarshal([]byte(raw), &specs); err != nil {
		return nil, fmt.Errorf("invalid %s JSON: %w", envToolSpecs, err)
	}
	return specs, nil
}
