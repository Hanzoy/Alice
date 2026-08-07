package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"alice/internal/builtin"
	"alice/internal/tasks"
	"alice/pkg/component"
)

func TestChatCreatesExplicitFactAndCoreOperatorCanInspectIt(t *testing.T) {
	requireIntegrationDatabase(t)
	root := t.TempDir()
	a, err := New(filepath.Join(root, "data"), filepath.Join(root, "components"))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	conversation, execution, err := a.Chat(context.Background(), "我不喜欢吃香菜", "test", "")
	if err != nil {
		t.Fatal(err)
	}
	if conversation.Reply == "" || execution.Status != "completed" {
		t.Fatalf("unexpected chat result: conversation=%+v execution=%+v", conversation, execution)
	}
	if len(conversation.CreatedFacts) != 1 || conversation.CreatedFacts[0].Object != "香菜" {
		t.Fatalf("explicit fact not created: %+v", conversation.CreatedFacts)
	}
	reply, _, err := a.CoreChat(context.Background(), "查看事实记忆")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reply, "香菜") {
		t.Fatalf("core operator did not expose fact: %s", reply)
	}
}

func TestManualTaskUsesExecutionAndReportsCompletion(t *testing.T) {
	requireIntegrationDatabase(t)
	root := t.TempDir()
	a, err := New(filepath.Join(root, "data"), filepath.Join(root, "components"))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	payload, _ := json.Marshal(builtin.MessageInput{Text: "执行一次测试任务", Source: "task"})
	task, err := a.Tasks.Create(tasks.Task{Label: "test", BlueprintID: "alice.chat.default", Trigger: tasks.Trigger{Type: "manual"}, Input: component.Envelope{Schema: "alice.message.v1", Payload: payload}, RememberResult: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Tasks.Trigger(task.ID); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		current, _ := a.Tasks.Get(task.ID)
		if current.Status == "completed" {
			if current.ExecutionID == "" || current.Result == "" {
				t.Fatalf("completed task missing execution result: %+v", current)
			}
			return
		}
		if current.Status == "failed" {
			t.Fatalf("task failed: %+v", current)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("task did not complete")
}

func requireIntegrationDatabase(t *testing.T) {
	t.Helper()
	url := os.Getenv("ALICE_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set ALICE_TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	t.Setenv("ALICE_DATABASE_URL", url)
	t.Setenv("ALICE_EMBEDDING_BASE_URL", "")
	t.Setenv("ALICE_EMBEDDING_MODEL", "")
}
