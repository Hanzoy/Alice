package builtin

import "alice/internal/facts"

const ConversationSchema = "alice.conversation.v1"

type MessageInput struct {
	Text        string                `json:"text"`
	Source      string                `json:"source,omitempty"`
	ReplyHandle string                `json:"reply_handle,omitempty"`
	History     []ConversationMessage `json:"history,omitempty"`
}

type ConversationMessage struct {
	Role      string `json:"role"`
	Content   string `json:"content"`
	CreatedAt int64  `json:"created_at,omitempty"`
}
type MemoryDecision struct {
	Action   string `json:"action"`
	Strategy string `json:"strategy,omitempty"`
	Question string `json:"question,omitempty"`
	Error    string `json:"error,omitempty"`
}

type Conversation struct {
	Input          MessageInput          `json:"input"`
	History        []ConversationMessage `json:"history,omitempty"`
	RecalledFacts  []facts.Fact          `json:"recalled_facts,omitempty"`
	Prompt         string                `json:"prompt,omitempty"`
	Reply          string                `json:"reply,omitempty"`
	CreatedFacts   []facts.Fact          `json:"created_facts,omitempty"`
	MemoryDecision MemoryDecision        `json:"memory_decision,omitempty"`
}

type TaskCompletion struct {
	TaskID      string `json:"task_id"`
	Label       string `json:"label,omitempty"`
	Status      string `json:"status"`
	Result      string `json:"result,omitempty"`
	ExecutionID string `json:"execution_id,omitempty"`
	FinishedAt  int64  `json:"finished_at"`
	Remember    bool   `json:"remember"`
}
