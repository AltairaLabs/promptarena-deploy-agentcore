package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"

	"github.com/AltairaLabs/PromptKit/runtime/tools"
)

// Gateway wire constants, from the AgentCore Gateway MCP contract.
const (
	// gatewayExecutorName is the executor's name, and therefore the tool Mode
	// that routes to it.
	gatewayExecutorName = "gateway"

	// sigV4Service is the SigV4 service name the Gateway signs against.
	sigV4Service = "bedrock-agentcore"

	// mcpProtocolVersion is the stateless revision: no initialize handshake,
	// no session header, one POST per call. Every other revision the Gateway
	// accepts would need a handshake first and a session to carry.
	mcpProtocolVersion = "2026-07-28"

	// mcpPath is where a Gateway serves MCP.
	mcpPath = "/mcp"

	// toolNameSep joins a Gateway target's name to a tool's own.
	toolNameSep = "___"

	// methodToolsCall is the JSON-RPC method for invoking a tool, sent both
	// in the body and in the Mcp-Method header.
	methodToolsCall = "tools/call"

	// keyName is the JSON-RPC params key naming the tool.
	keyName = "name"

	// metaProtocolVersion and metaClientInfo are the _meta keys the stateless
	// revision expects; the header and the _meta value must agree.
	metaProtocolVersion = "io.modelcontextprotocol/protocolVersion"
	metaClientInfo      = "io.modelcontextprotocol/clientInfo"

	gatewayCallTimeout = 60 * time.Second
)

// gatewayExecutor calls tools that live behind an AgentCore Gateway.
//
// It speaks MCP to the Gateway directly rather than going through a general
// MCP client, so AWS's particulars — SigV4 over each request, the Mcp-Method
// and Mcp-Name headers, the target___tool naming — stay inside the AWS-specific
// runtime instead of leaking into a provider-neutral library.
type gatewayExecutor struct {
	endpoint string
	region   string
	// signer is nil when the Gateway authorizes with NONE, which is a valid
	// configuration and needs no credentials at all.
	signer *v4.Signer
	creds  aws.CredentialsProvider
	client *http.Client

	// targets maps a pack tool name to its Gateway target name.
	targets map[string]string
}

// newGatewayExecutor builds an executor for a Gateway endpoint.
//
// authorizer is what the Gateway reports about itself, so the runtime signs
// exactly when the Gateway expects a signature rather than assuming either way.
func newGatewayExecutor(
	endpoint, region, authorizer string,
	creds aws.CredentialsProvider,
	targets map[string]string,
) *gatewayExecutor {
	e := &gatewayExecutor{
		endpoint: gatewayMCPURL(endpoint),
		region:   region,
		targets:  targets,
		client:   &http.Client{Timeout: gatewayCallTimeout},
	}
	if authorizer == "AWS_IAM" || authorizer == "AUTHENTICATE_ONLY" {
		e.signer = v4.NewSigner()
		e.creds = creds
	}
	return e
}

// gatewayMCPURL returns the URL to POST MCP requests to.
//
// The documented endpoint is the gateway host with /mcp; whether the URL the
// control plane hands back already carries that suffix is not consistent
// across AWS's own examples, so add it only when it is missing.
func gatewayMCPURL(endpoint string) string {
	trimmed := strings.TrimRight(endpoint, "/")
	if trimmed == "" || strings.HasSuffix(trimmed, mcpPath) {
		return trimmed
	}
	return trimmed + mcpPath
}

// Name is the executor's name, which is also the tool Mode that selects it.
func (e *gatewayExecutor) Name() string { return gatewayExecutorName }

