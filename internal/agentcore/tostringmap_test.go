package agentcore

import (
	"encoding/json"
	"testing"

	"github.com/AltairaLabs/PromptKit/runtime/packspec"
)

// TestToStringMap covers each shape a tool schema arrives in. The typed case is
// the one that matters: PackTool.Parameters is a *packspec.ToolParameters as of
// PromptKit v1.9.0, it satisfies the any parameter, and before the JSON
// round-trip fallback it fell through every case and returned nil — leaving
// gateway tools registered with no properties at all.
func TestToStringMap(t *testing.T) {
	typed := &packspec.ToolParameters{
		Type:       "object",
		Properties: map[string]map[string]any{"query": {"type": "string"}},
		Required:   []string{"query"},
	}

	tests := []struct {
		name      string
		in        any
		wantType  string
		wantProps bool
		wantNil   bool
	}{
		{name: "plain map", in: map[string]any{"type": "object"}, wantType: "object"},
		{name: "raw message", in: json.RawMessage(`{"type":"object"}`), wantType: "object"},
		{name: "byte slice", in: []byte(`{"type":"object"}`), wantType: "object"},
		{name: "generated type", in: typed, wantType: "object", wantProps: true},
		{name: "nil", in: nil, wantNil: true},
		{name: "malformed raw", in: json.RawMessage(`{"type":`), wantNil: true},
		{name: "unmarshalable", in: make(chan int), wantNil: true},
		{name: "not an object", in: json.RawMessage(`["a"]`), wantNil: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := toStringMap(tt.in)
			if tt.wantNil {
				if got != nil {
					t.Fatalf("want nil, got %v", got)
				}
				return
			}
			if got["type"] != tt.wantType {
				t.Errorf("type = %v, want %q", got["type"], tt.wantType)
			}
			if tt.wantProps {
				props, ok := got["properties"].(map[string]any)
				if !ok || len(props) == 0 {
					t.Errorf("properties did not survive the conversion: %v", got["properties"])
				}
			}
		})
	}
}
