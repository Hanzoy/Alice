package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"alice/pkg/component"
)

type Operator struct {
	Snapshot        func() map[string]any
	Model           ChatModel
	CreateComponent func(context.Context, string) (string, error)
}

func (Operator) Descriptor() component.Descriptor {
	return component.Descriptor{ID: "core.operator", Version: "builtin-1", Description: "Built-in Reasonix-like operator for inspecting and managing Alice Core.", Kind: "model", Lifecycle: "singleton", InputSchema: "alice.core_command.v1", OutputSchema: "alice.core_reply.v1", BuiltIn: true, Protected: true, SideEffects: []string{"core_management"}}
}

func (o Operator) Execute(ctx context.Context, input component.Envelope) (component.Envelope, error) {
	var request struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(input.Payload, &request); err != nil {
		return component.Envelope{}, fmt.Errorf("decode core operator request: %w", err)
	}
	request.Text = strings.TrimSpace(request.Text)
	if request.Text == "" {
		return component.Envelope{}, fmt.Errorf("core operator text is required")
	}
	snapshot := map[string]any{}
	if o.Snapshot != nil {
		snapshot = o.Snapshot()
	}
	var reply string
	var err error
	if o.CreateComponent != nil && (strings.Contains(request.Text, "创建组件") || strings.Contains(request.Text, "生成组件") || strings.Contains(request.Text, "新增组件")) {
		reply, err = o.CreateComponent(ctx, request.Text)
		if err != nil {
			return component.Envelope{}, err
		}
	}
	if reply == "" {
		reply = o.ruleReply(request.Text, snapshot)
	}
	if reply == "" && o.Model != nil {
		view, _ := json.MarshalIndent(snapshot, "", "  ")
		prompt := "你正在通过 Alice Core 管理页面回答系统管理问题。以下是当前系统快照：\n" + string(view) + "\n\n用户请求：" + request.Text
		var err error
		reply, err = o.Model.Reply(ctx, prompt)
		if err != nil {
			return component.Envelope{}, err
		}
	}
	if reply == "" {
		reply = "Core Operator 已就绪。你可以查询组件、蓝图、Execution、Task、事实和运行事件，也可以要求 Alice 生成新的 Go 组件。"
	}
	input.Schema = "alice.core_reply.v1"
	input.Payload, _ = json.Marshal(map[string]string{"reply": reply})
	return input, nil
}

func (o Operator) ruleReply(text string, snapshot map[string]any) string {
	var key, label string
	switch {
	case strings.Contains(text, "组件"):
		key, label = "components", "组件"
	case strings.Contains(text, "蓝图") || strings.Contains(strings.ToLower(text), "blueprint"):
		key, label = "blueprints", "蓝图"
	case strings.Contains(strings.ToLower(text), "execution") || strings.Contains(text, "执行"):
		key, label = "executions", "Execution"
	case strings.Contains(strings.ToLower(text), "task") || strings.Contains(text, "任务"):
		key, label = "tasks", "Task"
	case strings.Contains(text, "事实") || strings.Contains(text, "记忆"):
		key, label = "facts", "事实"
	case strings.Contains(text, "事件") || strings.Contains(strings.ToLower(text), "event"):
		key, label = "events", "事件"
	default:
		return ""
	}
	b, _ := json.MarshalIndent(snapshot[key], "", "  ")
	return label + "当前状态：\n" + string(b)
}
