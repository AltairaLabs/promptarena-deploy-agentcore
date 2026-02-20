package agentcore

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockagentcore"
	dpTypes "github.com/aws/aws-sdk-go-v2/service/bedrockagentcore/types"

	"github.com/AltairaLabs/PromptKit/runtime/statestore"
	"github.com/AltairaLabs/PromptKit/runtime/types"
)

// ---------- mock DataPlaneClient ----------

type mockDataPlaneClient struct {
	createEventFn func(
		ctx context.Context,
		input *bedrockagentcore.CreateEventInput,
		opts ...func(*bedrockagentcore.Options),
	) (*bedrockagentcore.CreateEventOutput, error)

	listEventsFn func(
		ctx context.Context,
		input *bedrockagentcore.ListEventsInput,
		opts ...func(*bedrockagentcore.Options),
	) (*bedrockagentcore.ListEventsOutput, error)

	createCalls []*bedrockagentcore.CreateEventInput
}

func (m *mockDataPlaneClient) CreateEvent(
	ctx context.Context,
	input *bedrockagentcore.CreateEventInput,
	opts ...func(*bedrockagentcore.Options),
) (*bedrockagentcore.CreateEventOutput, error) {
	m.createCalls = append(m.createCalls, input)
	if m.createEventFn != nil {
		return m.createEventFn(ctx, input, opts...)
	}
	return &bedrockagentcore.CreateEventOutput{}, nil
}

func (m *mockDataPlaneClient) ListEvents(
	ctx context.Context,
	input *bedrockagentcore.ListEventsInput,
	opts ...func(*bedrockagentcore.Options),
) (*bedrockagentcore.ListEventsOutput, error) {
	if m.listEventsFn != nil {
		return m.listEventsFn(ctx, input, opts...)
	}
	return &bedrockagentcore.ListEventsOutput{}, nil
}

// ---------- helpers ----------

func convPayload(
	role dpTypes.Role, text string,
) dpTypes.PayloadType {
	return &dpTypes.PayloadTypeMemberConversational{
		Value: dpTypes.Conversational{
			Role: role,
			Content: &dpTypes.ContentMemberText{
				Value: text,
			},
		},
	}
}

func multimodalConvPayload(
	msg types.Message,
) dpTypes.PayloadType {
	data, _ := json.Marshal(msg)
	text := multimodalPrefix + string(data)
	return &dpTypes.PayloadTypeMemberConversational{
		Value: dpTypes.Conversational{
			Role: mapRoleToSDK(msg.Role),
			Content: &dpTypes.ContentMemberText{
				Value: text,
			},
		},
	}
}

func makeEvent(
	sessionID string, payloads ...dpTypes.PayloadType,
) dpTypes.Event {
	return dpTypes.Event{
		SessionId: aws.String(sessionID),
		ActorId:   aws.String(defaultActorID),
		MemoryId:  aws.String("mem-1"),
		EventId:   aws.String("evt-1"),
		Payload:   payloads,
	}
}

// ---------- Load tests ----------

func TestLoad_EmptySession(t *testing.T) {
	mock := &mockDataPlaneClient{
		listEventsFn: func(
			_ context.Context,
			_ *bedrockagentcore.ListEventsInput,
			_ ...func(*bedrockagentcore.Options),
		) (*bedrockagentcore.ListEventsOutput, error) {
			return &bedrockagentcore.ListEventsOutput{
				Events: []dpTypes.Event{},
			}, nil
		},
	}

	store := NewStateStore("mem-1", mock)
	state, err := store.Load(context.Background(), "sess-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state.ID != "sess-1" {
		t.Errorf("got ID %q, want %q", state.ID, "sess-1")
	}
	if len(state.Messages) != 0 {
		t.Errorf("got %d messages, want 0", len(state.Messages))
	}
}

