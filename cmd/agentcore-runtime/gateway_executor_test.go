package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"

	"github.com/AltairaLabs/PromptKit/runtime/prompt"
	"github.com/AltairaLabs/PromptKit/runtime/tools"
)

func testCreds() aws.CredentialsProvider {
	return credentials.NewStaticCredentialsProvider("AKIAEXAMPLE", "secret", "")
}

// TestGatewayMCPURL covers the suffix AWS's own examples disagree about.
func TestGatewayMCPURL(t *testing.T) {
	for _, tt := range []struct{ in, want string }{
		{"https://gw.example.com", "https://gw.example.com/mcp"},
		{"https://gw.example.com/", "https://gw.example.com/mcp"},
		{"https://gw.example.com/mcp", "https://gw.example.com/mcp"},
		{"https://gw.example.com/mcp/", "https://gw.example.com/mcp"},
		{"", ""},
	} {
		if got := gatewayMCPURL(tt.in); got != tt.want {
			t.Errorf("gatewayMCPURL(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// TestGatewayExecutor_CallShape pins what the Gateway is actually sent.
//
// The target and tool names join with three underscores, the protocol version
// in the header has to match the one in _meta, and the tool name is repeated
// in Mcp-Name. Each is a documented requirement rather than a preference.
func TestGatewayExecutor_CallShape(t *testing.T) {
	var gotBody map[string]any
	var gotHeaders http.Header

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		if r.URL.Path != mcpPath {
			t.Errorf("posted to %q, want %q", r.URL.Path, mcpPath)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":"1","result":{"content":[{"type":"text","text":"{\"status\":\"shipped\"}"}]}}`))
	}))
	defer srv.Close()

	exec := newGatewayExecutor(srv.URL, "us-west-2", "AWS_IAM", testCreds(),
		map[string]string{"lookup_order": "ordertarget"})

	out, err := exec.Execute(context.Background(),
		&tools.ToolDescriptor{Name: "lookup_order"},
		json.RawMessage(`{"order_id":"A-1"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if string(out) != `{"status":"shipped"}` {
		t.Errorf("result = %s, want the tool's own JSON", out)
	}

	params, _ := gotBody["params"].(map[string]any)
	if got := params["name"]; got != "ordertarget___lookup_order" {
		t.Errorf("tool name = %v, want ordertarget___lookup_order", got)
	}

	meta, _ := params["_meta"].(map[string]any)
	if meta[metaProtocolVersion] != mcpProtocolVersion {
		t.Errorf("_meta protocol = %v, want %s", meta[metaProtocolVersion], mcpProtocolVersion)
	}
	if h := gotHeaders.Get("MCP-Protocol-Version"); h != mcpProtocolVersion {
		t.Errorf("header protocol = %q, want %s", h, mcpProtocolVersion)
	}
	if h := gotHeaders.Get("Mcp-Name"); h != "ordertarget___lookup_order" {
		t.Errorf("Mcp-Name = %q", h)
	}
	if h := gotHeaders.Get("Mcp-Method"); h != "tools/call" {
		t.Errorf("Mcp-Method = %q", h)
	}
}

// TestGatewayExecutor_SignsWhenTheGatewayAsks covers both authorizer shapes.
//
// A gateway that authorizes with NONE takes unsigned requests, and signing one
// anyway would need credentials the runtime may not have. The signature is
// applied on what the gateway reports, not on an assumption.
func TestGatewayExecutor_SignsWhenTheGatewayAsks(t *testing.T) {
	for _, tt := range []struct {
		authorizer string
		wantSigned bool
	}{
		{"AWS_IAM", true},
		{"AUTHENTICATE_ONLY", true},
		{"NONE", false},
		{"CUSTOM_JWT", false},
	} {
		t.Run(tt.authorizer, func(t *testing.T) {
			var auth string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				auth = r.Header.Get("Authorization")
				_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":"1","result":{"content":[]}}`))
			}))
			defer srv.Close()

			exec := newGatewayExecutor(srv.URL, "us-west-2", tt.authorizer, testCreds(),
				map[string]string{"t": "target"})
			if _, err := exec.Execute(context.Background(),
				&tools.ToolDescriptor{Name: "t"}, nil); err != nil {
				t.Fatalf("Execute: %v", err)
			}

			signed := strings.HasPrefix(auth, "AWS4-HMAC-SHA256")
			if signed != tt.wantSigned {
				t.Errorf("signed = %v, want %v (Authorization=%q)", signed, tt.wantSigned, auth)
			}
			if tt.wantSigned && !strings.Contains(auth, sigV4Service) {
				t.Errorf("signature does not name service %q: %s", sigV4Service, auth)
			}
		})
	}
}

