package main

// A2A / JSON-RPC message keys and values shared across the HTTP, SSE, and
// WebSocket bridges. Centralized here so the literals are defined once and
// reused (goconst).
const (
	// JSON-RPC and A2A message field keys.
	keyRole      = "role"
	keyParts     = "parts"
	keyKind      = "kind"
	keyMessageID = "messageId"
	keyMessage   = "message"
	keyParams    = "params"
	// keyContextID belongs on the message, not on params. The A2A server
	// reads params.Message.ContextID and ignores a contextId set beside the
	// message, so putting it there silently starts a new conversation on
	// every turn.
	keyContextID     = "contextId"
	keyMethod        = "method"
	keyConfiguration = "configuration"
	keyBlocking      = "blocking"
	keyStatus        = "status"
	keyError         = "error"
	keyJSONRPC       = "jsonrpc"

	// JSON-RPC and A2A values.
	jsonrpcVersion    = "2.0"
	methodMessageSend = "message/send"
	roleUser          = "user"
	// kindText is the A2A "text" part kind; it doubles as the "text" map key
	// and the "text" event type since the underlying string is identical.
	kindText = "text"
)
