package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWantsSSE(t *testing.T) {
	tests := []struct {
		accept string
		want   bool
	}{
		{"", false},
		{"application/json", false},
		{"text/event-stream", true},
		{"text/event-stream, application/json", true},
		{"application/json, text/event-stream", true},
	}
	for _, tt := range tests {
		r := httptest.NewRequest(http.MethodPost, "/", nil)
		if tt.accept != "" {
			r.Header.Set("Accept", tt.accept)
		}
		if got := wantsSSE(r); got != tt.want {
			t.Errorf("wantsSSE(%q) = %v, want %v", tt.accept, got, tt.want)
		}
	}
}

func TestBuildA2AStreamRequest_Simple(t *testing.T) {
	data, err := buildA2AStreamRequest("hello", "", nil)
	if err != nil {
		t.Fatalf("buildA2AStreamRequest: %v", err)
	}
	var req map[string]any
	if err := json.Unmarshal(data, &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if req["method"] != "message/stream" {
		t.Errorf("method = %v, want message/stream", req["method"])
	}
	params := req["params"].(map[string]any)
	if _, ok := params["contextId"]; ok {
		t.Error("expected no contextId when sessionID is empty")
	}
}

func TestBuildA2AStreamRequest_WithSessionAndMetadata(t *testing.T) {
	md := map[string]any{"user_id": "u1"}
	data, err := buildA2AStreamRequest("hello", "sess-1", md)
	if err != nil {
		t.Fatalf("buildA2AStreamRequest: %v", err)
	}
	var req map[string]any
	if err := json.Unmarshal(data, &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	params := req["params"].(map[string]any)
	if params["contextId"] != "sess-1" {
		t.Errorf("contextId = %v, want sess-1", params["contextId"])
	}
	message := params["message"].(map[string]any)
	msgMD := message["metadata"].(map[string]any)
	if msgMD["user_id"] != "u1" {
		t.Errorf("metadata[user_id] = %v, want u1", msgMD["user_id"])
	}
}

func TestIsTerminalState(t *testing.T) {
	tests := []struct {
		state string
		want  bool
	}{
		{"completed", true},
		{"failed", true},
		{"canceled", true},
		{"rejected", true},
		{"working", false},
		{"submitted", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := isTerminalState(tt.state); got != tt.want {
			t.Errorf("isTerminalState(%q) = %v, want %v", tt.state, got, tt.want)
		}
	}
}

func TestParseA2ASSEEvent_Status(t *testing.T) {
	b := &httpBridge{log: slog.Default()}
	data := `{"jsonrpc":"2.0","id":"1","result":{"taskId":"t1","contextId":"c1","status":{"state":"working"}}}`
	evt := b.parseA2ASSEEvent(data)
	if evt == nil {
		t.Fatal("expected non-nil event")
	}
	if evt.Type != "status" {
		t.Errorf("Type = %q, want status", evt.Type)
	}
	if evt.State != "working" {
		t.Errorf("State = %q, want working", evt.State)
	}
	if evt.TaskID != "t1" {
		t.Errorf("TaskID = %q, want t1", evt.TaskID)
	}
	if evt.ContextID != "c1" {
		t.Errorf("ContextID = %q, want c1", evt.ContextID)
	}
}

func TestParseA2ASSEEvent_Artifact(t *testing.T) {
	b := &httpBridge{log: slog.Default()}
	data := `{"jsonrpc":"2.0","id":"1","result":{"taskId":"t1","artifact":{"parts":[{"text":"hello"}]}}}`
	evt := b.parseA2ASSEEvent(data)
	if evt == nil {
		t.Fatal("expected non-nil event")
	}
	if evt.Type != "text" {
		t.Errorf("Type = %q, want text", evt.Type)
	}
	if evt.Content != "hello" {
		t.Errorf("Content = %q, want hello", evt.Content)
	}
}

func TestParseA2ASSEEvent_Error(t *testing.T) {
	b := &httpBridge{log: slog.Default()}
	data := `{"jsonrpc":"2.0","id":"1","error":{"message":"model error"}}`
	evt := b.parseA2ASSEEvent(data)
	if evt == nil {
		t.Fatal("expected non-nil event")
	}
	if evt.Type != "error" {
		t.Errorf("Type = %q, want error", evt.Type)
	}
	if evt.Content != "model error" {
		t.Errorf("Content = %q, want %q", evt.Content, "model error")
	}
}

func TestParseA2ASSEEvent_EmptyArtifact(t *testing.T) {
	b := &httpBridge{log: slog.Default()}
	data := `{"jsonrpc":"2.0","id":"1","result":{"taskId":"t1","artifact":{"parts":[]}}}`
	evt := b.parseA2ASSEEvent(data)
	if evt != nil {
		t.Errorf("expected nil for empty artifact, got %+v", evt)
	}
}

func TestParseA2ASSEEvent_InvalidJSON(t *testing.T) {
	b := &httpBridge{log: slog.Default()}
	evt := b.parseA2ASSEEvent("not json")
	if evt != nil {
		t.Errorf("expected nil for invalid JSON, got %+v", evt)
	}
}

func TestWriteSSEEvent(t *testing.T) {
	w := httptest.NewRecorder()
	evt := &sseEvent{Type: "text", Content: "hello"}
	if err := writeSSEEvent(w, w, evt); err != nil {
		t.Fatalf("writeSSEEvent: %v", err)
	}
	body := w.Body.String()
	if !strings.HasPrefix(body, "data: ") {
		t.Errorf("expected SSE data prefix, got %q", body)
	}
	if !strings.HasSuffix(body, "\n\n") {
		t.Errorf("expected double newline suffix, got %q", body)
	}
	// Parse the JSON in the data line.
	jsonStr := strings.TrimPrefix(strings.TrimSuffix(body, "\n\n"), "data: ")
	var got sseEvent
	if err := json.Unmarshal([]byte(jsonStr), &got); err != nil {
		t.Fatalf("parse SSE JSON: %v", err)
	}
	if got.Type != "text" || got.Content != "hello" {
		t.Errorf("got %+v, want type=text content=hello", got)
	}
}

func TestWriteSSEDone(t *testing.T) {
	w := httptest.NewRecorder()
	writeSSEDone(w, w)
	body := w.Body.String()
	if !strings.Contains(body, `"type":"done"`) {
		t.Errorf("expected done event, got %q", body)
	}
}

func TestRelaySSEEvents(t *testing.T) {
	b := &httpBridge{log: slog.Default()}

	// Simulate A2A SSE stream.
	sseData := strings.Join([]string{
		`data: {"jsonrpc":"2.0","id":"1","result":{"taskId":"t1","status":{"state":"working"}}}`,
		``,
		`data: {"jsonrpc":"2.0","id":"1","result":{"taskId":"t1","artifact":{"parts":[{"text":"hello "}]}}}`,
		``,
		`data: {"jsonrpc":"2.0","id":"1","result":{"taskId":"t1","artifact":{"parts":[{"text":"world"}]}}}`,
		``,
		`data: {"jsonrpc":"2.0","id":"1","result":{"taskId":"t1","status":{"state":"completed"}}}`,
		``,
	}, "\n")

	r := httptest.NewRequest(http.MethodPost, "/", nil)
	w := httptest.NewRecorder()

	b.relaySSEEvents(w, r, strings.NewReader(sseData))

	resp := w.Result()
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	ct := resp.Header.Get("Content-Type")
	if ct != sseContentType {
		t.Errorf("Content-Type = %q, want %q", ct, sseContentType)
	}

	// Parse all events.
	lines := strings.Split(string(body), "\n")
	var events []sseEvent
	for _, line := range lines {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		var evt sseEvent
		if err := json.Unmarshal([]byte(data), &evt); err != nil {
			t.Fatalf("parse event: %v (data=%q)", err, data)
		}
		events = append(events, evt)
	}

	// Expect: status(working), text(hello ), text(world), status(completed), done.
	wantTypes := []string{"status", "text", "text", "status", "done"}
	if len(events) != len(wantTypes) {
		t.Fatalf("got %d events, want %d: %+v", len(events), len(wantTypes), events)
	}
	for i, wt := range wantTypes {
		if events[i].Type != wt {
			t.Errorf("event[%d].Type = %q, want %q", i, events[i].Type, wt)
		}
	}
	if events[1].Content != "hello " {
		t.Errorf("event[1].Content = %q, want %q", events[1].Content, "hello ")
	}
}

func TestHandleStreamingInvocation(t *testing.T) {
	// Mock A2A server that returns SSE events.
	a2aMock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", sseContentType)
		w.WriteHeader(http.StatusOK)
		lines := []string{
			`data: {"jsonrpc":"2.0","id":"1","result":{"taskId":"t1","contextId":"c1","status":{"state":"working"}}}`,
			``,
			`data: {"jsonrpc":"2.0","id":"1","result":{"taskId":"t1","artifact":{"parts":[{"text":"streamed"}]}}}`,
			``,
			`data: {"jsonrpc":"2.0","id":"1","result":{"taskId":"t1","status":{"state":"completed"}}}`,
			``,
		}
		for _, l := range lines {
			fmt.Fprintln(w, l)
		}
	}))
	defer a2aMock.Close()

	// Extract port.
	port := extractTestPort(t, a2aMock.URL)

	b := &httpBridge{a2aPort: port, log: slog.Default()}
	req := &invocationRequest{Prompt: "test"}

	r := httptest.NewRequest(http.MethodPost, invocationsPath, nil)
	r.Header.Set("Accept", sseContentType)
	w := httptest.NewRecorder()

	b.handleStreamingInvocation(w, r, req)

	resp := w.Result()
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.Header.Get("Content-Type") != sseContentType {
		t.Errorf("Content-Type = %q, want %q", resp.Header.Get("Content-Type"), sseContentType)
	}

	// Should contain text and done events.
	if !strings.Contains(string(body), `"type":"text"`) {
		t.Error("expected text event in SSE output")
	}
	if !strings.Contains(string(body), `"type":"done"`) {
		t.Error("expected done event in SSE output")
	}
}

// extractTestPort extracts the port number from a test server URL.
func extractTestPort(t *testing.T, url string) int {
	t.Helper()
	parts := strings.Split(url, ":")
	portStr := parts[len(parts)-1]
	port := 0
	for _, c := range portStr {
		if c >= '0' && c <= '9' {
			port = port*10 + int(c-'0')
		}
	}
	if port == 0 {
		t.Fatalf("failed to extract port from %q", url)
	}
	return port
}
