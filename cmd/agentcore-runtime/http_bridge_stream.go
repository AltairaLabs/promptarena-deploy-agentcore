package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// sseContentType is the MIME type for Server-Sent Events.
const sseContentType = "text/event-stream"

// acceptHeader is the HTTP header used for content negotiation.
const acceptHeader = "Accept"

// sseEvent is the format written to the client for each SSE chunk.
type sseEvent struct {
	Type      string `json:"type"`
	Content   string `json:"content,omitempty"`
	State     string `json:"state,omitempty"`
	TaskID    string `json:"task_id,omitempty"`
	ContextID string `json:"context_id,omitempty"`
}

// wantsSSE returns true if the client accepts text/event-stream.
func wantsSSE(r *http.Request) bool {
	accept := r.Header.Get(acceptHeader)
	return strings.Contains(accept, sseContentType)
}

// buildA2AStreamRequest creates a streaming A2A message/stream JSON-RPC request.
func buildA2AStreamRequest(text, sessionID string, metadata map[string]any) ([]byte, error) {
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
	}
	if sessionID != "" {
		params["contextId"] = sessionID
	}

	a2aReq := map[string]any{
		"jsonrpc": "2.0",
		"id":      "http-bridge-stream-1",
		"method":  "message/stream",
		"params":  params,
	}
	return json.Marshal(a2aReq)
}

// handleStreamingInvocation sends a message/stream request to the A2A server
// and relays the SSE events to the HTTP client.
func (b *httpBridge) handleStreamingInvocation(
	w http.ResponseWriter, r *http.Request, req *invocationRequest,
) {
	sessionID := r.Header.Get(sessionHeader)
	a2aBody, err := buildA2AStreamRequest(req.text(), sessionID, req.allMetadata())
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	a2aURL := fmt.Sprintf("http://127.0.0.1:%d/a2a", b.a2aPort)
	b.log.Info("forwarding stream to a2a", "url", a2aURL)

	a2aResp, err := http.Post(a2aURL, "application/json", //nolint:noctx,gosec // internal loopback
		bytes.NewReader(a2aBody))
	if err != nil {
		b.log.Error("a2a stream forward failed", "error", err)
		http.Error(w, "agent unavailable", http.StatusBadGateway)
		return
	}
	defer func() { _ = a2aResp.Body.Close() }()

	b.relaySSEEvents(w, r, a2aResp.Body)
}

// relaySSEEvents reads A2A SSE events and writes simplified SSE events to the client.
func (b *httpBridge) relaySSEEvents(w http.ResponseWriter, r *http.Request, body io.Reader) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", sseContentType)
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	scanner := bufio.NewScanner(body)
	for scanner.Scan() {
		line := scanner.Text()

		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")

		evt := b.parseA2ASSEEvent(data)
		if evt == nil {
			continue
		}

		if err := writeSSEEvent(w, flusher, evt); err != nil {
			b.log.Error("sse write failed", "error", err)
			return
		}

		// Stop on terminal states.
		if evt.Type == "status" && isTerminalState(evt.State) {
			writeSSEDone(w, flusher)
			return
		}

		// Check for client disconnect.
		if r.Context().Err() != nil {
			b.log.Info("client disconnected during stream")
			return
		}
	}

	// Stream ended without a terminal event — send done.
	writeSSEDone(w, flusher)
}

// a2aSSEPayload is a partial parse of the JSON-RPC response wrapping A2A events.
type a2aSSEPayload struct {
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// a2aStreamEvent is a partial parse of the A2A event discriminated by field presence.
type a2aStreamEvent struct {
	TaskID    string           `json:"taskId"`
	ContextID string           `json:"contextId"`
	Status    *json.RawMessage `json:"status"`
	Artifact  *json.RawMessage `json:"artifact"`
}

// a2aStatusPayload extracts the state from a status event.
type a2aStatusPayload struct {
	State string `json:"state"`
}

// a2aArtifactPayload extracts text from an artifact event.
type a2aArtifactPayload struct {
	Parts []struct {
		Text *string `json:"text"`
	} `json:"parts"`
}

// parseA2ASSEEvent converts an A2A SSE data payload to a simplified sseEvent.
func (b *httpBridge) parseA2ASSEEvent(data string) *sseEvent {
	var payload a2aSSEPayload
	if err := json.Unmarshal([]byte(data), &payload); err != nil {
		b.log.Warn("unparseable SSE data", "data", data, "error", err)
		return nil
	}

	if payload.Error != nil {
		return &sseEvent{Type: "error", Content: payload.Error.Message}
	}

	var evt a2aStreamEvent
	if err := json.Unmarshal(payload.Result, &evt); err != nil {
		return nil
	}

	if evt.Status != nil {
		return b.parseStatusEvent(&evt)
	}
	if evt.Artifact != nil {
		return b.parseArtifactEvent(&evt)
	}
	return nil
}

// parseStatusEvent converts an A2A status event to an sseEvent.
func (b *httpBridge) parseStatusEvent(evt *a2aStreamEvent) *sseEvent {
	var status a2aStatusPayload
	if err := json.Unmarshal(*evt.Status, &status); err != nil {
		return nil
	}
	return &sseEvent{
		Type:      "status",
		State:     status.State,
		TaskID:    evt.TaskID,
		ContextID: evt.ContextID,
	}
}

// parseArtifactEvent converts an A2A artifact event to an sseEvent.
func (b *httpBridge) parseArtifactEvent(evt *a2aStreamEvent) *sseEvent {
	var artifact a2aArtifactPayload
	if err := json.Unmarshal(*evt.Artifact, &artifact); err != nil {
		return nil
	}
	var text string
	for _, p := range artifact.Parts {
		if p.Text != nil {
			text += *p.Text
		}
	}
	if text == "" {
		return nil
	}
	return &sseEvent{
		Type:      "text",
		Content:   text,
		TaskID:    evt.TaskID,
		ContextID: evt.ContextID,
	}
}

// writeSSEEvent writes a single SSE event to the response writer.
func writeSSEEvent(w http.ResponseWriter, flusher http.Flusher, evt *sseEvent) error {
	data, err := json.Marshal(evt)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "data: %s\n\n", data)
	if err != nil {
		return err
	}
	flusher.Flush()
	return nil
}

// writeSSEDone writes the terminal SSE done event.
func writeSSEDone(w http.ResponseWriter, flusher http.Flusher) {
	_, _ = fmt.Fprintf(w, "data: %s\n\n", `{"type":"done"}`)
	flusher.Flush()
}

// isTerminalState returns true for A2A task states that indicate completion.
func isTerminalState(state string) bool {
	switch state {
	case "completed", stateFailed, "canceled", "rejected":
		return true
	}
	return false
}
