package agentcore

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockagentcore"
	dpTypes "github.com/aws/aws-sdk-go-v2/service/bedrockagentcore/types"

	"github.com/AltairaLabs/PromptKit/runtime/statestore"
	"github.com/AltairaLabs/PromptKit/runtime/types"
)

// Constants for the AgentCore state store.
const (
	// defaultActorID is the actor ID used for all events
	// written by the state store.
	defaultActorID = "promptkit"

	// maxPayloadItems is the maximum number of payload items
	// per CreateEvent call (AWS API limit).
	maxPayloadItems = 100

	// multimodalPrefix is prepended to the text content of
	// conversational payloads that carry a JSON-encoded
	// multimodal Message. This avoids the Smithy document
	// limitations of PayloadTypeMemberBlob while preserving
	// full message fidelity.
	multimodalPrefix = "promptkit:multimodal:"

	// PromptKit role constants used for SDK mapping.
	roleUser      = "user"
	roleAssistant = "assistant"
	roleTool      = "tool"
)

// StateStore implements statestore.Store and
// statestore.MessageAppender by persisting conversation
// messages as events in an AWS Bedrock AgentCore Memory
// resource.
type StateStore struct {
	memoryID    string
	client      DataPlaneClient
	savedCounts map[string]int
	mu          sync.Mutex
}

// Compile-time interface checks.
var (
	_ statestore.Store           = (*StateStore)(nil)
	_ statestore.MessageAppender = (*StateStore)(nil)
)

// NewStateStore creates a new StateStore.
func NewStateStore(
	memoryID string, client DataPlaneClient,
) *StateStore {
	return &StateStore{
		memoryID:    memoryID,
		client:      client,
		savedCounts: make(map[string]int),
	}
}

// Load retrieves a conversation by listing all events for
// the given session ID.
func (s *StateStore) Load(
	ctx context.Context, id string,
) (*statestore.ConversationState, error) {
	messages, err := s.loadAllMessages(ctx, id)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	s.savedCounts[id] = len(messages)
	s.mu.Unlock()

	return &statestore.ConversationState{
		ID:       id,
		Messages: messages,
	}, nil
}

// Save persists only new (delta) messages since the last
// Load or Save for this conversation.
func (s *StateStore) Save(
	ctx context.Context,
	state *statestore.ConversationState,
) error {
	s.mu.Lock()
	saved := s.savedCounts[state.ID]
	s.mu.Unlock()

	if saved >= len(state.Messages) {
		return nil
	}

	newMsgs := state.Messages[saved:]
	if err := s.writeMessages(
		ctx, state.ID, newMsgs,
	); err != nil {
		return err
	}

	s.mu.Lock()
	s.savedCounts[state.ID] = len(state.Messages)
	s.mu.Unlock()

	return nil
}

// Fork copies all events from sourceID into a new session
// newID.
func (s *StateStore) Fork(
	ctx context.Context, sourceID, newID string,
) error {
	messages, err := s.loadAllMessages(ctx, sourceID)
	if err != nil {
		return err
	}
	if len(messages) == 0 {
		return statestore.ErrNotFound
	}

	if err := s.writeMessages(
		ctx, newID, messages,
	); err != nil {
		return err
	}

	s.mu.Lock()
	s.savedCounts[newID] = len(messages)
	s.mu.Unlock()

	return nil
}

// AppendMessages writes messages directly without requiring
// a prior Load.
func (s *StateStore) AppendMessages(
	ctx context.Context,
	id string,
	messages []types.Message,
) error {
	if err := s.writeMessages(
		ctx, id, messages,
	); err != nil {
		return err
	}

	s.mu.Lock()
	s.savedCounts[id] += len(messages)
	s.mu.Unlock()

	return nil
}

// ---------- internal helpers ----------

// loadAllMessages fetches all events for a session, following
// pagination, and converts them to PromptKit messages.
func (s *StateStore) loadAllMessages(
	ctx context.Context, sessionID string,
) ([]types.Message, error) {
	var allMessages []types.Message
	var nextToken *string

	for {
		out, err := s.client.ListEvents(
			ctx,
			&bedrockagentcore.ListEventsInput{
				MemoryId:  aws.String(s.memoryID),
				NextToken: nextToken,
			},
		)
		if err != nil {
			return nil, fmt.Errorf(
				"ListEvents for session %q: %w",
				sessionID, err,
			)
		}

		for i := range out.Events {
			msgs := extractMessagesFromEvent(
				&out.Events[i], sessionID,
			)
			allMessages = append(
				allMessages, msgs...,
			)
		}

		if out.NextToken == nil {
			break
		}
		nextToken = out.NextToken
	}

	return allMessages, nil
}

