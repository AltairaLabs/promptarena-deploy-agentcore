package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"strings"
	"time"
)

// httpBridgePort is the port AgentCore uses for the HTTP protocol contract.
const httpBridgePort = 8080

// invocationsPath is the HTTP protocol endpoint for agent invocations.
const invocationsPath = "/invocations"

// sessionHeader is the AgentCore header that carries the session ID.
const sessionHeader = "X-Amzn-Bedrock-AgentCore-Runtime-Session-Id"

// invocationRequest is the payload format sent by invoke_agent_runtime.
// Supports both "prompt" (our convention) and "input" (AWS example convention).
// Extra fields are captured as metadata and forwarded to the A2A server.
type invocationRequest struct {
	Prompt   string         `json:"prompt"`
	Input    string         `json:"input"`
	Metadata map[string]any `json:"metadata,omitempty"`
	Extra    map[string]any `json:"-"` // all other top-level fields
}

// UnmarshalJSON implements custom unmarshalling to capture extra fields
// beyond prompt, input, and metadata.
func (r *invocationRequest) UnmarshalJSON(data []byte) error {
	// First unmarshal known fields via an alias to avoid recursion.
	type alias invocationRequest
	var a alias
	if err := json.Unmarshal(data, &a); err != nil {
		return err
	}
	*r = invocationRequest(a)

	// Then unmarshal everything into a generic map for extra fields.
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	delete(raw, "prompt")
	delete(raw, "input")
	delete(raw, "metadata")
	if len(raw) > 0 {
		r.Extra = raw
	}
	return nil
}

// text returns the user's message, preferring "prompt" over "input".
func (r *invocationRequest) text() string {
	if r.Prompt != "" {
		return r.Prompt
	}
	return r.Input
}

// allMetadata merges explicit metadata with extra top-level fields.
// Extra fields are namespaced under "payload" to avoid collisions.
func (r *invocationRequest) allMetadata() map[string]any {
	if len(r.Metadata) == 0 && len(r.Extra) == 0 {
		return nil
	}
	merged := make(map[string]any, len(r.Metadata)+1)
	maps.Copy(merged, r.Metadata)
	if len(r.Extra) > 0 {
		merged["payload"] = r.Extra
	}
	return merged
}

