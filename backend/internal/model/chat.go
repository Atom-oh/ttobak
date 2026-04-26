package model

const PrefixMsg = "MSG#"

// ChatMessage represents a single chat message in a research conversation
type ChatMessage struct {
	MsgID     string `dynamodbav:"msgId" json:"msgId"`
	Role      string `dynamodbav:"role" json:"role"`                             // "user" | "agent"
	Content   string `dynamodbav:"content" json:"content"`
	Action    string `dynamodbav:"action,omitempty" json:"action,omitempty"`     // propose_structure | ask_question | approve | request_subpage
	Metadata  string `dynamodbav:"metadata,omitempty" json:"metadata,omitempty"` // JSON string
	CreatedAt string `dynamodbav:"createdAt" json:"createdAt"`
}

// ChatMessageItem wraps a ChatMessage with DynamoDB key attributes
// PK: RESEARCH#{researchId}, SK: MSG#{createdAt}#{msgId}
type ChatMessageItem struct {
	PK         string `dynamodbav:"PK"`
	SK         string `dynamodbav:"SK"`
	EntityType string `dynamodbav:"entityType"`
	ChatMessage
}

// SendChatMessageRequest is the request body for sending a chat message
type SendChatMessageRequest struct {
	Content string `json:"content"`
	Action  string `json:"action,omitempty"`
}

// ChatMessagesResponse wraps a list of chat messages
type ChatMessagesResponse struct {
	Messages []ChatMessage `json:"messages"`
}
