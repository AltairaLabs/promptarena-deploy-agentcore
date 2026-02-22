package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// ---------------------------------------------------------------------------
// Shared test infrastructure
// ---------------------------------------------------------------------------

// mockA2AServer simulates an A2A server that handles both message/send
// (blocking) and message/stream (SSE) JSON-RPC methods. Tests can
// configure the response behaviour per-request via hooks.
type mockA2AServer struct {
	srv *httptest.Server

	// onSend is called for message/send. If nil, a default success
	// response with the request text echoed back is returned.
	onSend func(params map[string]any) (int, string)

	// onStream is called for message/stream. If nil, a default SSE
	// sequence (working → artifact → completed) is produced.
	onStream func(w http.ResponseWriter, params map[string]any)

	// mu protects the counters.
	mu          sync.Mutex
	sendCount   int
	streamCount int

	// lastSendParams stores the params from the most recent message/send.
	lastSendParams atomic.Value // map[string]any
}

// newMockA2AServer creates and starts a mock A2A server.
func newMockA2AServer(t *testing.T) *mockA2AServer {
	t.Helper()
	m := &mockA2AServer{}
	m.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.handleA2A(t, w, r)
	}))
	t.Cleanup(m.srv.Close)
	return m
}

func (m *mockA2AServer) port(t *testing.T) int {
	t.Helper()
	return extractTestPort(t, m.srv.URL)
}

func (m *mockA2AServer) handleA2A(t *testing.T, w http.ResponseWriter, r *http.Request) {
	t.Helper()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		t.Errorf("mock a2a: read body: %v", err)
		http.Error(w, "read error", http.StatusBadRequest)
		return
	}

	var rpcReq struct {
		Method string         `json:"method"`
		Params map[string]any `json:"params"`
		ID     string         `json:"id"`
	}
	if err := json.Unmarshal(body, &rpcReq); err != nil {
		t.Errorf("mock a2a: unmarshal: %v", err)
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}

	switch rpcReq.Method {
	case "message/send":
		m.mu.Lock()
		m.sendCount++
		m.mu.Unlock()
		m.lastSendParams.Store(rpcReq.Params)
		m.handleSend(w, rpcReq.Params)
	case "message/stream":
		m.mu.Lock()
		m.streamCount++
		m.mu.Unlock()
		m.handleStream(w, rpcReq.Params)
	default:
		http.Error(w, "unknown method", http.StatusBadRequest)
	}
}

func (m *mockA2AServer) handleSend(w http.ResponseWriter, params map[string]any) {
	if m.onSend != nil {
		code, body := m.onSend(params)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_, _ = w.Write([]byte(body))
		return
	}
	// Default: echo the input text back.
	text := extractTextFromParams(params)
	contextID := ""
	if cid, ok := params["contextId"].(string); ok {
		contextID = cid
	}
	resp := fmt.Sprintf(`{
		"jsonrpc": "2.0",
		"id": "1",
		"result": {
			"id": "task-001",
			"contextId": %q,
			"status": {"state": "completed"},
			"artifacts": [{"parts": [{"text": %q}]}],
			"metadata": {
				"usage": {"input_tokens": 10, "output_tokens": 20}
			}
		}
	}`, contextID, "echo: "+text)
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(resp))
}

func (m *mockA2AServer) handleStream(w http.ResponseWriter, params map[string]any) {
	if m.onStream != nil {
		m.onStream(w, params)
		return
	}
	// Default: working → 2 artifact chunks → completed.
	text := extractTextFromParams(params)
	contextID := ""
	if cid, ok := params["contextId"].(string); ok {
		contextID = cid
	}
	events := []string{
		fmt.Sprintf(`{"jsonrpc":"2.0","id":"1","result":{"taskId":"task-s1","contextId":%q,"status":{"state":"working"}}}`, contextID),
		fmt.Sprintf(`{"jsonrpc":"2.0","id":"1","result":{"taskId":"task-s1","contextId":%q,"artifact":{"parts":[{"text":"echo: "}]}}}`, contextID),
		fmt.Sprintf(`{"jsonrpc":"2.0","id":"1","result":{"taskId":"task-s1","contextId":%q,"artifact":{"parts":[{"text":%q}]}}}`, contextID, text),
		fmt.Sprintf(`{"jsonrpc":"2.0","id":"1","result":{"taskId":"task-s1","contextId":%q,"status":{"state":"completed"}}}`, contextID),
	}
	w.Header().Set("Content-Type", sseContentType)
	w.WriteHeader(http.StatusOK)
	for _, evt := range events {
		fmt.Fprintf(w, "data: %s\n\n", evt)
	}
}

// extractTextFromParams pulls the user text from A2A params.
func extractTextFromParams(params map[string]any) string {
	msg, _ := params["message"].(map[string]any)
	parts, _ := msg["parts"].([]any)
	if len(parts) == 0 {
		return ""
	}
	first, _ := parts[0].(map[string]any)
	text, _ := first["text"].(string)
	return text
}

