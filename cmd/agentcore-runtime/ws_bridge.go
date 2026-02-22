package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
)

// wsReadLimit is the maximum message size for WebSocket reads.
const wsReadLimit = 1 << 20 // 1 MiB

// wsBufferSize is the read/write buffer size for WebSocket connections.
const wsBufferSize = 4096

// upgrader configures the WebSocket upgrade with permissive origin checks
// (the bridge is only reachable from within the AgentCore VPC).
var upgrader = websocket.Upgrader{
	ReadBufferSize:  wsBufferSize,
	WriteBufferSize: wsBufferSize,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

// wsRequest is the WebSocket message payload from the client.
type wsRequest struct {
	Prompt   string         `json:"prompt"`
	Input    string         `json:"input"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

// text returns the user's message, preferring "prompt" over "input".
func (r *wsRequest) text() string {
	if r.Prompt != "" {
		return r.Prompt
	}
	return r.Input
}

// wsResponse is the WebSocket message payload sent to the client.
type wsResponse struct {
	Type      string     `json:"type"`
	Content   string     `json:"content,omitempty"`
	State     string     `json:"state,omitempty"`
	TaskID    string     `json:"task_id,omitempty"`
	ContextID string     `json:"context_id,omitempty"`
	Usage     *usageInfo `json:"usage,omitempty"`
}

// handleWebSocket upgrades the connection and processes messages.
// Each message is forwarded to the A2A server as a blocking message/send.
func (b *httpBridge) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		b.log.Error("websocket upgrade failed", "error", err)
		return
	}
	defer func() { _ = conn.Close() }()

	conn.SetReadLimit(wsReadLimit)

	b.log.Info("websocket connection established")

	for {
		_, msg, readErr := conn.ReadMessage()
		if readErr != nil {
			if websocket.IsUnexpectedCloseError(readErr,
				websocket.CloseNormalClosure,
				websocket.CloseGoingAway) {
				b.log.Error("websocket read error", "error", readErr)
			}
			return
		}

		b.processWSMessage(conn, msg)
	}
}

// processWSMessage handles a single WebSocket message by forwarding it
// to the A2A server and writing the response back.
func (b *httpBridge) processWSMessage(conn *websocket.Conn, msg []byte) {
	var req wsRequest
	if err := json.Unmarshal(msg, &req); err != nil {
		b.writeWSError(conn, "invalid JSON")
		return
	}

	if req.text() == "" {
		b.writeWSError(conn, "prompt or input is required")
		return
	}

	a2aBody, err := buildWSA2ARequest(req.text(), req.Metadata)
	if err != nil {
		b.writeWSError(conn, "internal error")
		return
	}

	respBody, err := b.forwardToA2A(a2aBody)
	if err != nil {
		b.writeWSError(conn, "agent unavailable")
		return
	}

	b.writeWSA2AResponse(conn, respBody)
}

// buildWSA2ARequest creates a blocking A2A message/send for WebSocket messages.
func buildWSA2ARequest(text string, metadata map[string]any) ([]byte, error) {
	message := map[string]any{
		"role": "user",
		"parts": []map[string]any{
			{"kind": "text", "text": text},
		},
		"messageId": fmt.Sprintf("ws-%d", time.Now().UnixNano()),
	}
	if len(metadata) > 0 {
		message["metadata"] = metadata
	}

	a2aReq := map[string]any{
		"jsonrpc": "2.0",
		"id":      "ws-bridge-1",
		"method":  "message/send",
		"params": map[string]any{
			"message": message,
			"configuration": map[string]any{
				"blocking": true,
			},
		},
	}
	return json.Marshal(a2aReq)
}

// writeWSA2AResponse parses the A2A JSON-RPC response and writes a
// wsResponse message to the WebSocket connection.
func (b *httpBridge) writeWSA2AResponse(conn *websocket.Conn, body []byte) {
	var result a2aResponse
	if err := json.Unmarshal(body, &result); err != nil {
		b.writeWSError(conn, "invalid response from agent")
		return
	}

	if result.Error != nil {
		b.writeWSError(conn, result.Error.Message)
		return
	}

	if result.Result.Status.State == stateFailed {
		b.writeWSJSON(conn, wsResponse{
			Type:    "error",
			Content: extractFailedMessage(&result),
		})
		return
	}

	b.writeWSJSON(conn, wsResponse{
		Type:      "text",
		Content:   extractArtifactText(&result),
		TaskID:    result.Result.ID,
		ContextID: result.Result.ContextID,
		Usage:     extractUsage(&result),
	})

	b.writeWSJSON(conn, wsResponse{Type: "done"})
}

// writeWSError writes an error message to the WebSocket connection.
func (b *httpBridge) writeWSError(conn *websocket.Conn, msg string) {
	b.writeWSJSON(conn, wsResponse{Type: "error", Content: msg})
}

// writeWSJSON writes a JSON message to the WebSocket connection.
func (b *httpBridge) writeWSJSON(conn *websocket.Conn, v any) {
	if err := conn.WriteJSON(v); err != nil {
		b.log.Error("websocket write error", "error", err)
	}
}
