package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// --- invocationRequest tests ---

func TestInvocationRequest_TextPreference(t *testing.T) {
	tests := []struct {
		name   string
		json   string
		expect string
	}{
		{"prompt only", `{"prompt":"hello"}`, "hello"},
		{"input only", `{"input":"world"}`, "world"},
		{"prompt wins", `{"prompt":"hello","input":"world"}`, "hello"},
		{"both empty", `{}`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var req invocationRequest
			if err := json.Unmarshal([]byte(tt.json), &req); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if got := req.text(); got != tt.expect {
				t.Errorf("text() = %q, want %q", got, tt.expect)
			}
		})
	}
}

func TestInvocationRequest_ExtraFields(t *testing.T) {
	body := `{"prompt":"hello","user_id":"u123","context":{"key":"val"}}`
	var req invocationRequest
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if req.text() != "hello" {
		t.Errorf("text() = %q, want %q", req.text(), "hello")
	}
	if req.Extra == nil {
		t.Fatal("expected Extra to be populated")
	}
	if req.Extra["user_id"] != "u123" {
		t.Errorf("Extra[user_id] = %v, want u123", req.Extra["user_id"])
	}
}

func TestInvocationRequest_ExplicitMetadata(t *testing.T) {
	body := `{"prompt":"hello","metadata":{"session":"s1"},"extra_field":"val"}`
	var req invocationRequest
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	md := req.allMetadata()
	if md == nil {
		t.Fatal("expected allMetadata to be non-nil")
	}
	if md["session"] != "s1" {
		t.Errorf("metadata[session] = %v, want s1", md["session"])
	}
	payload, ok := md["payload"].(map[string]any)
	if !ok {
		t.Fatal("expected payload key in merged metadata")
	}
	if payload["extra_field"] != "val" {
		t.Errorf("payload[extra_field] = %v, want val", payload["extra_field"])
	}
}

func TestInvocationRequest_NoMetadata(t *testing.T) {
	body := `{"prompt":"hello"}`
	var req invocationRequest
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if md := req.allMetadata(); md != nil {
		t.Errorf("expected nil metadata, got %v", md)
	}
}

// --- buildA2ARequest tests ---