// invocationResponse is the HTTP protocol response format.
// Extra fields use omitempty for backward compatibility.
type invocationResponse struct {
	Response  string         `json:"response"`
	Status    string         `json:"status"`
	TaskID    string         `json:"task_id,omitempty"`
	ContextID string         `json:"context_id,omitempty"`
	Usage     *usageInfo     `json:"usage,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

// usageInfo holds token usage from the A2A response.
type usageInfo struct {
	InputTokens  int `json:"input_tokens,omitempty"`
	OutputTokens int `json:"output_tokens,omitempty"`
}

// httpBridge serves the AgentCore HTTP protocol contract on port 8080,
// forwarding invocations to the A2A server on port 9000.
type httpBridge struct {
	a2aPort int
	log     *slog.Logger
	srv     *http.Server
}

// startHTTPBridge starts the HTTP bridge server on port 8080.
// It forwards /invocations requests to the A2A server's /a2a endpoint.
func startHTTPBridge(log *slog.Logger, healthH *healthHandler, a2aPort int) (*httpBridge, error) {
	b := &httpBridge{
		a2aPort: a2aPort,
		log:     log,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST "+invocationsPath, b.handleInvocation)
	mux.Handle("/ping", healthH)
	mux.HandleFunc("/", b.handleUnknown)

	addr := fmt.Sprintf(":%d", httpBridgePort)
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("listen %s: %w", addr, err)
	}

	b.srv = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: defaultReadHeaderTmout,
	}

	go func() {
		if err := b.srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Error("http bridge serve error", "error", err)
		}
	}()

	log.Info("http bridge listening", "addr", addr)
	return b, nil
}

// shutdown gracefully shuts down the HTTP bridge server.
func (b *httpBridge) shutdown(ctx context.Context) error {
	if b == nil {
		return nil
	}
	return b.srv.Shutdown(ctx)
}

// writeInvocationError writes a JSON error response for an invocation.
func writeInvocationError(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusInternalServerError)
	_ = json.NewEncoder(w).Encode(invocationResponse{
		Response: msg,
		Status:   "error",
	})
}

// handleUnknown logs any unmatched requests for debugging.
func (b *httpBridge) handleUnknown(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	b.log.Warn("unmatched request on http bridge",
		"method", r.Method, "path", r.URL.Path,
		"content-type", r.Header.Get("Content-Type"),
		"body_size", len(body), "body", string(body))
	http.Error(w, "not found", http.StatusNotFound)
}

// a2aResponse is the parsed JSON-RPC response from the A2A server.
type a2aResponse struct {
	Result struct {
		ID        string `json:"id"`
		ContextID string `json:"contextId"`
		Status    struct {
			State   string `json:"state"`
			Message *struct {
				Parts []struct {
					Text *string `json:"text"`
				} `json:"parts"`
			} `json:"message"`
		} `json:"status"`
		Artifacts []struct {
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
			Metadata map[string]any `json:"metadata,omitempty"`
		} `json:"artifacts"`
		Metadata map[string]any `json:"metadata,omitempty"`
	} `json:"result"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// buildA2ARequest creates a blocking A2A message/send JSON-RPC request.
// sessionID maps to contextId for multi-turn conversation continuity.
// metadata is forwarded as A2A message-level metadata.
func buildA2ARequest(text, sessionID string, metadata map[string]any) ([]byte, error) {
	message := map[string]any{
		"role": "user",
		"parts": []map[string]any{
			{"kind": "text", "text": text},
		},
		"messageId": fmt.Sprintf("http-%d", time.Now().UnixNano()),
	}
	if len(metadata) > 0 {
		message["metadata"] = metadata
	}

	params := map[string]any{
		"message": message,
		"configuration": map[string]any{
			"blocking": true,
		},
	}
	if sessionID != "" {
		params["contextId"] = sessionID
	}

	a2aReq := map[string]any{
		"jsonrpc": "2.0",
		"id":      "http-bridge-1",
		"method":  "message/send",
		"params":  params,
	}
	return json.Marshal(a2aReq)
}

// extractFailedMessage extracts an error message from a failed A2A task status.
func extractFailedMessage(result *a2aResponse) string {
	errMsg := "agent task failed"
	if m := result.Result.Status.Message; m != nil {
		for _, p := range m.Parts {
			if p.Text != nil {
				errMsg = *p.Text
				break
			}
		}
	}
	return errMsg
}

// extractArtifactText concatenates all text parts from A2A response artifacts.
func extractArtifactText(result *a2aResponse) string {
	var sb strings.Builder
	for _, art := range result.Result.Artifacts {
		for _, part := range art.Parts {
			if part.Text != "" {
				sb.WriteString(part.Text)
			}
		}
	}
	return sb.String()
}

// extractUsage extracts token usage from A2A response metadata.
func extractUsage(result *a2aResponse) *usageInfo {
	md := result.Result.Metadata
	if md == nil {
		return nil
	}
	usage, ok := md["usage"]
	if !ok {
		return nil
	}
	usageMap, ok := usage.(map[string]any)
	if !ok {
		return nil
	}
	info := &usageInfo{}
	if v, ok := usageMap["input_tokens"].(float64); ok {
		info.InputTokens = int(v)
	}
	if v, ok := usageMap["output_tokens"].(float64); ok {
		info.OutputTokens = int(v)
	}
	if info.InputTokens == 0 && info.OutputTokens == 0 {
		return nil
	}
	return info
}

// handleInvocation converts an HTTP /invocations request to an A2A message/send
// call and returns the response.
func (b *httpBridge) handleInvocation(w http.ResponseWriter, r *http.Request) {
	b.log.Info("invocation received", "method", r.Method, "path", r.URL.Path,
		"content-type", r.Header.Get("Content-Type"))

	body, err := io.ReadAll(r.Body)
	if err != nil {
		b.log.Error("failed to read body", "error", err)
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}

	b.log.Info("invocation body", "size", len(body), "body", string(body))

	var req invocationRequest
	if unmarshalErr := json.Unmarshal(body, &req); unmarshalErr != nil {
		b.log.Error("invalid JSON in invocation", "error", unmarshalErr, "body", string(body))
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	if req.text() == "" {
		b.log.Warn("invocation missing prompt/input field", "body", string(body))
		http.Error(w, "prompt or input is required", http.StatusBadRequest)
		return
	}

	sessionID := r.Header.Get(sessionHeader)
	a2aBody, err := buildA2ARequest(req.text(), sessionID, req.allMetadata())
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	respBody, err := b.forwardToA2A(a2aBody)
	if err != nil {
		http.Error(w, "agent unavailable", http.StatusBadGateway)
		return
	}

	b.writeA2AResponse(w, respBody)
}

// forwardToA2A sends a JSON-RPC request to the A2A server and returns the body.
func (b *httpBridge) forwardToA2A(a2aBody []byte) ([]byte, error) {
	a2aURL := fmt.Sprintf("http://127.0.0.1:%d/a2a", b.a2aPort)
	b.log.Info("forwarding to a2a", "url", a2aURL, "body_size", len(a2aBody))

	resp, err := http.Post(a2aURL, "application/json", //nolint:noctx,gosec // internal loopback
		bytes.NewReader(a2aBody))
	if err != nil {
		b.log.Error("a2a forward failed", "error", err)
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	b.log.Info("a2a response", "status", resp.StatusCode, "body", string(respBody))
	return respBody, nil
}

// writeA2AResponse parses the A2A JSON-RPC response and writes the invocation response.
func (b *httpBridge) writeA2AResponse(w http.ResponseWriter, respBody []byte) {
	var result a2aResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(respBody)
		return
	}

	if result.Error != nil {
		writeInvocationError(w, result.Error.Message)
		return
	}

	if result.Result.Status.State == "failed" {
		writeInvocationError(w, extractFailedMessage(&result))
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(invocationResponse{
		Response:  extractArtifactText(&result),
		Status:    "success",
		TaskID:    result.Result.ID,
		ContextID: result.Result.ContextID,
		Usage:     extractUsage(&result),
	})
}
