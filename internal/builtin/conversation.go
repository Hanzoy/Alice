package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"alice/internal/facts"
	"alice/pkg/component"
)

type Normalize struct{}

func (Normalize) Descriptor() component.Descriptor {
	return component.Descriptor{ID: "message.normalize", Version: "builtin-1", Description: "Normalize any text input into Alice's conversation envelope.", Kind: "transform", Lifecycle: "per_call", InputSchema: "alice.message.v1", OutputSchema: ConversationSchema, BuiltIn: true, Protected: true}
}

func (Normalize) Execute(_ context.Context, in component.Envelope) (component.Envelope, error) {
	var message MessageInput
	if err := json.Unmarshal(in.Payload, &message); err != nil {
		return component.Envelope{}, fmt.Errorf("normalize message: %w", err)
	}
	message.Text = strings.TrimSpace(message.Text)
	if message.Text == "" {
		return component.Envelope{}, fmt.Errorf("message text is required")
	}
	if message.Source == "" {
		message.Source = in.Source
	}
	if message.ReplyHandle == "" {
		message.ReplyHandle = in.ReplyHandle
	}
	out := in
	out.Schema = ConversationSchema
	out.Payload, _ = json.Marshal(Conversation{Input: message, History: message.History})
	return out, nil
}

type FactRetriever interface {
	Search(context.Context, string, int) ([]facts.Fact, error)
}
type FactQuery struct{ Retriever FactRetriever }

func (FactQuery) Descriptor() component.Descriptor {
	return component.Descriptor{ID: "memory.fact.query", Version: "builtin-1", Description: "Query relevant sourced facts using the current conversation text.", Kind: "query", Lifecycle: "singleton", InputSchema: ConversationSchema, OutputSchema: ConversationSchema, BuiltIn: true, Protected: true}
}

func (c FactQuery) Execute(ctx context.Context, in component.Envelope) (component.Envelope, error) {
	state, err := decodeConversation(in)
	if err != nil {
		return component.Envelope{}, err
	}
	if c.Retriever != nil {
		state.RecalledFacts, err = c.Retriever.Search(ctx, state.Input.Text, 5)
		if err != nil {
			return component.Envelope{}, err
		}
	}
	in.Payload, _ = json.Marshal(state)
	return in, nil
}

type ContextAssembler struct{}

func (ContextAssembler) Descriptor() component.Descriptor {
	return component.Descriptor{ID: "context.assemble", Version: "builtin-1", Description: "Assemble user input and recalled facts for the configured language model.", Kind: "transform", Lifecycle: "per_call", InputSchema: ConversationSchema, OutputSchema: ConversationSchema, BuiltIn: true, Protected: true}
}

func (ContextAssembler) Execute(_ context.Context, in component.Envelope) (component.Envelope, error) {
	state, err := decodeConversation(in)
	if err != nil {
		return component.Envelope{}, err
	}
	var b strings.Builder
	if len(state.History) > 0 {
		b.WriteString("最近对话（同一个 Alice 时间线）：\n")
		for _, m := range state.History {
			fmt.Fprintf(&b, "- %s: %s\n", m.Role, m.Content)
		}
		b.WriteString("\n")
	}
	b.WriteString("用户输入：\n")
	b.WriteString(state.Input.Text)
	if len(state.RecalledFacts) > 0 {
		b.WriteString("\n\n可能相关的有来源事实（可能过期，不得覆盖当前输入）：\n")
		for _, fact := range state.RecalledFacts {
			fmt.Fprintf(&b, "- %s %s %s [source=%s]\n", fact.Subject, fact.Predicate, fact.Object, fact.SourceKind)
		}
	}
	state.Prompt = b.String()
	in.Payload, _ = json.Marshal(state)
	return in, nil
}

type ChatModel interface {
	Reply(context.Context, string) (string, error)
}

type StructuredChatModel interface {
	Chat(context.Context, string, string, bool) (string, error)
	Configured() bool
}

type Chat struct{ Model ChatModel }

func (Chat) Descriptor() component.Descriptor {
	return component.Descriptor{ID: "llm.chat", Version: "builtin-1", Description: "Generate Alice's conversational response.", Kind: "model", Lifecycle: "singleton", InputSchema: ConversationSchema, OutputSchema: ConversationSchema, BuiltIn: true, Protected: true}
}