func TestBuildA2ARequest_Simple(t *testing.T) {
	data, err := buildA2ARequest("hello", "", nil)
	if err != nil {
		t.Fatalf("buildA2ARequest: %v", err)
	}
	var req map[string]any
	if err := json.Unmarshal(data, &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if req["method"] != "message/send" {
		t.Errorf("method = %v, want message/send", req["method"])
	}
	params := req["params"].(map[string]any)
	if _, ok := params["contextId"]; ok {
		t.Error("expected no contextId when sessionID is empty")
	}
	message := params["message"].(map[string]any)
	if _, ok := message["metadata"]; ok {
		t.Error("expected no metadata when none provided")
	}
}

func TestBuildA2ARequest_WithSession(t *testing.T) {
	data, err := buildA2ARequest("hello", "session-abc", nil)
	if err != nil {
		t.Fatalf("buildA2ARequest: %v", err)
	}
	var req map[string]any
	if err := json.Unmarshal(data, &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	params := req["params"].(map[string]any)
	if params["contextId"] != "session-abc" {
		t.Errorf("contextId = %v, want session-abc", params["contextId"])
	}
}

func TestBuildA2ARequest_WithMetadata(t *testing.T) {
	md := map[string]any{"user_id": "u123"}
	data, err := buildA2ARequest("hello", "", md)
	if err != nil {
		t.Fatalf("buildA2ARequest: %v", err)
	}
	var req map[string]any
	if err := json.Unmarshal(data, &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	params := req["params"].(map[string]any)
	message := params["message"].(map[string]any)
	msgMD, ok := message["metadata"].(map[string]any)
	if !ok {
		t.Fatal("expected metadata on message")
	}
	if msgMD["user_id"] != "u123" {
		t.Errorf("metadata[user_id] = %v, want u123", msgMD["user_id"])
	}
}

// --- a2aResponse extraction tests ---

func TestExtractArtifactText(t *testing.T) {
	resp := &a2aResponse{}
	resp.Result.Artifacts = []struct {
		Parts []struct {
			Text string `json:"text"`
		} `json:"parts"`
		Metadata map[string]any `json:"metadata,omitempty"`
	}{
		{Parts: []struct {
			Text string `json:"text"`
		}{
			{Text: "hello "},
			{Text: "world"},
		}},
	}
	if got := extractArtifactText(resp); got != "hello world" {
		t.Errorf("extractArtifactText = %q, want %q", got, "hello world")
	}
}

func TestExtractFailedMessage_Default(t *testing.T) {
	resp := &a2aResponse{}
	if got := extractFailedMessage(resp); got != "agent task failed" {
		t.Errorf("extractFailedMessage = %q, want %q", got, "agent task failed")
	}
}

func TestExtractFailedMessage_WithMessage(t *testing.T) {
	errText := "something went wrong"
	resp := &a2aResponse{}
	resp.Result.Status.Message = &struct {
		Parts []struct {
			Text *string `json:"text"`
		} `json:"parts"`
	}{
		Parts: []struct {
			Text *string `json:"text"`
		}{
			{Text: &errText},
		},
	}
	if got := extractFailedMessage(resp); got != errText {
		t.Errorf("extractFailedMessage = %q, want %q", got, errText)
	}
}

func TestExtractUsage(t *testing.T) {
	t.Run("no metadata", func(t *testing.T) {
		resp := &a2aResponse{}
		if got := extractUsage(resp); got != nil {
			t.Errorf("expected nil, got %+v", got)
		}
	})

	t.Run("with usage", func(t *testing.T) {
		resp := &a2aResponse{}
		resp.Result.Metadata = map[string]any{
			"usage": map[string]any{
				"input_tokens":  float64(100),
				"output_tokens": float64(50),
			},
		}
		got := extractUsage(resp)
		if got == nil {
			t.Fatal("expected non-nil usage")
		}
		if got.InputTokens != 100 {
			t.Errorf("InputTokens = %d, want 100", got.InputTokens)
		}
		if got.OutputTokens != 50 {
			t.Errorf("OutputTokens = %d, want 50", got.OutputTokens)
		}
	})

	t.Run("zero usage", func(t *testing.T) {
		resp := &a2aResponse{}
		resp.Result.Metadata = map[string]any{
			"usage": map[string]any{},
		}
		if got := extractUsage(resp); got != nil {
			t.Errorf("expected nil for zero usage, got %+v", got)
		}
	})
}

// --- writeA2AResponse tests ---

func TestWriteA2AResponse_Success(t *testing.T) {
	a2aJSON := `{
		"result": {
			"id": "task-123",
			"contextId": "ctx-456",
			"status": {"state": "completed"},
			"artifacts": [{"parts": [{"text": "The answer"}]}],
			"metadata": {"usage": {"input_tokens": 10, "output_tokens": 5}}
		}
	}`
	w := httptest.NewRecorder()
	b := &httpBridge{log: slog.Default()}
	b.writeA2AResponse(w, []byte(a2aJSON))

	resp := w.Result()
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	var invResp invocationResponse
	if err := json.Unmarshal(body, &invResp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if invResp.Response != "The answer" {
		t.Errorf("Response = %q, want %q", invResp.Response, "The answer")
	}
	if invResp.Status != "success" {
		t.Errorf("Status = %q, want %q", invResp.Status, "success")
	}
	if invResp.TaskID != "task-123" {
		t.Errorf("TaskID = %q, want %q", invResp.TaskID, "task-123")
	}
	if invResp.ContextID != "ctx-456" {
		t.Errorf("ContextID = %q, want %q", invResp.ContextID, "ctx-456")
	}
	if invResp.Usage == nil {
		t.Fatal("expected Usage to be non-nil")
	}
	if invResp.Usage.InputTokens != 10 {
		t.Errorf("Usage.InputTokens = %d, want 10", invResp.Usage.InputTokens)
	}
}

func TestWriteA2AResponse_Error(t *testing.T) {
	a2aJSON := `{"error": {"message": "model error"}}`
	w := httptest.NewRecorder()
	b := &httpBridge{log: slog.Default()}
	b.writeA2AResponse(w, []byte(a2aJSON))

	resp := w.Result()
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	var invResp invocationResponse
	if err := json.Unmarshal(body, &invResp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if invResp.Status != "error" {
		t.Errorf("Status = %q, want %q", invResp.Status, "error")
	}
	if invResp.Response != "model error" {
		t.Errorf("Response = %q, want %q", invResp.Response, "model error")
	}
}

func TestWriteA2AResponse_Failed(t *testing.T) {
	a2aJSON := `{"result": {"status": {"state": "failed"}}}`
	w := httptest.NewRecorder()
	b := &httpBridge{log: slog.Default()}
	b.writeA2AResponse(w, []byte(a2aJSON))

	resp := w.Result()
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	var invResp invocationResponse
	if err := json.Unmarshal(body, &invResp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if invResp.Status != "error" {
		t.Errorf("Status = %q, want %q", invResp.Status, "error")
	}
}

func TestWriteA2AResponse_NoMetadata(t *testing.T) {
	a2aJSON := `{
		"result": {
			"status": {"state": "completed"},
			"artifacts": [{"parts": [{"text": "hi"}]}]
		}
	}`
	w := httptest.NewRecorder()
	b := &httpBridge{log: slog.Default()}
	b.writeA2AResponse(w, []byte(a2aJSON))

	resp := w.Result()
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	var invResp invocationResponse
	if err := json.Unmarshal(body, &invResp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if invResp.TaskID != "" {
		t.Errorf("TaskID should be empty, got %q", invResp.TaskID)
	}
	if invResp.Usage != nil {
		t.Errorf("Usage should be nil, got %+v", invResp.Usage)
	}

	// Verify omitempty works: these fields should not be in raw JSON
	if strings.Contains(string(body), "task_id") {
		t.Error("task_id should not appear in JSON when empty")
	}
	if strings.Contains(string(body), "usage") {
		t.Error("usage should not appear in JSON when nil")
	}
}

// --- handleInvocation integration tests ---

func TestHandleInvocation_SessionHeader(t *testing.T) {
	// Set up a mock A2A server that echoes back the received request.
	var receivedBody []byte
	a2aMock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"result": {
				"id": "task-1",
				"contextId": "session-xyz",
				"status": {"state": "completed"},
				"artifacts": [{"parts": [{"text": "ok"}]}]
			}
		}`))
	}))
	defer a2aMock.Close()

	// Extract port from mock server URL.
	parts := strings.Split(a2aMock.URL, ":")
	port := 0
	for _, p := range parts {
		if n := 0; len(p) > 0 {
			_, _ = json.Number(p).Int64()
			// Simple port extraction
			for _, c := range p {
				if c >= '0' && c <= '9' {
					n = n*10 + int(c-'0')
				}
			}
			if n > 0 {
				port = n
			}
		}
	}

	b := &httpBridge{
		a2aPort: port,
		log:     slog.Default(),
	}

	reqBody := `{"prompt":"test","user_id":"u1"}`
	r := httptest.NewRequest(http.MethodPost, invocationsPath, strings.NewReader(reqBody))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set(sessionHeader, "session-xyz")
	w := httptest.NewRecorder()

	b.handleInvocation(w, r)

	// Verify the A2A request included contextId.
	var a2aReq map[string]any
	if err := json.Unmarshal(receivedBody, &a2aReq); err != nil {
		t.Fatalf("unmarshal a2a request: %v", err)
	}
	params, _ := a2aReq["params"].(map[string]any)
	if params["contextId"] != "session-xyz" {
		t.Errorf("contextId = %v, want session-xyz", params["contextId"])
	}

	// Verify metadata was forwarded.
	message, _ := params["message"].(map[string]any)
	md, _ := message["metadata"].(map[string]any)
	payload, _ := md["payload"].(map[string]any)
	if payload["user_id"] != "u1" {
		t.Errorf("metadata.payload.user_id = %v, want u1", payload["user_id"])
	}
}

func TestHandleInvocation_MissingPrompt(t *testing.T) {
	b := &httpBridge{log: slog.Default()}
	r := httptest.NewRequest(http.MethodPost, invocationsPath, strings.NewReader(`{}`))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	b.handleInvocation(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestHandleInvocation_InvalidJSON(t *testing.T) {
	b := &httpBridge{log: slog.Default()}
	r := httptest.NewRequest(http.MethodPost, invocationsPath, strings.NewReader(`not json`))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	b.handleInvocation(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}
