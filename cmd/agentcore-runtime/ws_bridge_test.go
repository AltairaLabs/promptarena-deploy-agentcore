package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
)

func TestWSRequest_TextPreference(t *testing.T) {
	tests := []struct {
		name   string
		req    wsRequest
		expect string
	}{
		{"prompt only", wsRequest{Prompt: "hello"}, "hello"},
		{"input only", wsRequest{Input: "world"}, "world"},
		{"prompt wins", wsRequest{Prompt: "hello", Input: "world"}, "hello"},
		{"both empty", wsRequest{}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.req.text(); got != tt.expect {
				t.Errorf("text() = %q, want %q", got, tt.expect)
			}
		})
	}
}

func TestBuildWSA2ARequest(t *testing.T) {
	data, err := buildWSA2ARequest("hello", nil)
	if err != nil {
		t.Fatalf("buildWSA2ARequest: %v", err)
	}
	var req map[string]any
	if err := json.Unmarshal(data, &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if req["method"] != "message/send" {
		t.Errorf("method = %v, want message/send", req["method"])
	}
	if req["id"] != "ws-bridge-1" {
		t.Errorf("id = %v, want ws-bridge-1", req["id"])
	}
}

func TestBuildWSA2ARequest_WithMetadata(t *testing.T) {
	md := map[string]any{"key": "val"}
	data, err := buildWSA2ARequest("hi", md)
	if err != nil {
		t.Fatalf("buildWSA2ARequest: %v", err)
	}
	var req map[string]any
	if err := json.Unmarshal(data, &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	params := req["params"].(map[string]any)
	msg := params["message"].(map[string]any)
	msgMD := msg["metadata"].(map[string]any)
	if msgMD["key"] != "val" {
		t.Errorf("metadata[key] = %v, want val", msgMD["key"])
	}
}

func TestWSBridge_Integration(t *testing.T) {
	// Mock A2A server.
	a2aMock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		_ = json.Unmarshal(body, &req)

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"result": {
				"id": "task-ws",
				"contextId": "ctx-ws",
				"status": {"state": "completed"},
				"artifacts": [{"parts": [{"text": "ws response"}]}]
			}
		}`))
	}))
	defer a2aMock.Close()

	a2aPort := extractTestPort(t, a2aMock.URL)

	b := &httpBridge{a2aPort: a2aPort, log: slog.Default()}

	// Create an HTTP server with the WebSocket handler.
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", b.handleWebSocket)
	wsSrv := httptest.NewServer(mux)
	defer wsSrv.Close()

	// Connect via WebSocket.
	wsURL := "ws" + strings.TrimPrefix(wsSrv.URL, "http") + "/ws"
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("ws dial: %v", err)
	}
	defer func() {
		_ = conn.Close()
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
	}()

	// Send a message.
	msg := `{"prompt":"hello from ws"}`
	if err := conn.WriteMessage(websocket.TextMessage, []byte(msg)); err != nil {
		t.Fatalf("ws write: %v", err)
	}

	// Read the text response.
	_, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("ws read: %v", err)
	}
	var wsResp wsResponse
	if err := json.Unmarshal(data, &wsResp); err != nil {
		t.Fatalf("unmarshal ws response: %v", err)
	}
	if wsResp.Type != "text" {
		t.Errorf("Type = %q, want text", wsResp.Type)
	}
	if wsResp.Content != "ws response" {
		t.Errorf("Content = %q, want %q", wsResp.Content, "ws response")
	}
	if wsResp.TaskID != "task-ws" {
		t.Errorf("TaskID = %q, want task-ws", wsResp.TaskID)
	}
	if wsResp.ContextID != "ctx-ws" {
		t.Errorf("ContextID = %q, want ctx-ws", wsResp.ContextID)
	}

	// Read the done message.
	_, data, err = conn.ReadMessage()
	if err != nil {
		t.Fatalf("ws read done: %v", err)
	}
	var doneResp wsResponse
	if err := json.Unmarshal(data, &doneResp); err != nil {
		t.Fatalf("unmarshal done: %v", err)
	}
	if doneResp.Type != "done" {
		t.Errorf("done Type = %q, want done", doneResp.Type)
	}
}

func TestWSBridge_InvalidJSON(t *testing.T) {
	b := &httpBridge{a2aPort: 1, log: slog.Default()}

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", b.handleWebSocket)
	wsSrv := httptest.NewServer(mux)
	defer wsSrv.Close()

	wsURL := "ws" + strings.TrimPrefix(wsSrv.URL, "http") + "/ws"
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("ws dial: %v", err)
	}
	defer func() {
		_ = conn.Close()
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
	}()

	// Send invalid JSON.
	if writeErr := conn.WriteMessage(websocket.TextMessage, []byte("not json")); writeErr != nil {
		t.Fatalf("ws write: %v", writeErr)
	}

	_, data, readErr := conn.ReadMessage()
	if readErr != nil {
		t.Fatalf("ws read: %v", readErr)
	}
	var wsResp wsResponse
	if unmarshalErr := json.Unmarshal(data, &wsResp); unmarshalErr != nil {
		t.Fatalf("unmarshal: %v", unmarshalErr)
	}
	if wsResp.Type != "error" {
		t.Errorf("Type = %q, want error", wsResp.Type)
	}
	if wsResp.Content != "invalid JSON" {
		t.Errorf("Content = %q, want %q", wsResp.Content, "invalid JSON")
	}
}

func TestWSBridge_MissingPrompt(t *testing.T) {
	b := &httpBridge{a2aPort: 1, log: slog.Default()}

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", b.handleWebSocket)
	wsSrv := httptest.NewServer(mux)
	defer wsSrv.Close()

	wsURL := "ws" + strings.TrimPrefix(wsSrv.URL, "http") + "/ws"
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("ws dial: %v", err)
	}
	defer func() {
		_ = conn.Close()
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
	}()

	if writeErr := conn.WriteMessage(websocket.TextMessage, []byte(`{}`)); writeErr != nil {
		t.Fatalf("ws write: %v", writeErr)
	}

	_, data, readErr := conn.ReadMessage()
	if readErr != nil {
		t.Fatalf("ws read: %v", readErr)
	}
	var wsResp wsResponse
	if unmarshalErr := json.Unmarshal(data, &wsResp); unmarshalErr != nil {
		t.Fatalf("unmarshal: %v", unmarshalErr)
	}
	if wsResp.Type != "error" {
		t.Errorf("Type = %q, want error", wsResp.Type)
	}
}

func TestWSBridge_A2AError(t *testing.T) {
	// Mock A2A server that returns a JSON-RPC error.
	a2aMock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"error":{"message":"model failed"}}`))
	}))
	defer a2aMock.Close()

	a2aPort := extractTestPort(t, a2aMock.URL)
	b := &httpBridge{a2aPort: a2aPort, log: slog.Default()}

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", b.handleWebSocket)
	wsSrv := httptest.NewServer(mux)
	defer wsSrv.Close()

	wsURL := "ws" + strings.TrimPrefix(wsSrv.URL, "http") + "/ws"
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("ws dial: %v", err)
	}
	defer func() {
		_ = conn.Close()
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
	}()

	if writeErr := conn.WriteMessage(websocket.TextMessage, []byte(`{"prompt":"test"}`)); writeErr != nil {
		t.Fatalf("ws write: %v", writeErr)
	}

	_, data, readErr := conn.ReadMessage()
	if readErr != nil {
		t.Fatalf("ws read: %v", readErr)
	}
	var wsResp wsResponse
	if unmarshalErr := json.Unmarshal(data, &wsResp); unmarshalErr != nil {
		t.Fatalf("unmarshal: %v", unmarshalErr)
	}
	if wsResp.Type != "error" {
		t.Errorf("Type = %q, want error", wsResp.Type)
	}
	if wsResp.Content != "model failed" {
		t.Errorf("Content = %q, want %q", wsResp.Content, "model failed")
	}
}