// TestGatewayExecutor_ToolErrorIsAnError keeps a failed tool from reading as a
// successful one whose answer happens to describe a failure.
func TestGatewayExecutor_ToolErrorIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(
			`{"jsonrpc":"2.0","id":"1","result":{"isError":true,"content":[{"type":"text","text":"no such order"}]}}`))
	}))
	defer srv.Close()

	exec := newGatewayExecutor(srv.URL, "us-west-2", "NONE", nil,
		map[string]string{"t": "target"})
	_, err := exec.Execute(context.Background(), &tools.ToolDescriptor{Name: "t"}, nil)
	if err == nil {
		t.Fatal("expected an error for isError:true")
	}
	if !strings.Contains(err.Error(), "no such order") {
		t.Errorf("error should carry the tool's message, got %v", err)
	}
}

// TestGatewayExecutor_UnknownToolNamesItself covers plan and runtime
// disagreeing about what was deployed.
func TestGatewayExecutor_UnknownToolNamesItself(t *testing.T) {
	exec := newGatewayExecutor("https://gw", "us-west-2", "NONE", nil, nil)
	_, err := exec.Execute(context.Background(),
		&tools.ToolDescriptor{Name: "missing"}, nil)
	if err == nil || !strings.Contains(err.Error(), "missing") {
		t.Errorf("error should name the tool, got %v", err)
	}
}

// TestBuildToolRegistry_CoversEveryPackTool is the guard against the silent
// failure this design introduces.
//
// Supplying a registry detaches the pack's own tool repository, so a tool this
// misses is a tool the model never sees — no error, no log from the SDK, just
// an agent that quietly cannot do something.
func TestBuildToolRegistry_CoversEveryPackTool(t *testing.T) {
	pack := &prompt.Pack{
		Tools: map[string]*prompt.PackTool{
			"gw_tool":   {Name: "gw_tool", Description: "via gateway"},
			"mock_tool": {Name: "mock_tool", Description: "mocked"},
			"http_tool": {Name: "http_tool", Description: "http"},
		},
	}
	specs := map[string]toolSpec{
		"gw_tool":   {Mode: toolModeGateway, GatewayTarget: "target"},
		"mock_tool": {Mode: toolModeMock, MockResult: map[string]any{"ok": true}},
		"http_tool": {Mode: toolModeLive, URL: "https://api.example.com/x"},
	}

	reg := buildToolRegistry(pack, specs, "us-west-2", testCreds(),
		"https://gw.example.com", "AWS_IAM", slog.Default())
	if reg == nil {
		t.Fatal("expected a registry")
	}

	for name := range pack.Tools {
		if reg.Get(name) == nil {
			t.Errorf("tool %q is not in the registry; the model would never see it", name)
		}
	}
}

// TestBuildToolRegistry_NoToolsKeepsThePackRegistry checks the case that must
// not change: a pack without tools should get the SDK's own registry, built
// from the pack, rather than an empty one from here.
func TestBuildToolRegistry_NoToolsKeepsThePackRegistry(t *testing.T) {
	if reg := buildToolRegistry(&prompt.Pack{}, nil, "us-west-2", nil, "", "", slog.Default()); reg != nil {
		t.Error("a pack with no tools should not get a replacement registry")
	}
}

// TestDescriptorFor_RejectsUnusableSpecs covers the specs that cannot produce a
// working tool, so they are reported at startup rather than at call time.
func TestDescriptorFor_RejectsUnusableSpecs(t *testing.T) {
	packTool := &prompt.PackTool{Name: "t", Description: "d"}
	for _, tt := range []struct {
		name string
		spec toolSpec
	}{
		{"gateway without a target", toolSpec{Mode: toolModeGateway}},
		{"live without a url", toolSpec{Mode: toolModeLive}},
		{"mock without data", toolSpec{Mode: toolModeMock}},
		{"unknown mode", toolSpec{Mode: "telepathy"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := descriptorFor("t", packTool, &tt.spec); err == nil {
				t.Error("expected an error")
			}
		})
	}
}