// bridgeForTest creates an httpBridge pointed at the given A2A port.
func bridgeForTest(t *testing.T, a2aPort int) *httpBridge {
	t.Helper()
	return &httpBridge{
		a2aPort: a2aPort,
		log:     slog.New(slog.NewJSONHandler(io.Discard, nil)),
	}
}

// startBridgeServer starts a full HTTP bridge server (mux with /invocations,
// /ws, /ping) on a random port and returns the base URL.
func startBridgeServer(t *testing.T, b *httpBridge) string {
	t.Helper()
	healthH := newHealthHandler()
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+invocationsPath, b.handleInvocation)
	mux.HandleFunc("/ws", b.handleWebSocket)
	mux.Handle("/ping", healthH)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: defaultReadHeaderTmout,
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})

	return "http://" + ln.Addr().String()
}

// ---------------------------------------------------------------------------
// /invocations — blocking integration tests
// ---------------------------------------------------------------------------

func TestIntegration_Invocation_BasicRoundTrip(t *testing.T) {
	mock := newMockA2AServer(t)
	b := bridgeForTest(t, mock.port(t))
	baseURL := startBridgeServer(t, b)

	resp, err := http.Post(baseURL+invocationsPath, "application/json",
		strings.NewReader(`{"prompt":"hello world"}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var inv invocationResponse
	if err := json.NewDecoder(resp.Body).Decode(&inv); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if inv.Status != "success" {
		t.Errorf("status = %q, want success", inv.Status)
	}
	if inv.Response != "echo: hello world" {
		t.Errorf("response = %q, want %q", inv.Response, "echo: hello world")
	}
	if inv.TaskID != "task-001" {
		t.Errorf("task_id = %q, want task-001", inv.TaskID)
	}
	if inv.Usage == nil {
		t.Fatal("expected usage to be non-nil")
	}
	if inv.Usage.InputTokens != 10 || inv.Usage.OutputTokens != 20 {
		t.Errorf("usage = %+v, want {10, 20}", inv.Usage)
	}
}

func TestIntegration_Invocation_SessionPropagation(t *testing.T) {
	mock := newMockA2AServer(t)
	b := bridgeForTest(t, mock.port(t))
	baseURL := startBridgeServer(t, b)

	req, _ := http.NewRequest(http.MethodPost, baseURL+invocationsPath,
		strings.NewReader(`{"prompt":"multi-turn"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(sessionHeader, "session-42")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()

	var inv invocationResponse
	if err := json.NewDecoder(resp.Body).Decode(&inv); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if inv.ContextID != "session-42" {
		t.Errorf("context_id = %q, want session-42", inv.ContextID)
	}

	// Verify the A2A request included contextId.
	params, ok := mock.lastSendParams.Load().(map[string]any)
	if !ok {
		t.Fatal("expected lastSendParams to be set")
	}
	if params["contextId"] != "session-42" {
		t.Errorf("a2a contextId = %v, want session-42", params["contextId"])
	}
}

func TestIntegration_Invocation_MetadataPassThrough(t *testing.T) {
	mock := newMockA2AServer(t)
	b := bridgeForTest(t, mock.port(t))
	baseURL := startBridgeServer(t, b)

	body := `{"prompt":"test","metadata":{"user":"u1"},"trace_id":"t123"}`
	resp, err := http.Post(baseURL+invocationsPath, "application/json",
		strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()

	params, ok := mock.lastSendParams.Load().(map[string]any)
	if !ok {
		t.Fatal("expected lastSendParams to be set")
	}
	msg, _ := params["message"].(map[string]any)
	md, _ := msg["metadata"].(map[string]any)
	if md == nil {
		t.Fatal("expected metadata on A2A message")
	}
	if md["user"] != "u1" {
		t.Errorf("metadata.user = %v, want u1", md["user"])
	}
	payload, _ := md["payload"].(map[string]any)
	if payload == nil || payload["trace_id"] != "t123" {
		t.Errorf("metadata.payload.trace_id = %v, want t123", payload)
	}
}

func TestIntegration_Invocation_InputField(t *testing.T) {
	mock := newMockA2AServer(t)
	b := bridgeForTest(t, mock.port(t))
	baseURL := startBridgeServer(t, b)

	resp, err := http.Post(baseURL+invocationsPath, "application/json",
		strings.NewReader(`{"input":"using input field"}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()

	var inv invocationResponse
	if err := json.NewDecoder(resp.Body).Decode(&inv); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if inv.Response != "echo: using input field" {
		t.Errorf("response = %q, want %q", inv.Response, "echo: using input field")
	}
}

func TestIntegration_Invocation_EmptyPrompt(t *testing.T) {
	mock := newMockA2AServer(t)
	b := bridgeForTest(t, mock.port(t))
	baseURL := startBridgeServer(t, b)

	resp, err := http.Post(baseURL+invocationsPath, "application/json",
		strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestIntegration_Invocation_InvalidJSON(t *testing.T) {
	mock := newMockA2AServer(t)
	b := bridgeForTest(t, mock.port(t))
	baseURL := startBridgeServer(t, b)

	resp, err := http.Post(baseURL+invocationsPath, "application/json",
		strings.NewReader(`not json at all`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestIntegration_Invocation_A2AError(t *testing.T) {
	mock := newMockA2AServer(t)
	mock.onSend = func(_ map[string]any) (int, string) {
		return http.StatusOK, `{"jsonrpc":"2.0","id":"1","error":{"code":-32000,"message":"model overloaded"}}`
	}
	b := bridgeForTest(t, mock.port(t))
	baseURL := startBridgeServer(t, b)

	resp, err := http.Post(baseURL+invocationsPath, "application/json",
		strings.NewReader(`{"prompt":"test"}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()

	var inv invocationResponse
	if err := json.NewDecoder(resp.Body).Decode(&inv); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if inv.Status != "error" {
		t.Errorf("status = %q, want error", inv.Status)
	}
	if inv.Response != "model overloaded" {
		t.Errorf("response = %q, want %q", inv.Response, "model overloaded")
	}
}

func TestIntegration_Invocation_A2ATaskFailed(t *testing.T) {
	mock := newMockA2AServer(t)
	mock.onSend = func(_ map[string]any) (int, string) {
		return http.StatusOK, `{
			"jsonrpc": "2.0", "id": "1",
			"result": {
				"id": "task-f1",
				"status": {
					"state": "failed",
					"message": {"parts": [{"text": "rate limit exceeded"}]}
				}
			}
		}`
	}
	b := bridgeForTest(t, mock.port(t))
	baseURL := startBridgeServer(t, b)

	resp, err := http.Post(baseURL+invocationsPath, "application/json",
		strings.NewReader(`{"prompt":"test"}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()

	var inv invocationResponse
	if err := json.NewDecoder(resp.Body).Decode(&inv); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if inv.Status != "error" {
		t.Errorf("status = %q, want error", inv.Status)
	}
	if inv.Response != "rate limit exceeded" {
		t.Errorf("response = %q, want %q", inv.Response, "rate limit exceeded")
	}
}

func TestIntegration_Invocation_A2AUnavailable(t *testing.T) {
	// Point bridge at a port with nothing listening.
	b := bridgeForTest(t, 1) // port 1 is never listening
	baseURL := startBridgeServer(t, b)

	resp, err := http.Post(baseURL+invocationsPath, "application/json",
		strings.NewReader(`{"prompt":"test"}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", resp.StatusCode)
	}
}

func TestIntegration_Invocation_ConcurrentRequests(t *testing.T) {
	mock := newMockA2AServer(t)
	b := bridgeForTest(t, mock.port(t))
	baseURL := startBridgeServer(t, b)

	const numRequests = 10
	var wg sync.WaitGroup
	errs := make(chan error, numRequests)

	for i := range numRequests {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			prompt := fmt.Sprintf("request-%d", idx)
			resp, err := http.Post(baseURL+invocationsPath, "application/json",
				strings.NewReader(fmt.Sprintf(`{"prompt":%q}`, prompt)))
			if err != nil {
				errs <- fmt.Errorf("request %d: %w", idx, err)
				return
			}
			defer resp.Body.Close()

			var inv invocationResponse
			if err := json.NewDecoder(resp.Body).Decode(&inv); err != nil {
				errs <- fmt.Errorf("request %d decode: %w", idx, err)
				return
			}
			if inv.Status != "success" {
				errs <- fmt.Errorf("request %d: status=%q", idx, inv.Status)
				return
			}
			expected := "echo: " + prompt
			if inv.Response != expected {
				errs <- fmt.Errorf("request %d: got %q, want %q", idx, inv.Response, expected)
			}
		}(i)
	}

	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}

	mock.mu.Lock()
	defer mock.mu.Unlock()
	if mock.sendCount != numRequests {
		t.Errorf("sendCount = %d, want %d", mock.sendCount, numRequests)
	}
}

func TestIntegration_Invocation_MultipleArtifacts(t *testing.T) {
	mock := newMockA2AServer(t)
	mock.onSend = func(_ map[string]any) (int, string) {
		return http.StatusOK, `{
			"jsonrpc": "2.0", "id": "1",
			"result": {
				"id": "task-m1",
				"status": {"state": "completed"},
				"artifacts": [
					{"parts": [{"text": "part one "}]},
					{"parts": [{"text": "part two"}]}
				]
			}
		}`
	}
	b := bridgeForTest(t, mock.port(t))
	baseURL := startBridgeServer(t, b)

	resp, err := http.Post(baseURL+invocationsPath, "application/json",
		strings.NewReader(`{"prompt":"test"}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()

	var inv invocationResponse
	if err := json.NewDecoder(resp.Body).Decode(&inv); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if inv.Response != "part one part two" {
		t.Errorf("response = %q, want %q", inv.Response, "part one part two")
	}
}

// ---------------------------------------------------------------------------
// /invocations — SSE streaming integration tests
// ---------------------------------------------------------------------------

// readSSEEvents reads all SSE data events from a response body.
func readSSEEvents(t *testing.T, body io.Reader) []sseEvent {
	t.Helper()
	var events []sseEvent
	scanner := bufio.NewScanner(body)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		var evt sseEvent
		if err := json.Unmarshal([]byte(data), &evt); err != nil {
			t.Fatalf("parse SSE event: %v (data=%q)", err, data)
		}
		events = append(events, evt)
	}
	return events
}

func TestIntegration_SSE_BasicStream(t *testing.T) {
	mock := newMockA2AServer(t)
	b := bridgeForTest(t, mock.port(t))
	baseURL := startBridgeServer(t, b)

	req, _ := http.NewRequest(http.MethodPost, baseURL+invocationsPath,
		strings.NewReader(`{"prompt":"stream me"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", sseContentType)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()

	if resp.Header.Get("Content-Type") != sseContentType {
		t.Errorf("Content-Type = %q, want %q", resp.Header.Get("Content-Type"), sseContentType)
	}

	events := readSSEEvents(t, resp.Body)

	// Expected: status(working), text("echo: "), text("stream me"), status(completed), done
	wantTypes := []string{"status", "text", "text", "status", "done"}
	if len(events) != len(wantTypes) {
		t.Fatalf("got %d events, want %d: %+v", len(events), len(wantTypes), events)
	}
	for i, wt := range wantTypes {
		if events[i].Type != wt {
			t.Errorf("event[%d].Type = %q, want %q", i, events[i].Type, wt)
		}
	}

	// Verify content.
	if events[1].Content != "echo: " {
		t.Errorf("event[1].Content = %q, want %q", events[1].Content, "echo: ")
	}
	if events[2].Content != "stream me" {
		t.Errorf("event[2].Content = %q, want %q", events[2].Content, "stream me")
	}

	// Verify task IDs are present.
	if events[0].TaskID != "task-s1" {
		t.Errorf("event[0].TaskID = %q, want task-s1", events[0].TaskID)
	}
}

func TestIntegration_SSE_WithSession(t *testing.T) {
	mock := newMockA2AServer(t)
	b := bridgeForTest(t, mock.port(t))
	baseURL := startBridgeServer(t, b)

	req, _ := http.NewRequest(http.MethodPost, baseURL+invocationsPath,
		strings.NewReader(`{"prompt":"session stream"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", sseContentType)
	req.Header.Set(sessionHeader, "sess-sse-1")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()

	events := readSSEEvents(t, resp.Body)
	// First status event should carry the context ID.
	if len(events) == 0 {
		t.Fatal("expected at least one event")
	}
	if events[0].ContextID != "sess-sse-1" {
		t.Errorf("event[0].ContextID = %q, want sess-sse-1", events[0].ContextID)
	}
}

func TestIntegration_SSE_ErrorDuringStream(t *testing.T) {
	mock := newMockA2AServer(t)
	mock.onStream = func(w http.ResponseWriter, _ map[string]any) {
		w.Header().Set("Content-Type", sseContentType)
		w.WriteHeader(http.StatusOK)
		events := []string{
			`{"jsonrpc":"2.0","id":"1","result":{"taskId":"t1","status":{"state":"working"}}}`,
			`{"jsonrpc":"2.0","id":"1","error":{"code":-32000,"message":"stream error"}}`,
		}
		for _, evt := range events {
			fmt.Fprintf(w, "data: %s\n\n", evt)
		}
	}
	b := bridgeForTest(t, mock.port(t))
	baseURL := startBridgeServer(t, b)

	req, _ := http.NewRequest(http.MethodPost, baseURL+invocationsPath,
		strings.NewReader(`{"prompt":"test"}`))
	req.Header.Set("Accept", sseContentType)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()

	events := readSSEEvents(t, resp.Body)
	// Should contain a status event and an error event.
	hasError := false
	for _, evt := range events {
		if evt.Type == "error" && evt.Content == "stream error" {
			hasError = true
		}
	}
	if !hasError {
		t.Errorf("expected an error event, got %+v", events)
	}
}

func TestIntegration_SSE_FailedTask(t *testing.T) {
	mock := newMockA2AServer(t)
	mock.onStream = func(w http.ResponseWriter, _ map[string]any) {
		w.Header().Set("Content-Type", sseContentType)
		w.WriteHeader(http.StatusOK)
		events := []string{
			`{"jsonrpc":"2.0","id":"1","result":{"taskId":"t1","status":{"state":"working"}}}`,
			`{"jsonrpc":"2.0","id":"1","result":{"taskId":"t1","status":{"state":"failed"}}}`,
		}
		for _, evt := range events {
			fmt.Fprintf(w, "data: %s\n\n", evt)
		}
	}
	b := bridgeForTest(t, mock.port(t))
	baseURL := startBridgeServer(t, b)

	req, _ := http.NewRequest(http.MethodPost, baseURL+invocationsPath,
		strings.NewReader(`{"prompt":"test"}`))
	req.Header.Set("Accept", sseContentType)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()

	events := readSSEEvents(t, resp.Body)
	lastNonDone := events[len(events)-2] // second-to-last should be failed status
	if lastNonDone.Type != "status" || lastNonDone.State != "failed" {
		t.Errorf("expected failed status before done, got %+v", lastNonDone)
	}
	if events[len(events)-1].Type != "done" {
		t.Error("expected done as last event")
	}
}

func TestIntegration_SSE_A2AUnavailable(t *testing.T) {
	b := bridgeForTest(t, 1) // port 1 — nothing listening
	baseURL := startBridgeServer(t, b)

	req, _ := http.NewRequest(http.MethodPost, baseURL+invocationsPath,
		strings.NewReader(`{"prompt":"test"}`))
	req.Header.Set("Accept", sseContentType)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", resp.StatusCode)
	}
}

func TestIntegration_SSE_LargeStream(t *testing.T) {
	const numChunks = 50
	mock := newMockA2AServer(t)
	mock.onStream = func(w http.ResponseWriter, _ map[string]any) {
		w.Header().Set("Content-Type", sseContentType)
		w.WriteHeader(http.StatusOK)

		fmt.Fprintf(w, "data: %s\n\n",
			`{"jsonrpc":"2.0","id":"1","result":{"taskId":"t1","status":{"state":"working"}}}`)

		for i := range numChunks {
			chunk := fmt.Sprintf("chunk-%d ", i)
			evt := fmt.Sprintf(`{"jsonrpc":"2.0","id":"1","result":{"taskId":"t1","artifact":{"parts":[{"text":%q}]}}}`, chunk)
			fmt.Fprintf(w, "data: %s\n\n", evt)
		}

		fmt.Fprintf(w, "data: %s\n\n",
			`{"jsonrpc":"2.0","id":"1","result":{"taskId":"t1","status":{"state":"completed"}}}`)
	}
	b := bridgeForTest(t, mock.port(t))
	baseURL := startBridgeServer(t, b)

	req, _ := http.NewRequest(http.MethodPost, baseURL+invocationsPath,
		strings.NewReader(`{"prompt":"test"}`))
	req.Header.Set("Accept", sseContentType)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()

	events := readSSEEvents(t, resp.Body)
	// 1 working + 50 text + 1 completed + 1 done = 53
	expectedEvents := numChunks + 3
	if len(events) != expectedEvents {
		t.Fatalf("got %d events, want %d", len(events), expectedEvents)
	}

	textCount := 0
	for _, evt := range events {
		if evt.Type == "text" {
			textCount++
		}
	}
	if textCount != numChunks {
		t.Errorf("got %d text events, want %d", textCount, numChunks)
	}
}

// ---------------------------------------------------------------------------
// /ws — WebSocket integration tests
// ---------------------------------------------------------------------------

// dialWS connects to the WebSocket endpoint and returns the connection.
func dialWS(t *testing.T, baseURL string) *websocket.Conn {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(baseURL, "http") + "/ws"
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("ws dial: %v", err)
	}
	t.Cleanup(func() {
		_ = conn.Close()
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
	})
	return conn
}

// readWSResponse reads and parses a single WebSocket message.
func readWSResponse(t *testing.T, conn *websocket.Conn) wsResponse {
	t.Helper()
	_, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("ws read: %v", err)
	}
	var resp wsResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatalf("ws unmarshal: %v (data=%q)", err, data)
	}
	return resp
}

func TestIntegration_WS_BasicRoundTrip(t *testing.T) {
	mock := newMockA2AServer(t)
	b := bridgeForTest(t, mock.port(t))
	baseURL := startBridgeServer(t, b)
	conn := dialWS(t, baseURL)

	if err := conn.WriteJSON(wsRequest{Prompt: "ws hello"}); err != nil {
		t.Fatalf("write: %v", err)
	}

	resp := readWSResponse(t, conn)
	if resp.Type != "text" {
		t.Errorf("Type = %q, want text", resp.Type)
	}
	if resp.Content != "echo: ws hello" {
		t.Errorf("Content = %q, want %q", resp.Content, "echo: ws hello")
	}
	if resp.TaskID != "task-001" {
		t.Errorf("TaskID = %q, want task-001", resp.TaskID)
	}
	if resp.Usage == nil {
		t.Fatal("expected usage to be non-nil")
	}

	done := readWSResponse(t, conn)
	if done.Type != "done" {
		t.Errorf("done.Type = %q, want done", done.Type)
	}
}

func TestIntegration_WS_MultipleMessages(t *testing.T) {
	mock := newMockA2AServer(t)
	b := bridgeForTest(t, mock.port(t))
	baseURL := startBridgeServer(t, b)
	conn := dialWS(t, baseURL)

	// Send 3 messages on the same connection.
	for i := range 3 {
		prompt := fmt.Sprintf("msg-%d", i)
		if err := conn.WriteJSON(wsRequest{Prompt: prompt}); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}

		resp := readWSResponse(t, conn)
		if resp.Type != "text" {
			t.Errorf("msg %d: Type = %q, want text", i, resp.Type)
		}
		expected := "echo: " + prompt
		if resp.Content != expected {
			t.Errorf("msg %d: Content = %q, want %q", i, resp.Content, expected)
		}

		done := readWSResponse(t, conn)
		if done.Type != "done" {
			t.Errorf("msg %d: done.Type = %q, want done", i, done.Type)
		}
	}

	mock.mu.Lock()
	defer mock.mu.Unlock()
	if mock.sendCount != 3 {
		t.Errorf("sendCount = %d, want 3", mock.sendCount)
	}
}

func TestIntegration_WS_ErrorRecovery(t *testing.T) {
	callCount := int32(0)
	mock := newMockA2AServer(t)
	mock.onSend = func(params map[string]any) (int, string) {
		n := atomic.AddInt32(&callCount, 1)
		if n == 1 {
			// First call returns error.
			return http.StatusOK, `{"error":{"message":"first call fails"}}`
		}
		// Subsequent calls succeed.
		text := extractTextFromParams(params)
		return http.StatusOK, fmt.Sprintf(`{
			"result": {
				"id": "task-r1",
				"status": {"state": "completed"},
				"artifacts": [{"parts": [{"text": %q}]}]
			}
		}`, "ok: "+text)
	}
	b := bridgeForTest(t, mock.port(t))
	baseURL := startBridgeServer(t, b)
	conn := dialWS(t, baseURL)

	// First message: should get error.
	if err := conn.WriteJSON(wsRequest{Prompt: "first"}); err != nil {
		t.Fatalf("write: %v", err)
	}
	resp1 := readWSResponse(t, conn)
	if resp1.Type != "error" {
		t.Errorf("first: Type = %q, want error", resp1.Type)
	}

	// Second message: same connection, should succeed.
	if err := conn.WriteJSON(wsRequest{Prompt: "second"}); err != nil {
		t.Fatalf("write: %v", err)
	}
	resp2 := readWSResponse(t, conn)
	if resp2.Type != "text" {
		t.Errorf("second: Type = %q, want text", resp2.Type)
	}
	if resp2.Content != "ok: second" {
		t.Errorf("second: Content = %q, want %q", resp2.Content, "ok: second")
	}
}

func TestIntegration_WS_InvalidJSON(t *testing.T) {
	mock := newMockA2AServer(t)
	b := bridgeForTest(t, mock.port(t))
	baseURL := startBridgeServer(t, b)
	conn := dialWS(t, baseURL)

	if err := conn.WriteMessage(websocket.TextMessage, []byte("not json")); err != nil {
		t.Fatalf("write: %v", err)
	}
	resp := readWSResponse(t, conn)
	if resp.Type != "error" {
		t.Errorf("Type = %q, want error", resp.Type)
	}
	if resp.Content != "invalid JSON" {
		t.Errorf("Content = %q, want %q", resp.Content, "invalid JSON")
	}
}

func TestIntegration_WS_EmptyPrompt(t *testing.T) {
	mock := newMockA2AServer(t)
	b := bridgeForTest(t, mock.port(t))
	baseURL := startBridgeServer(t, b)
	conn := dialWS(t, baseURL)

	if err := conn.WriteJSON(wsRequest{}); err != nil {
		t.Fatalf("write: %v", err)
	}
	resp := readWSResponse(t, conn)
	if resp.Type != "error" {
		t.Errorf("Type = %q, want error", resp.Type)
	}
}

func TestIntegration_WS_A2AUnavailable(t *testing.T) {
	b := bridgeForTest(t, 1) // port 1 — nothing listening
	baseURL := startBridgeServer(t, b)
	conn := dialWS(t, baseURL)

	if err := conn.WriteJSON(wsRequest{Prompt: "test"}); err != nil {
		t.Fatalf("write: %v", err)
	}
	resp := readWSResponse(t, conn)
	if resp.Type != "error" {
		t.Errorf("Type = %q, want error", resp.Type)
	}
	if resp.Content != "agent unavailable" {
		t.Errorf("Content = %q, want %q", resp.Content, "agent unavailable")
	}
}

func TestIntegration_WS_ConcurrentConnections(t *testing.T) {
	mock := newMockA2AServer(t)
	b := bridgeForTest(t, mock.port(t))
	baseURL := startBridgeServer(t, b)

	const numConns = 5
	var wg sync.WaitGroup
	errs := make(chan error, numConns)

	for i := range numConns {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			wsURL := "ws" + strings.TrimPrefix(baseURL, "http") + "/ws"
			conn, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
			if err != nil {
				errs <- fmt.Errorf("conn %d dial: %w", idx, err)
				return
			}
			defer func() {
				_ = conn.Close()
				if resp != nil && resp.Body != nil {
					_ = resp.Body.Close()
				}
			}()

			prompt := fmt.Sprintf("conn-%d", idx)
			if writeErr := conn.WriteJSON(wsRequest{Prompt: prompt}); writeErr != nil {
				errs <- fmt.Errorf("conn %d write: %w", idx, writeErr)
				return
			}

			_, data, readErr := conn.ReadMessage()
			if readErr != nil {
				errs <- fmt.Errorf("conn %d read: %w", idx, readErr)
				return
			}
			var wsResp wsResponse
			if unmarshalErr := json.Unmarshal(data, &wsResp); unmarshalErr != nil {
				errs <- fmt.Errorf("conn %d unmarshal: %w", idx, unmarshalErr)
				return
			}
			expected := "echo: " + prompt
			if wsResp.Content != expected {
				errs <- fmt.Errorf("conn %d: got %q, want %q", idx, wsResp.Content, expected)
			}
		}(i)
	}

	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

// ---------------------------------------------------------------------------
// Cross-endpoint tests
// ---------------------------------------------------------------------------

func TestIntegration_Ping_Endpoint(t *testing.T) {
	mock := newMockA2AServer(t)
	b := bridgeForTest(t, mock.port(t))
	baseURL := startBridgeServer(t, b)

	resp, err := http.Get(baseURL + "/ping")
	if err != nil {
		t.Fatalf("GET /ping: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}

	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["status"] != "healthy" {
		t.Errorf("status = %q, want healthy", body["status"])
	}
}

func TestIntegration_MixedEndpoints(t *testing.T) {
	// Verify that blocking, SSE, and WS can all use the same bridge
	// and A2A server concurrently.
	mock := newMockA2AServer(t)
	b := bridgeForTest(t, mock.port(t))
	baseURL := startBridgeServer(t, b)

	var wg sync.WaitGroup
	errs := make(chan error, 3)

	// Blocking invocation.
	wg.Add(1)
	go func() {
		defer wg.Done()
		resp, err := http.Post(baseURL+invocationsPath, "application/json",
			strings.NewReader(`{"prompt":"blocking"}`))
		if err != nil {
			errs <- fmt.Errorf("blocking: %w", err)
			return
		}
		defer resp.Body.Close()
		var inv invocationResponse
		if decErr := json.NewDecoder(resp.Body).Decode(&inv); decErr != nil {
			errs <- fmt.Errorf("blocking decode: %w", decErr)
			return
		}
		if inv.Status != "success" {
			errs <- fmt.Errorf("blocking: status=%q", inv.Status)
		}
	}()

	// SSE streaming.
	wg.Add(1)
	go func() {
		defer wg.Done()
		req, _ := http.NewRequest(http.MethodPost, baseURL+invocationsPath,
			strings.NewReader(`{"prompt":"streaming"}`))
		req.Header.Set("Accept", sseContentType)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			errs <- fmt.Errorf("sse: %w", err)
			return
		}
		defer resp.Body.Close()
		events := readSSEEvents(t, resp.Body)
		hasDone := false
		for _, evt := range events {
			if evt.Type == "done" {
				hasDone = true
			}
		}
		if !hasDone {
			errs <- fmt.Errorf("sse: missing done event")
		}
	}()

	// WebSocket.
	wg.Add(1)
	go func() {
		defer wg.Done()
		wsURL := "ws" + strings.TrimPrefix(baseURL, "http") + "/ws"
		conn, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
		if err != nil {
			errs <- fmt.Errorf("ws dial: %w", err)
			return
		}
		defer func() {
			_ = conn.Close()
			if resp != nil && resp.Body != nil {
				_ = resp.Body.Close()
			}
		}()
		if writeErr := conn.WriteJSON(wsRequest{Prompt: "websocket"}); writeErr != nil {
			errs <- fmt.Errorf("ws write: %w", writeErr)
			return
		}
		_, data, readErr := conn.ReadMessage()
		if readErr != nil {
			errs <- fmt.Errorf("ws read: %w", readErr)
			return
		}
		var wsResp wsResponse
		if unmarshalErr := json.Unmarshal(data, &wsResp); unmarshalErr != nil {
			errs <- fmt.Errorf("ws unmarshal: %w", unmarshalErr)
			return
		}
		if wsResp.Type != "text" {
			errs <- fmt.Errorf("ws: Type=%q, want text", wsResp.Type)
		}
	}()

	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func TestIntegration_MethodNotAllowed(t *testing.T) {
	mock := newMockA2AServer(t)
	b := bridgeForTest(t, mock.port(t))
	baseURL := startBridgeServer(t, b)

	// GET on /invocations should not match POST-only route.
	resp, err := http.Get(baseURL + invocationsPath)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	// Should get 404 or 405 (unmatched route).
	if resp.StatusCode == http.StatusOK {
		t.Error("expected non-200 for GET /invocations")
	}
}

func TestIntegration_SSE_NonEventLines(t *testing.T) {
	// Verify the bridge correctly skips non-data lines in the SSE stream.
	mock := newMockA2AServer(t)
	mock.onStream = func(w http.ResponseWriter, _ map[string]any) {
		w.Header().Set("Content-Type", sseContentType)
		w.WriteHeader(http.StatusOK)
		// Include comment lines and empty lines (standard SSE).
		lines := []string{
			": this is a comment",
			"",
			`data: {"jsonrpc":"2.0","id":"1","result":{"taskId":"t1","status":{"state":"working"}}}`,
			"",
			"event: custom_event_type",
			`data: {"jsonrpc":"2.0","id":"1","result":{"taskId":"t1","artifact":{"parts":[{"text":"filtered"}]}}}`,
			"",
			`data: {"jsonrpc":"2.0","id":"1","result":{"taskId":"t1","status":{"state":"completed"}}}`,
			"",
		}
		for _, l := range lines {
			fmt.Fprintln(w, l)
		}
	}
	b := bridgeForTest(t, mock.port(t))
	baseURL := startBridgeServer(t, b)

	req, _ := http.NewRequest(http.MethodPost, baseURL+invocationsPath,
		strings.NewReader(`{"prompt":"test"}`))
	req.Header.Set("Accept", sseContentType)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()

	events := readSSEEvents(t, resp.Body)
	// Should have: status(working), text(filtered), status(completed), done
	wantTypes := []string{"status", "text", "status", "done"}
	if len(events) != len(wantTypes) {
		t.Fatalf("got %d events, want %d: %+v", len(events), len(wantTypes), events)
	}
	for i, wt := range wantTypes {
		if events[i].Type != wt {
			t.Errorf("event[%d].Type = %q, want %q", i, events[i].Type, wt)
		}
	}
}

func TestIntegration_WS_WithMetadata(t *testing.T) {
	var receivedParams atomic.Value
	mock := newMockA2AServer(t)
	original := mock.onSend
	mock.onSend = func(params map[string]any) (int, string) {
		receivedParams.Store(params)
		if original != nil {
			return original(params)
		}
		text := extractTextFromParams(params)
		return http.StatusOK, fmt.Sprintf(`{
			"result": {
				"id": "task-md",
				"status": {"state": "completed"},
				"artifacts": [{"parts": [{"text": %q}]}]
			}
		}`, "ok: "+text)
	}
	b := bridgeForTest(t, mock.port(t))
	baseURL := startBridgeServer(t, b)
	conn := dialWS(t, baseURL)

	msg := wsRequest{
		Prompt:   "with metadata",
		Metadata: map[string]any{"user": "test-user", "session": "s1"},
	}
	if err := conn.WriteJSON(msg); err != nil {
		t.Fatalf("write: %v", err)
	}

	resp := readWSResponse(t, conn)
	if resp.Type != "text" {
		t.Fatalf("Type = %q, want text", resp.Type)
	}

	// Verify metadata was forwarded.
	params, ok := receivedParams.Load().(map[string]any)
	if !ok {
		t.Fatal("expected params to be captured")
	}
	message, _ := params["message"].(map[string]any)
	md, _ := message["metadata"].(map[string]any)
	if md["user"] != "test-user" {
		t.Errorf("metadata.user = %v, want test-user", md["user"])
	}
}

func TestIntegration_WS_InputField(t *testing.T) {
	mock := newMockA2AServer(t)
	b := bridgeForTest(t, mock.port(t))
	baseURL := startBridgeServer(t, b)
	conn := dialWS(t, baseURL)

	if err := conn.WriteJSON(wsRequest{Input: "using input"}); err != nil {
		t.Fatalf("write: %v", err)
	}

	resp := readWSResponse(t, conn)
	if resp.Type != "text" {
		t.Errorf("Type = %q, want text", resp.Type)
	}
	if resp.Content != "echo: using input" {
		t.Errorf("Content = %q, want %q", resp.Content, "echo: using input")
	}
}

func TestIntegration_WS_A2ATaskFailed(t *testing.T) {
	mock := newMockA2AServer(t)
	mock.onSend = func(_ map[string]any) (int, string) {
		return http.StatusOK, `{
			"result": {
				"id": "task-f1",
				"status": {
					"state": "failed",
					"message": {"parts": [{"text": "task failed"}]}
				}
			}
		}`
	}
	b := bridgeForTest(t, mock.port(t))
	baseURL := startBridgeServer(t, b)
	conn := dialWS(t, baseURL)

	if err := conn.WriteJSON(wsRequest{Prompt: "test"}); err != nil {
		t.Fatalf("write: %v", err)
	}

	resp := readWSResponse(t, conn)
	if resp.Type != "error" {
		t.Errorf("Type = %q, want error", resp.Type)
	}
	if resp.Content != "task failed" {
		t.Errorf("Content = %q, want %q", resp.Content, "task failed")
	}
}