func TestLoad_TextMessages(t *testing.T) {
	mock := &mockDataPlaneClient{
		listEventsFn: func(
			_ context.Context,
			_ *bedrockagentcore.ListEventsInput,
			_ ...func(*bedrockagentcore.Options),
		) (*bedrockagentcore.ListEventsOutput, error) {
			return &bedrockagentcore.ListEventsOutput{
				Events: []dpTypes.Event{
					makeEvent("sess-1",
						convPayload(dpTypes.RoleUser, "hello"),
						convPayload(dpTypes.RoleAssistant, "hi"),
					),
				},
			}, nil
		},
	}

	store := NewStateStore("mem-1", mock)
	state, err := store.Load(context.Background(), "sess-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(state.Messages) != 2 {
		t.Fatalf("got %d messages, want 2", len(state.Messages))
	}

	tests := []struct {
		idx     int
		role    string
		content string
	}{
		{0, "user", "hello"},
		{1, "assistant", "hi"},
	}
	for _, tt := range tests {
		msg := state.Messages[tt.idx]
		if msg.Role != tt.role {
			t.Errorf("msg[%d] role: got %q, want %q",
				tt.idx, msg.Role, tt.role)
		}
		if msg.Content != tt.content {
			t.Errorf("msg[%d] content: got %q, want %q",
				tt.idx, msg.Content, tt.content)
		}
	}
}

func TestLoad_MultimodalMessage(t *testing.T) {
	textVal := "multimodal text"
	multiMsg := types.Message{
		Role: "user",
		Parts: []types.ContentPart{
			{
				Type: types.ContentTypeText,
				Text: &textVal,
			},
		},
	}

	mock := &mockDataPlaneClient{
		listEventsFn: func(
			_ context.Context,
			_ *bedrockagentcore.ListEventsInput,
			_ ...func(*bedrockagentcore.Options),
		) (*bedrockagentcore.ListEventsOutput, error) {
			return &bedrockagentcore.ListEventsOutput{
				Events: []dpTypes.Event{
					makeEvent("sess-1",
						multimodalConvPayload(multiMsg),
					),
				},
			}, nil
		},
	}

	store := NewStateStore("mem-1", mock)
	state, err := store.Load(context.Background(), "sess-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(state.Messages) != 1 {
		t.Fatalf("got %d messages, want 1",
			len(state.Messages))
	}
	msg := state.Messages[0]
	if msg.Role != "user" {
		t.Errorf("role: got %q, want %q",
			msg.Role, "user")
	}
	if !msg.IsMultimodal() {
		t.Error("expected multimodal message")
	}
	if msg.Parts[0].Type != types.ContentTypeText {
		t.Errorf("part type: got %q, want %q",
			msg.Parts[0].Type, types.ContentTypeText)
	}
	if *msg.Parts[0].Text != "multimodal text" {
		t.Errorf("part text: got %q, want %q",
			*msg.Parts[0].Text, "multimodal text")
	}
}

func TestLoad_Pagination(t *testing.T) {
	callCount := 0
	mock := &mockDataPlaneClient{
		listEventsFn: func(
			_ context.Context,
			_ *bedrockagentcore.ListEventsInput,
			_ ...func(*bedrockagentcore.Options),
		) (*bedrockagentcore.ListEventsOutput, error) {
			callCount++
			if callCount == 1 {
				return &bedrockagentcore.ListEventsOutput{
					Events: []dpTypes.Event{
						makeEvent("sess-1",
							convPayload(
								dpTypes.RoleUser, "page1",
							),
						),
					},
					NextToken: aws.String("token-2"),
				}, nil
			}
			return &bedrockagentcore.ListEventsOutput{
				Events: []dpTypes.Event{
					makeEvent("sess-1",
						convPayload(
							dpTypes.RoleAssistant, "page2",
						),
					),
				},
			}, nil
		},
	}

	store := NewStateStore("mem-1", mock)
	state, err := store.Load(context.Background(), "sess-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(state.Messages) != 2 {
		t.Fatalf("got %d messages, want 2", len(state.Messages))
	}
	if state.Messages[0].Content != "page1" {
		t.Errorf("msg[0]: got %q, want %q",
			state.Messages[0].Content, "page1")
	}
	if state.Messages[1].Content != "page2" {
		t.Errorf("msg[1]: got %q, want %q",
			state.Messages[1].Content, "page2")
	}
	if callCount != 2 {
		t.Errorf("got %d calls, want 2", callCount)
	}
}

