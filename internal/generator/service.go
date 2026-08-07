package generator

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"alice/internal/dynamic"
)

type Model interface {
	Chat(context.Context, string, string, bool) (string, error)
	Configured() bool
}
type Service struct {
	Model       Model
	Components  dynamic.RuntimeManager
	MaxAttempts int
}
type proposal struct {
	Manifest dynamic.Manifest `json:"manifest"`
	MainGo   string           `json:"main_go"`
}

func (s *Service) Create(ctx context.Context, request string) (string, error) {
	if s.Model == nil || !s.Model.Configured() {
		return "要由 Alice 自动生成 Go 组件，请先在管理页面配置对话模型。当前 Component Host 已就绪，也可以先通过“构建 Go 动态组件”编译已有源码。", nil
	}
	attempts := s.MaxAttempts
	if attempts <= 0 {
		attempts = 3
	}
	feedback := ""
	for attempt := 1; attempt <= attempts; attempt++ {
		prompt := generationPrompt(request, feedback)
		raw, err := s.Model.Chat(ctx, systemPrompt, prompt, true)
		if err != nil {
			return "", err
		}
		p, err := decodeProposal(raw)
		if err == nil {
			var manifest dynamic.Manifest
			manifest, err = s.Components.BuildSource(ctx, dynamic.GeneratedSource{Manifest: p.Manifest, MainGo: p.MainGo, Activate: true})
			if err == nil {
				return fmt.Sprintf("组件 %s@%s 已生成、编译并热激活。源码位于 components/generated/%s。", manifest.Descriptor.ID, manifest.Descriptor.Version, safeName(manifest.Descriptor.ID)), nil
			}
		}
		feedback = err.Error()
	}
	return "", fmt.Errorf("组件在 %d 次生成/编译尝试后仍失败：%s", attempts, feedback)
}

const systemPrompt = `你是 Alice Core 内置的 Go 组件工程师。你只能生成符合 alice/pkg/component 稳定协议的独立 stdio 组件。只输出一个 JSON 对象，不要 Markdown。组件必须使用 Go 标准库和 alice/pkg/component；入口必须调用 component.ServeStdio(context.Background(), componentInstance, os.Stdin, os.Stdout)。不得访问 Alice Core 内部包。`

func generationPrompt(request, feedback string) string {
	prompt := `为下面需求生成一个可编译组件。JSON 必须是：{"manifest":{"descriptor":{"id":"lower.dotted.id","version":"1.0.0","description":"...","kind":"transform","lifecycle":"per_call","input_schema":"...","output_schema":"..."}},"main_go":"完整 Go 源码"}。
需求：` + request
	if feedback != "" {
		prompt += "\n上一次失败，请修复：" + feedback
	}
	return prompt
}
func decodeProposal(raw string) (proposal, error) {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	var p proposal
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &p); err != nil {
		return p, fmt.Errorf("decode generated component: %w", err)
	}
	if p.Manifest.Descriptor.ID == "" || p.Manifest.Descriptor.Version == "" || strings.TrimSpace(p.MainGo) == "" {
		return p, fmt.Errorf("generated component JSON is incomplete")
	}
	return p, nil
}
func safeName(value string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, value)
}