// extractMessagesFromEvent converts a single Event's payload
// items into PromptKit messages. Only events matching the
// target sessionID are included.
func extractMessagesFromEvent(
	event *dpTypes.Event, sessionID string,
) []types.Message {
	if aws.ToString(event.SessionId) != sessionID {
		return nil
	}

	var messages []types.Message
	for _, payload := range event.Payload {
		msg, ok := convertPayloadToMessage(payload)
		if ok {
			messages = append(messages, msg)
		}
	}
	return messages
}

// convertPayloadToMessage maps a single PayloadType to a
// PromptKit Message. Returns false if the payload cannot be
// converted.
func convertPayloadToMessage(
	payload dpTypes.PayloadType,
) (types.Message, bool) {
	conv, ok := payload.(*dpTypes.PayloadTypeMemberConversational)
	if !ok {
		return types.Message{}, false
	}
	return convertConversational(conv.Value), true
}

// convertConversational maps a Conversational payload to a
// PromptKit Message. If the text content starts with the
// multimodal prefix, it is decoded as a full JSON Message.
func convertConversational(
	conv dpTypes.Conversational,
) types.Message {
	text := extractTextContent(conv.Content)

	if strings.HasPrefix(text, multimodalPrefix) {
		jsonStr := strings.TrimPrefix(text, multimodalPrefix)
		var msg types.Message
		if err := json.Unmarshal(
			[]byte(jsonStr), &msg,
		); err == nil {
			return msg
		}
	}

	role := mapRoleFromSDK(conv.Role)
	return types.Message{Role: role, Content: text}
}

// extractTextContent extracts the text string from the
// Content union type.
func extractTextContent(content dpTypes.Content) string {
	if c, ok := content.(*dpTypes.ContentMemberText); ok {
		return c.Value
	}
	return ""
}

// writeMessages converts PromptKit messages to payloads and
// writes them in batches of maxPayloadItems.
func (s *StateStore) writeMessages(
	ctx context.Context, sessionID string,
	messages []types.Message,
) error {
	payloads := make(
		[]dpTypes.PayloadType, 0, len(messages),
	)
	for i := range messages {
		payloads = append(
			payloads,
			convertMessageToPayload(&messages[i]),
		)
	}

	return s.writeBatches(ctx, sessionID, payloads)
}

// writeBatches sends payloads in chunks of maxPayloadItems.
func (s *StateStore) writeBatches(
	ctx context.Context, sessionID string,
	payloads []dpTypes.PayloadType,
) error {
	for i := 0; i < len(payloads); i += maxPayloadItems {
		end := i + maxPayloadItems
		if end > len(payloads) {
			end = len(payloads)
		}

		now := time.Now()
		_, err := s.client.CreateEvent(
			ctx,
			&bedrockagentcore.CreateEventInput{
				MemoryId:       aws.String(s.memoryID),
				ActorId:        aws.String(defaultActorID),
				SessionId:      aws.String(sessionID),
				EventTimestamp: &now,
				Payload:        payloads[i:end],
			},
		)
		if err != nil {
			return fmt.Errorf(
				"CreateEvent for session %q: %w",
				sessionID, err,
			)
		}
	}
	return nil
}

// convertMessageToPayload maps a PromptKit Message to an AWS
// PayloadType. All messages use the conversational format.
// Multimodal messages are JSON-encoded with a prefix marker
// in the text content to preserve full fidelity.
func convertMessageToPayload(
	msg *types.Message,
) dpTypes.PayloadType {
	if msg.IsMultimodal() {
		return messageToMultimodalPayload(msg)
	}
	return messageToConversational(msg)
}

// messageToMultimodalPayload encodes a multimodal Message as
// a conversational payload with JSON in the text field,
// prefixed by the multimodal marker.
func messageToMultimodalPayload(
	msg *types.Message,
) dpTypes.PayloadType {
	data, err := json.Marshal(msg)
	if err != nil {
		return messageToConversational(msg)
	}
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

// messageToConversational builds a conversational payload
// from a text-only message.
func messageToConversational(
	msg *types.Message,
) dpTypes.PayloadType {
	return &dpTypes.PayloadTypeMemberConversational{
		Value: dpTypes.Conversational{
			Role: mapRoleToSDK(msg.Role),
			Content: &dpTypes.ContentMemberText{
				Value: msg.Content,
			},
		},
	}
}

// mapRoleToSDK converts a PromptKit role string to the SDK
// Role enum.
func mapRoleToSDK(role string) dpTypes.Role {
	switch role {
	case roleUser:
		return dpTypes.RoleUser
	case roleAssistant:
		return dpTypes.RoleAssistant
	case roleTool:
		return dpTypes.RoleTool
	default:
		return dpTypes.RoleOther
	}
}

// mapRoleFromSDK converts an SDK Role enum to a PromptKit
// role string.
func mapRoleFromSDK(role dpTypes.Role) string {
	switch role {
	case dpTypes.RoleUser:
		return roleUser
	case dpTypes.RoleAssistant:
		return roleAssistant
	case dpTypes.RoleTool:
		return roleTool
	case dpTypes.RoleOther:
		return string(role)
	default:
		return string(role)
	}
}