// ---------- Save tests ----------

func TestSave_DeltaOnly(t *testing.T) {
	mock := &mockDataPlaneClient{
		listEventsFn: func(
			_ context.Context,
			_ *bedrockagentcore.ListEventsInput,
			_ ...func(*bedrockagentcore.Options),
		) (*bedrockagentcore.ListEventsOutput, error) {
			return &bedrockagentcore.ListEventsOutput{
				Events: []dpTypes.Event{
					makeEvent("sess-1",
						convPayload(
							dpTypes.RoleUser, "existing",
						),
					),
				},
			}, nil
		},
	}

	store := NewStateStore("mem-1", mock)
	ctx := context.Background()

	_, err := store.Load(ctx, "sess-1")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	state := &statestore.ConversationState{
		ID: "sess-1",
		Messages: []types.Message{
			{Role: "user", Content: "existing"},
			{Role: "assistant", Content: "new reply"},
		},
	}
	if err := store.Save(ctx, state); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if len(mock.createCalls) != 1 {
		t.Fatalf("got %d calls, want 1",
			len(mock.createCalls))
	}
	payloads := mock.createCalls[0].Payload
	if len(payloads) != 1 {
		t.Fatalf("got %d payloads, want 1", len(payloads))
	}
	conv, ok := payloads[0].(*dpTypes.PayloadTypeMemberConversational)
	if !ok {
		t.Fatal("expected conversational payload")
	}
	if conv.Value.Role != dpTypes.RoleAssistant {
		t.Errorf("role: got %q, want %q",
			conv.Value.Role, dpTypes.RoleAssistant)
	}
}

func TestSave_NoNewMessages(t *testing.T) {
	mock := &mockDataPlaneClient{
		listEventsFn: func(
			_ context.Context,
			_ *bedrockagentcore.ListEventsInput,
			_ ...func(*bedrockagentcore.Options),
		) (*bedrockagentcore.ListEventsOutput, error) {
			return &bedrockagentcore.ListEventsOutput{
				Events: []dpTypes.Event{
					makeEvent("sess-1",
						convPayload(dpTypes.RoleUser, "hi"),
					),
				},
			}, nil
		},
	}

	store := NewStateStore("mem-1", mock)
	ctx := context.Background()

	_, err := store.Load(ctx, "sess-1")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	state := &statestore.ConversationState{
		ID: "sess-1",
		Messages: []types.Message{
			{Role: "user", Content: "hi"},
		},
	}
	if err := store.Save(ctx, state); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if len(mock.createCalls) != 0 {
		t.Errorf("got %d calls, want 0",
			len(mock.createCalls))
	}
}

func TestSave_TextPayload(t *testing.T) {
	mock := &mockDataPlaneClient{}
	store := NewStateStore("mem-1", mock)

	state := &statestore.ConversationState{
		ID: "sess-1",
		Messages: []types.Message{
			{Role: "user", Content: "hello"},
		},
	}
	if err := store.Save(
		context.Background(), state,
	); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if len(mock.createCalls) != 1 {
		t.Fatalf("got %d calls, want 1",
			len(mock.createCalls))
	}
	payload := mock.createCalls[0].Payload[0]
	conv, ok := payload.(*dpTypes.PayloadTypeMemberConversational)
	if !ok {
		t.Fatal("expected conversational payload")
	}
	if conv.Value.Role != dpTypes.RoleUser {
		t.Errorf("role: got %q, want %q",
			conv.Value.Role, dpTypes.RoleUser)
	}
	text, ok := conv.Value.Content.(*dpTypes.ContentMemberText)
	if !ok {
		t.Fatal("expected text content")
	}
	if text.Value != "hello" {
		t.Errorf("text: got %q, want %q",
			text.Value, "hello")
	}
}

