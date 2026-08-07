package models

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestDeepSeekChatParametersJSONRetryAndCacheUsage(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/chat/completions" {
			t.Fatalf("unexpected path: %s", req.URL.Path)
		}
		if req.Header.Get("Authorization") != "Bearer test-key" {
			t.Fatalf("missing bearer token")
		}
		var body map[string]any
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["model"] != DeepSeekFlashModel {
			t.Fatalf("unexpected model: %v", body["model"])
		}
		thinking, ok := body["thinking"].(map[string]any)
		if !ok || thinking["type"] != "enabled" || body["reasoning_effort"] != "max" {
			t.Fatalf("DeepSeek thinking fields missing: %#v", body)
		}
		if _, ok = body["response_format"]; !ok || body["max_tokens"] != float64(4096) {
			t.Fatalf("JSON mode fields missing: %#v", body)
		}
		messages := body["messages"].([]any)
		system := messages[0].(map[string]any)["content"].(string)
		if !strings.Contains(strings.ToLower(system), "json") {
			t.Fatalf("system prompt must contain json: %s", system)
		}

		w.Header().Set("Content-Type", "application/json")
		if calls.Add(1) == 1 {
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":""}}],"usage":{"prompt_tokens":20,"completion_tokens":2,"total_tokens":22,"prompt_cache_hit_tokens":10,"prompt_cache_miss_tokens":10,"completion_tokens_details":{"reasoning_tokens":2}}}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"status\":\"ok\"}"}}],"usage":{"prompt_tokens":10,"completion_tokens":4,"total_tokens":14,"prompt_cache_hit_tokens":5,"prompt_cache_miss_tokens":5,"completion_tokens_details":{"reasoning_tokens":1}}}`))
	}))
	defer server.Close()

	runtime := &Runtime{
		config: Config{Provider: ProviderDeepSeek, BaseURL: server.URL, ChatModel: DeepSeekFlashModel, ThinkingMode: "enabled", ReasoningEffort: "max", APIKey: "test-key"},
		client: server.Client(),
	}
	got, err := runtime.Chat(context.Background(), "输出结果", "测试", true)
	if err != nil {
		t.Fatal(err)
	}
	if got != `{"status":"ok"}` || calls.Load() != 2 {
		t.Fatalf("unexpected result=%q calls=%d", got, calls.Load())
	}
	status := runtime.Status()
	if status.Requests != 2 || status.TotalUsage.PromptCacheHitTokens != 15 || status.TotalUsage.ReasoningTokens != 3 || status.CacheHitRate != .5 {
		t.Fatalf("unexpected status: %#v", status)
	}
}

func TestDeepSeekDefaultsAndLegacyModelValidation(t *testing.T) {
	c := normalizeConfig(Config{})
	if c.Provider != ProviderDeepSeek || c.BaseURL != DeepSeekBaseURL || c.ChatModel != DeepSeekFlashModel || c.ThinkingMode != "enabled" || c.ReasoningEffort != "high" {
		t.Fatalf("unexpected defaults: %#v", c)
	}
	if embeddingURL(c) != "" {
		t.Fatal("DeepSeek must not be used as an embedding endpoint")
	}
	c.ChatModel = "deepseek-chat"
	if err := validateConfig(c); err == nil || !strings.Contains(err.Error(), "2026-07-24") {
		t.Fatalf("expected retired model error, got %v", err)
	}
	c = normalizeConfig(Config{Provider: "openai_compatible"})
	if err := validateConfig(c); err == nil || !strings.Contains(err.Error(), "只支持 DeepSeek") {
		t.Fatalf("expected DeepSeek-only provider error, got %v", err)
	}
}