func (c Chat) Execute(ctx context.Context, in component.Envelope) (component.Envelope, error) {
	state, err := decodeConversation(in)
	if err != nil {
		return component.Envelope{}, err
	}
	if c.Model == nil {
		return component.Envelope{}, fmt.Errorf("llm.chat has no model configured")
	}
	state.Reply, err = c.Model.Reply(ctx, state.Prompt)
	if err != nil {
		return component.Envelope{}, err
	}
	in.Payload, _ = json.Marshal(state)
	return in, nil
}

type FactProcess struct {
	Store *facts.Store
	Model StructuredChatModel
}

func (FactProcess) Descriptor() component.Descriptor {
	return component.Descriptor{ID: "memory.fact.process", Version: "builtin-1", Description: "Extract conservative explicit fact candidates from a completed conversation turn.", Kind: "transform", Lifecycle: "singleton", InputSchema: ConversationSchema, OutputSchema: ConversationSchema, BuiltIn: true, Protected: true, SideEffects: []string{"fact_write"}}
}

func (c FactProcess) Execute(ctx context.Context, in component.Envelope) (component.Envelope, error) {
	state, err := decodeConversation(in)
	if err != nil {
		return component.Envelope{}, err
	}
	candidates := explicitFactCandidates(state.Input.Text, in.InputID)
	strategy := "coexist"
	if c.Model != nil && c.Model.Configured() {
		decision, decisionErr := decideFacts(ctx, c.Model, state, in.InputID)
		if decisionErr != nil {
			state.MemoryDecision = MemoryDecision{Action: "fallback", Error: decisionErr.Error()}
		} else {
			state.MemoryDecision = MemoryDecision{Action: decision.Action, Strategy: decision.Strategy, Question: decision.Question}
			candidates = decision.Facts
			strategy = decision.Strategy
			if decision.Action == "ignore" {
				candidates = nil
			}
			if decision.Action == "ask" {
				candidates = nil
				if strings.TrimSpace(decision.Question) != "" {
					state.Reply += "\n\n为了正确保存这条记忆，我需要确认：" + decision.Question
				}
			}
		}
	}
	for _, candidate := range candidates {
		fact, created, err := c.Store.AddContext(ctx, candidate, strategy)
		if err != nil {
			return component.Envelope{}, err
		}
		if created {
			state.CreatedFacts = append(state.CreatedFacts, fact)
		}
	}
	in.Payload, _ = json.Marshal(state)
	return in, nil
}

type factDecision struct {
	Action   string       `json:"action"`
	Strategy string       `json:"strategy"`
	Question string       `json:"question"`
	Facts    []facts.Fact `json:"facts"`
}

func decideFacts(ctx context.Context, model StructuredChatModel, state Conversation, sourceID string) (factDecision, error) {
	system := `你是 Alice 的事实记忆决策组件。只保存未来可能有用、可独立陈述的事实，不保存闲聊、推测、问题或助手生成的幻觉。冲突时根据语义选择 replace、coexist 或 ask。必须只输出 JSON。`
	prompt := `根据本轮对话决定记忆动作。JSON 格式：{"action":"commit|ignore|ask","strategy":"replace|coexist|ask","question":"","facts":[{"subject":"user","predicate":"...","object":"...","confidence":1,"sensitivity":"normal","tags":[]}]}。
用户输入：` + state.Input.Text + `\n助手回复：` + state.Reply
	raw, err := model.Chat(ctx, system, prompt, true)
	if err != nil {
		return factDecision{}, err
	}
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	var d factDecision
	if err = json.Unmarshal([]byte(strings.TrimSpace(raw)), &d); err != nil {
		return d, fmt.Errorf("decode fact decision: %w", err)
	}
	if d.Action == "" {
		d.Action = "ignore"
	}
	if d.Strategy == "" {
		d.Strategy = "coexist"
	}
	for i := range d.Facts {
		f := &d.Facts[i]
		if f.Subject == "" {
			f.Subject = "user"
		}
		if f.SourceKind == "" {
			f.SourceKind = "model_extracted"
		}
		f.SourceIDs = []string{sourceID}
		if f.AssertedBy == "" {
			f.AssertedBy = "user"
		}
		if f.Confidence == 0 {
			f.Confidence = .8
		}
		if f.ValidFrom == 0 {
			f.ValidFrom = time.Now().UnixMilli()
		}
		if f.Status == "" {
			f.Status = "active"
		}
	}
	return d, nil
}