func TestSave_MultimodalPayload(t *testing.T) {
	mock := &mockDataPlaneClient{}
	store := NewStateStore("mem-1", mock)

	textVal := "see this image"
	state := &statestore.ConversationState{
		ID: "sess-1",
		Messages: []types.Message{
			{
				Role: "user",
				Parts: []types.ContentPart{
					{
						Type: types.ContentTypeText,
						Text: &textVal,
					},
				},
			},
		},
	}
	if err := store.Save(
		context.Background(), state,
	); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if len(mock.createCalls) != 1 {
		t.Fatalf("got %d calls, want 1",
			len(mock.createCalls))
	}
	payload := mock.createCalls[0].Payload[0]
	conv, ok := payload.(*dpTypes.PayloadTypeMemberConversational)
	if !ok {
		t.Fatal("expected conversational payload")
	}
	text, ok := conv.Value.Content.(*dpTypes.ContentMemberText)
	if !ok {
		t.Fatal("expected text content")
	}
	if !strings.HasPrefix(text.Value, multimodalPrefix) {
		t.Fatalf("expected multimodal prefix, got %q",
			text.Value)
	}
	jsonStr := strings.TrimPrefix(
		text.Value, multimodalPrefix,
	)
	var decoded types.Message
	if err := json.Unmarshal(
		[]byte(jsonStr), &decoded,
	); err != nil {
		t.Fatalf("failed to decode multimodal JSON: %v",
			err)
	}
	if decoded.Role != "user" {
		t.Errorf("role: got %q, want %q",
			decoded.Role, "user")
	}
	if !decoded.IsMultimodal() {
		t.Error("expected decoded message to be multimodal")
	}
}

func TestSave_Batching(t *testing.T) {
	mock := &mockDataPlaneClient{}
	store := NewStateStore("mem-1", mock)

	totalMessages := 150
	messages := make([]types.Message, totalMessages)
	for i := range totalMessages {
		messages[i] = types.Message{
			Role:    "user",
			Content: fmt.Sprintf("msg-%d", i),
		}
	}

	state := &statestore.ConversationState{
		ID:       "sess-1",
		Messages: messages,
	}
	if err := store.Save(
		context.Background(), state,
	); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if len(mock.createCalls) != 2 {
		t.Fatalf("got %d calls, want 2",
			len(mock.createCalls))
	}
	if len(mock.createCalls[0].Payload) != maxPayloadItems {
		t.Errorf("batch 1: got %d, want %d",
			len(mock.createCalls[0].Payload),
			maxPayloadItems)
	}
	secondBatch := totalMessages - maxPayloadItems
	if len(mock.createCalls[1].Payload) != secondBatch {
		t.Errorf("batch 2: got %d, want %d",
			len(mock.createCalls[1].Payload), secondBatch)
	}
}

// ---------- AppendMessages tests ----------

func TestAppendMessages(t *testing.T) {
	mock := &mockDataPlaneClient{}
	store := NewStateStore("mem-1", mock)

	msgs := []types.Message{
		{Role: "user", Content: "appended"},
	}
	err := store.AppendMessages(
		context.Background(), "sess-1", msgs,
	)
	if err != nil {
		t.Fatalf("AppendMessages: %v", err)
	}

	if len(mock.createCalls) != 1 {
		t.Fatalf("got %d calls, want 1",
			len(mock.createCalls))
	}
	if aws.ToString(
		mock.createCalls[0].SessionId,
	) != "sess-1" {
		t.Errorf("session: got %q, want %q",
			aws.ToString(mock.createCalls[0].SessionId),
			"sess-1")
	}
}

