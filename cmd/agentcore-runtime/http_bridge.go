package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"
)

// httpBridgePort is the port AgentCore uses for the HTTP protocol contract.
const httpBridgePort = 8080

// invocationsPath is the HTTP protocol endpoint for agent invocations.
const invocationsPath = "/invocations"

// invocationRequest is the payload format sent by invoke_agent_runtime.
// Supports both "prompt" (our convention) and "input" (AWS example convention).
type invocationRequest struct {
	Prompt string `json:"prompt"`
	Input  string `json:"input"`
}

// text returns the user's message, preferring "prompt" over "input".
func (r *invocationRequest) text() string {
	if r.Prompt != "" {
		return r.Prompt
	}
	return r.Input
}

// invocationResponse is the HTTP protocol response format.
type invocationResponse struct {
	Response string `json:"response"`
	Status   string `json:"status"`
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
		Status struct {
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
		} `json:"artifacts"`
	} `json:"result"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// buildA2ARequest creates a blocking A2A message/send JSON-RPC request.
func buildA2ARequest(text string) ([]byte, error) {
	a2aReq := map[string]any{
		"jsonrpc": "2.0",
		"id":      "http-bridge-1",
		"method":  "message/send",
		"params": map[string]any{
			"message": map[string]any{
				"role": "user",
				"parts": []map[string]any{
					{"kind": "text", "text": text},
				},
				"messageId": fmt.Sprintf("http-%d", time.Now().UnixNano()),
			},
			"configuration": map[string]any{
				"blocking": true,
			},
		},
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

	a2aBody, err := buildA2ARequest(req.text())
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
		Response: extractArtifactText(&result),
		Status:   "success",
	})
}
