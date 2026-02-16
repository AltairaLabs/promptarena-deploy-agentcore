package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/AltairaLabs/PromptKit/runtime/deploy/adaptersdk"
)

// jsonRPCRequest builds a JSON-RPC 2.0 request line.
func jsonRPCRequest(method string, id int, params any) string {
	req := map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
		"id":      id,
	}
	if params != nil {
		req["params"] = params
	}
	b, _ := json.Marshal(req)
	return string(b) + "\n"
}

// jsonRPCResponse represents a JSON-RPC 2.0 response for test assertions.
type jsonRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
	ID json.RawMessage `json:"id"`
}

func callAdapter(t *testing.T, input string) jsonRPCResponse {
	t.Helper()
	provider := NewAgentCoreProvider()

	var out bytes.Buffer
	err := adaptersdk.ServeIO(provider, strings.NewReader(input), &out)
	if err != nil {
		t.Fatalf("ServeIO error: %v", err)
	}

	var resp jsonRPCResponse
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response %q: %v", out.String(), err)
	}
	return resp
}

func TestGetProviderInfo(t *testing.T) {
	resp := callAdapter(t, jsonRPCRequest("get_provider_info", 1, nil))

	if resp.Error != nil {
		t.Fatalf("unexpected error: %s", resp.Error.Message)
	}

	var info struct {
		Name         string   `json:"name"`
		Version      string   `json:"version"`
		Capabilities []string `json:"capabilities"`
		ConfigSchema string   `json:"config_schema"`
	}
	if err := json.Unmarshal(resp.Result, &info); err != nil {
		t.Fatalf("failed to unmarshal result: %v", err)
	}

	if info.Name != "agentcore" {
		t.Errorf("name = %q, want agentcore", info.Name)
	}
	if info.Version == "" {
		t.Error("version is empty")
	}
	if len(info.Capabilities) != 4 {
		t.Errorf("capabilities = %v, want 4 items", info.Capabilities)
	}
	if info.ConfigSchema == "" {
		t.Error("config_schema is empty")
	}
}

func TestValidateConfig_Valid(t *testing.T) {
	params := map[string]string{
		"config": `{"region":"us-west-2","runtime_role_arn":"arn:aws:iam::123456789012:role/test"}`,
	}
	resp := callAdapter(t, jsonRPCRequest("validate_config", 2, params))

	if resp.Error != nil {
		t.Fatalf("unexpected error: %s", resp.Error.Message)
	}

	var result struct {
		Valid  bool     `json:"valid"`
		Errors []string `json:"errors"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("failed to unmarshal result: %v", err)
	}

	if !result.Valid {
		t.Errorf("expected valid=true, got errors: %v", result.Errors)
	}
}

func TestValidateConfig_Invalid(t *testing.T) {
	params := map[string]string{
		"config": `{}`,
	}
	resp := callAdapter(t, jsonRPCRequest("validate_config", 3, params))

	if resp.Error != nil {
		t.Fatalf("unexpected error: %s", resp.Error.Message)
	}

	var result struct {
		Valid  bool     `json:"valid"`
		Errors []string `json:"errors"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("failed to unmarshal result: %v", err)
	}

	if result.Valid {
		t.Error("expected valid=false for empty config")
	}
	if len(result.Errors) < 2 {
		t.Errorf("expected at least 2 errors (region + role), got %d: %v", len(result.Errors), result.Errors)
	}
}

func TestValidateConfig_BadJSON(t *testing.T) {
	params := map[string]string{
		"config": `{not valid json}`,
	}
	resp := callAdapter(t, jsonRPCRequest("validate_config", 4, params))

	if resp.Error != nil {
		t.Fatalf("unexpected error: %s", resp.Error.Message)
	}

	var result struct {
		Valid  bool     `json:"valid"`
		Errors []string `json:"errors"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("failed to unmarshal result: %v", err)
	}

	if result.Valid {
		t.Error("expected valid=false for bad JSON")
	}
}

func TestPlanStubReturnsError(t *testing.T) {
	params := map[string]string{
		"pack_json":     `{}`,
		"deploy_config": `{}`,
	}
	resp := callAdapter(t, jsonRPCRequest("plan", 5, params))

	if resp.Error == nil {
		t.Fatal("expected error for stub method")
	}
	if !strings.Contains(resp.Error.Message, "not yet implemented") {
		t.Errorf("error message = %q, want 'not yet implemented'", resp.Error.Message)
	}
}

func TestApplyStubReturnsError(t *testing.T) {
	params := map[string]string{
		"pack_json":     `{}`,
		"deploy_config": `{}`,
	}
	resp := callAdapter(t, jsonRPCRequest("apply", 6, params))

	if resp.Error == nil {
		t.Fatal("expected error for stub method")
	}
	if !strings.Contains(resp.Error.Message, "not yet implemented") {
		t.Errorf("error message = %q, want 'not yet implemented'", resp.Error.Message)
	}
}

func TestDestroyStubReturnsError(t *testing.T) {
	params := map[string]string{
		"deploy_config": `{}`,
	}
	resp := callAdapter(t, jsonRPCRequest("destroy", 7, params))

	if resp.Error == nil {
		t.Fatal("expected error for stub method")
	}
	if !strings.Contains(resp.Error.Message, "not yet implemented") {
		t.Errorf("error message = %q, want 'not yet implemented'", resp.Error.Message)
	}
}

func TestStatusStubReturnsError(t *testing.T) {
	params := map[string]string{
		"deploy_config": `{}`,
	}
	resp := callAdapter(t, jsonRPCRequest("status", 8, params))

	if resp.Error == nil {
		t.Fatal("expected error for stub method")
	}
	if !strings.Contains(resp.Error.Message, "not yet implemented") {
		t.Errorf("error message = %q, want 'not yet implemented'", resp.Error.Message)
	}
}

func TestUnknownMethodReturnsError(t *testing.T) {
	resp := callAdapter(t, jsonRPCRequest("nonexistent", 9, nil))

	if resp.Error == nil {
		t.Fatal("expected error for unknown method")
	}
	if resp.Error.Code != -32601 {
		t.Errorf("error code = %d, want -32601", resp.Error.Code)
	}
}