func TestAppendMessages_UpdatesSavedCount(t *testing.T) {
	mock := &mockDataPlaneClient{}
	store := NewStateStore("mem-1", mock)
	ctx := context.Background()

	msgs := []types.Message{
		{Role: "user", Content: "a"},
		{Role: "assistant", Content: "b"},
	}
	if err := store.AppendMessages(
		ctx, "sess-1", msgs,
	); err != nil {
		t.Fatalf("AppendMessages: %v", err)
	}

	// Save with 2 old + 1 new should only write the new.
	mock.createCalls = nil
	state := &statestore.ConversationState{
		ID: "sess-1",
		Messages: []types.Message{
			{Role: "user", Content: "a"},
			{Role: "assistant", Content: "b"},
			{Role: "user", Content: "c"},
		},
	}
	if err := store.Save(ctx, state); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if len(mock.createCalls) != 1 {
		t.Fatalf("got %d calls, want 1",
			len(mock.createCalls))
	}
	if len(mock.createCalls[0].Payload) != 1 {
		t.Errorf("got %d payloads, want 1",
			len(mock.createCalls[0].Payload))
	}
}

// ---------- Fork tests ----------

func TestFork(t *testing.T) {
	mock := &mockDataPlaneClient{
		listEventsFn: func(
			_ context.Context,
			_ *bedrockagentcore.ListEventsInput,
			_ ...func(*bedrockagentcore.Options),
		) (*bedrockagentcore.ListEventsOutput, error) {
			return &bedrockagentcore.ListEventsOutput{
				Events: []dpTypes.Event{
					makeEvent("src",
						convPayload(
							dpTypes.RoleUser, "forked",
						),
						convPayload(
							dpTypes.RoleAssistant, "reply",
						),
					),
				},
			}, nil
		},
	}

	store := NewStateStore("mem-1", mock)
	err := store.Fork(context.Background(), "src", "dst")
	if err != nil {
		t.Fatalf("Fork: %v", err)
	}

	if len(mock.createCalls) != 1 {
		t.Fatalf("got %d calls, want 1",
			len(mock.createCalls))
	}
	if aws.ToString(
		mock.createCalls[0].SessionId,
	) != "dst" {
		t.Errorf("session: got %q, want %q",
			aws.ToString(mock.createCalls[0].SessionId),
			"dst")
	}
	if len(mock.createCalls[0].Payload) != 2 {
		t.Errorf("got %d payloads, want 2",
			len(mock.createCalls[0].Payload))
	}
}

func TestFork_EmptySource(t *testing.T) {
	mock := &mockDataPlaneClient{
		listEventsFn: func(
			_ context.Context,
			_ *bedrockagentcore.ListEventsInput,
			_ ...func(*bedrockagentcore.Options),
		) (*bedrockagentcore.ListEventsOutput, error) {
			return &bedrockagentcore.ListEventsOutput{
				Events: []dpTypes.Event{},
			}, nil
		},
	}

	store := NewStateStore("mem-1", mock)
	err := store.Fork(context.Background(), "empty", "dst")
	if err != statestore.ErrNotFound {
		t.Errorf("got %v, want ErrNotFound", err)
	}
}

// ---------- Role mapping tests ----------

func TestRoleMapping(t *testing.T) {
	tests := []struct {
		promptkit string
		sdk       dpTypes.Role
	}{
		{"user", dpTypes.RoleUser},
		{"assistant", dpTypes.RoleAssistant},
		{"tool", dpTypes.RoleTool},
		{"system", dpTypes.RoleOther},
		{"unknown", dpTypes.RoleOther},
	}
	for _, tt := range tests {
		t.Run("to_"+tt.promptkit, func(t *testing.T) {
			got := mapRoleToSDK(tt.promptkit)
			if got != tt.sdk {
				t.Errorf("mapRoleToSDK(%q) = %q, want %q",
					tt.promptkit, got, tt.sdk)
			}
		})
	}

	reverseTests := []struct {
		sdk       dpTypes.Role
		promptkit string
	}{
		{dpTypes.RoleUser, "user"},
		{dpTypes.RoleAssistant, "assistant"},
		{dpTypes.RoleTool, "tool"},
	}
	for _, tt := range reverseTests {
		t.Run("from_"+string(tt.sdk), func(t *testing.T) {
			got := mapRoleFromSDK(tt.sdk)
			if got != tt.promptkit {
				t.Errorf(
					"mapRoleFromSDK(%q) = %q, want %q",
					tt.sdk, got, tt.promptkit,
				)
			}
		})
	}
}