// Execute calls one tool through the Gateway.
func (e *gatewayExecutor) Execute(
	ctx context.Context, descriptor *tools.ToolDescriptor, args json.RawMessage,
) (json.RawMessage, error) {
	target, ok := e.targets[descriptor.Name]
	if !ok || target == "" {
		// The adapter names every gateway-backed tool when it deploys, so a
		// missing entry means the two disagree about what was deployed.
		return nil, fmt.Errorf(
			"tool %q has no gateway target; it was not deployed as a gateway tool",
			descriptor.Name)
	}

	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}

	qualified := target + toolNameSep + descriptor.Name

	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      "tools-call-" + descriptor.Name,
		"method":  methodToolsCall,
		"params": map[string]any{
			keyName:     qualified,
			"arguments": args,
			"_meta": map[string]any{
				metaProtocolVersion: mcpProtocolVersion,
				metaClientInfo: map[string]any{
					"name":    "promptarena-agentcore",
					"version": version,
				},
			},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("encode tools/call for %q: %w", descriptor.Name, err)
	}

	reply, err := e.post(ctx, body, map[string]string{
		"Mcp-Method": methodToolsCall,
		"Mcp-Name":   qualified,
	})
	if err != nil {
		return nil, err
	}
	return toolResultContent(reply, descriptor.Name)
}

// post sends one signed JSON-RPC request and returns the decoded reply.
func (e *gatewayExecutor) post(
	ctx context.Context, body []byte, headers map[string]string,
) (*jsonRPCReply, error) {
	req, err := http.NewRequestWithContext(
		ctx, http.MethodPost, e.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build gateway request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	// Only application/json: asking for text/event-stream makes a
	// streaming-enabled gateway answer with SSE, which this executor would
	// then have to frame-decode for no benefit — a tool result is one value.
	req.Header.Set("Accept", "application/json")
	req.Header.Set("MCP-Protocol-Version", mcpProtocolVersion)
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	if signErr := e.sign(ctx, req, body); signErr != nil {
		return nil, signErr
	}

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("call gateway: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read gateway response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("gateway returned %d: %s",
			resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	var reply jsonRPCReply
	if err := json.Unmarshal(respBody, &reply); err != nil {
		return nil, fmt.Errorf("decode gateway response: %w", err)
	}
	if reply.Error != nil {
		return nil, fmt.Errorf("gateway error %d: %s",
			reply.Error.Code, reply.Error.Message)
	}
	return &reply, nil
}

// sign applies SigV4 when the Gateway authenticates its callers.
//
// The signature covers a hash of this request's body, so it has to be computed
// per request and cannot be prepared in advance.
func (e *gatewayExecutor) sign(ctx context.Context, req *http.Request, body []byte) error {
	if e.signer == nil {
		return nil
	}
	if e.creds == nil {
		return fmt.Errorf("gateway requires signed requests but no credentials are configured")
	}

	cred, err := e.creds.Retrieve(ctx)
	if err != nil {
		return fmt.Errorf("retrieve credentials for gateway call: %w", err)
	}

	sum := sha256.Sum256(body)
	if err := e.signer.SignHTTP(
		ctx, cred, req, hex.EncodeToString(sum[:]),
		sigV4Service, e.region, time.Now(),
	); err != nil {
		return fmt.Errorf("sign gateway request: %w", err)
	}
	return nil
}

// jsonRPCReply is the subset of a JSON-RPC response this executor reads.
type jsonRPCReply struct {
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// toolResultContent extracts a tool's answer from an MCP tools/call result.
//
// MCP returns content as a list of typed parts. Text parts are concatenated
// and returned as JSON when they parse as JSON, and wrapped otherwise, so a
// tool answering with a bare string still reaches the model as a value.
func toolResultContent(reply *jsonRPCReply, toolName string) (json.RawMessage, error) {
	var result struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(reply.Result, &result); err != nil {
		// An unrecognized shape is still the tool's answer; hand it through
		// rather than failing the turn over a decoding preference.
		return reply.Result, nil
	}

	var text strings.Builder
	for _, part := range result.Content {
		if part.Type == "text" || part.Text != "" {
			text.WriteString(part.Text)
		}
	}

	if result.IsError {
		return nil, fmt.Errorf("tool %q failed: %s", toolName, text.String())
	}

	raw := text.String()
	if raw == "" {
		return reply.Result, nil
	}
	if json.Valid([]byte(raw)) {
		return json.RawMessage(raw), nil
	}
	wrapped, err := json.Marshal(map[string]string{"result": raw})
	if err != nil {
		return nil, fmt.Errorf("wrap tool %q result: %w", toolName, err)
	}
	return wrapped, nil
}