type ReplyOutput struct{}

func (ReplyOutput) Descriptor() component.Descriptor {
	return component.Descriptor{ID: "output.reply", Version: "builtin-1", Description: "Expose the generated reply to the originating input adapter.", Kind: "output", Lifecycle: "per_call", InputSchema: ConversationSchema, OutputSchema: ConversationSchema, BuiltIn: true, Protected: true}
}

func (ReplyOutput) Execute(_ context.Context, in component.Envelope) (component.Envelope, error) {
	if _, err := decodeConversation(in); err != nil {
		return component.Envelope{}, err
	}
	return in, nil
}

type TaskResultFacts struct{ Store *facts.Store }

func (TaskResultFacts) Descriptor() component.Descriptor {
	return component.Descriptor{ID: "task.result.fact", Version: "builtin-1", Description: "Basic replaceable decision component for committing useful Task completion results as facts.", Kind: "transform", Lifecycle: "singleton", InputSchema: "alice.task_completion.v1", OutputSchema: "alice.task_completion.v1", BuiltIn: true, Protected: true, SideEffects: []string{"fact_write"}}
}

func (c TaskResultFacts) Execute(_ context.Context, in component.Envelope) (component.Envelope, error) {
	var result TaskCompletion
	if err := json.Unmarshal(in.Payload, &result); err != nil {
		return component.Envelope{}, fmt.Errorf("decode task completion: %w", err)
	}
	if result.Status != "completed" || !result.Remember || strings.TrimSpace(result.Result) == "" {
		return in, nil
	}
	_, _, err := c.Store.Add(facts.Fact{
		Subject:     "task:" + result.TaskID,
		Predicate:   "completed_with_result",
		Object:      result.Result,
		AssertedBy:  "alice",
		SourceKind:  "task_result",
		SourceIDs:   []string{result.ExecutionID},
		Confidence:  1,
		ValidFrom:   result.FinishedAt,
		Sensitivity: "normal",
		Status:      "active",
		Tags:        []string{"task_result"},
	})
	return in, err
}

func decodeConversation(in component.Envelope) (Conversation, error) {
	var state Conversation
	if err := json.Unmarshal(in.Payload, &state); err != nil {
		return Conversation{}, fmt.Errorf("decode conversation: %w", err)
	}
	if strings.TrimSpace(state.Input.Text) == "" {
		return Conversation{}, fmt.Errorf("conversation input text is empty")
	}
	return state, nil
}

func explicitFactCandidates(text, sourceID string) []facts.Fact {
	text = strings.TrimSpace(strings.TrimRight(text, "。.!！?？"))
	if text == "" || strings.HasPrefix(text, "如果") || strings.HasPrefix(text, "假如") || strings.Contains(text, "吗") {
		return nil
	}
	type pattern struct {
		prefix    string
		predicate string
		tags      []string
	}
	patterns := []pattern{
		{prefix: "我不喜欢", predicate: "dislikes", tags: []string{"preference"}},
		{prefix: "我喜欢", predicate: "likes", tags: []string{"preference"}},
		{prefix: "我叫", predicate: "is_named", tags: []string{"identity"}},
		{prefix: "记住", predicate: "states", tags: []string{"explicit_memory"}},
	}
	for _, p := range patterns {
		if !strings.HasPrefix(text, p.prefix) {
			continue
		}
		object := strings.TrimSpace(strings.TrimPrefix(text, p.prefix))
		if p.predicate == "dislikes" || p.predicate == "likes" {
			object = strings.TrimPrefix(object, "吃")
		}
		if object == "" {
			return nil
		}
		return []facts.Fact{{
			Subject:     "user",
			Predicate:   p.predicate,
			Object:      object,
			AssertedBy:  "user",
			SourceKind:  "explicit_statement",
			SourceIDs:   []string{sourceID},
			Confidence:  1,
			ValidFrom:   time.Now().UnixMilli(),
			Sensitivity: "normal",
			Status:      "active",
			Tags:        p.tags,
		}}
	}
	return nil
}
