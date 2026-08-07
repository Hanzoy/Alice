package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"alice/internal/builtin"
	"alice/internal/core"
	"alice/internal/dynamic"
	"alice/internal/facts"
	"alice/internal/generator"
	"alice/internal/memory"
	"alice/internal/models"
	"alice/internal/storage"
	"alice/internal/tasks"
	"alice/internal/vector"
	"alice/pkg/component"
)

type App struct {
	Registry   *core.Registry
	Blueprints *core.BlueprintStore
	Engine     *core.Engine
	Events     *core.EventLog
	Facts      *facts.Store
	Tasks      *tasks.Manager
	Dynamic    dynamic.RuntimeManager
	DB         *storage.DB
	Models     *models.Runtime
	Vectors    *vector.Store
	Memory     *memory.Retriever
	Indexer    *vector.Indexer
	dataDir    string
}

func New(dataDir, componentSourceRoot string) (*App, error) {
	if dataDir == "" {
		dataDir = "data"
	}
	if componentSourceRoot == "" {
		componentSourceRoot = "components"
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	databaseURL := os.Getenv("ALICE_DATABASE_URL")
	if databaseURL == "" {
		databaseURL = "postgres://alice:alice@localhost:5432/alice?sslmode=disable"
	}
	db, err := storage.Open(ctx, databaseURL)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*App, error) { db.Close(); return nil, err }
	modelRuntime, err := models.New(ctx, db, filepath.Join(dataDir, "master.key"))
	if err != nil {
		return fail(err)
	}
	qPort, _ := strconv.Atoi(os.Getenv("ALICE_QDRANT_PORT"))
	vectors, err := vector.New(vector.Config{Host: envDefault("ALICE_QDRANT_HOST", "localhost"), Port: qPort, APIKey: os.Getenv("ALICE_QDRANT_API_KEY"), UseTLS: os.Getenv("ALICE_QDRANT_TLS") == "true"}, modelRuntime)
	if err != nil {
		return fail(err)
	}
	factStore := facts.NewPostgresStore(db.Pool)
	factStore.OnCommit = func(ctx context.Context, f facts.Fact) error { return vectors.Upsert(ctx, f.ID, f.EmbeddingText()) }
	retriever := &memory.Retriever{Facts: factStore, Vectors: vectors}
	registry := core.NewRegistry()
	type blueprintState struct {
		Blueprints []core.Blueprint `json:"blueprints"`
		Active     map[string]int   `json:"active"`
	}
	blueprints := core.NewBlueprintStore()
	var storedBlueprints blueprintState
	if ok, loadErr := db.GetSetting(ctx, "core.blueprints", &storedBlueprints); loadErr != nil {
		vectors.Close()
		return fail(loadErr)
	} else if ok {
		if err = blueprints.Restore(storedBlueprints.Blueprints, storedBlueprints.Active); err != nil {
			vectors.Close()
			return fail(err)
		}
	} else if legacy, legacyErr := core.NewBlueprintStoreAt(filepath.Join(dataDir, "blueprints.json")); legacyErr != nil {
		vectors.Close()
		return fail(legacyErr)
	} else {
		if err = blueprints.Restore(legacy.List(), legacy.ActiveVersions()); err != nil {
			vectors.Close()
			return fail(err)
		}
	}
	blueprints.SetOnChange(func(items []core.Blueprint, active map[string]int) error {
		return db.PutSetting(context.Background(), "core.blueprints", blueprintState{Blueprints: items, Active: active})
	})
	events := core.NewEventLog(filepath.Join(dataDir, "timeline.jsonl"))
	engine := core.NewEngine(registry, blueprints, events)
	engine.SetSnapshotSink(func(s core.ExecutionSnapshot) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = db.SaveExecution(ctx, s)
	})
	a := &App{Registry: registry, Blueprints: blueprints, Engine: engine, Events: events, Facts: factStore, DB: db, Models: modelRuntime, Vectors: vectors, Memory: retriever, dataDir: dataDir}
	var factsMigrated bool
	if ok, _ := db.GetSetting(ctx, "migration.facts_json_v1", &factsMigrated); !ok || !factsMigrated {
		if _, importErr := factStore.ImportJSON(ctx, filepath.Join(dataDir, "facts.json")); importErr != nil {
			vectors.Close()
			return fail(importErr)
		}
		_ = db.PutSetting(ctx, "migration.facts_json_v1", true)
	}
	builtins := []component.Component{builtin.Normalize{}, builtin.FactQuery{Retriever: retriever}, builtin.ContextAssembler{}, builtin.Chat{Model: modelRuntime}, builtin.FactProcess{Store: factStore, Model: modelRuntime}, builtin.ReplyOutput{}, builtin.TaskResultFacts{Store: factStore}}
	for _, c := range builtins {
		if err := registry.Register(c); err != nil {
			vectors.Close()
			return fail(err)
		}
	}
	var dynamicManager dynamic.RuntimeManager
	if hostURL := os.Getenv("ALICE_COMPONENT_HOST_URL"); hostURL != "" {
		dynamicManager = dynamic.NewRemoteManager(registry, hostURL, os.Getenv("ALICE_COMPONENT_HOST_TOKEN"))
	} else {
		dynamicManager = dynamic.NewManager(registry, componentSourceRoot, filepath.Join(dataDir, "components", "bin"), filepath.Join(dataDir, "components", "installed.json"))
	}
	a.Dynamic = dynamicManager
	if err := registry.Register(dynamic.BuilderComponent{Manager: dynamicManager}); err != nil {
		vectors.Close()
		return fail(err)
	}
	if err := dynamicManager.Load(); err != nil {
		vectors.Close()
		return fail(fmt.Errorf("load dynamic components: %w", err))
	}
	componentGenerator := &generator.Service{Model: modelRuntime, Components: dynamicManager, MaxAttempts: 3}
	if err := registry.Register(builtin.Operator{Snapshot: a.Snapshot, Model: modelRuntime, CreateComponent: componentGenerator.Create}); err != nil {
		vectors.Close()
		return fail(err)
	}
	if err := a.publishBuiltInBlueprints(); err != nil {
		vectors.Close()
		return fail(err)
	}
	if err := blueprints.Persist(); err != nil {
		vectors.Close()
		return fail(err)
	}
	var storedTasks []tasks.Task
	tasksInDB, loadTasksErr := db.GetSetting(ctx, "core.tasks", &storedTasks)
	if loadTasksErr != nil {
		vectors.Close()
		return fail(loadTasksErr)
	}
	taskPath := filepath.Join(dataDir, "tasks.json")
	if tasksInDB {
		taskPath = ""
	}
	taskManager, err := tasks.NewManager(taskPath, engine, registry)
	if err != nil {
		vectors.Close()
		return fail(err)
	}
	if tasksInDB {
		taskManager.Restore(storedTasks)
	}
	taskManager.SetOnChange(func(items []tasks.Task) error { return db.PutSetting(context.Background(), "core.tasks", items) })
	if err = taskManager.Persist(); err != nil {
		vectors.Close()
		return fail(err)
	}
	a.Tasks = taskManager
	taskManager.Start()
	indexer := vector.NewIndexer(db, factStore, vectors)
	a.Indexer = indexer
	indexer.Start()
	return a, nil
}

func (a *App) Close() {
	if a.Tasks != nil {
		a.Tasks.Close()
	}
	if a.Indexer != nil {
		a.Indexer.Close()
	}
	if a.Vectors != nil {
		a.Vectors.Close()
	}
	if a.DB != nil {
		a.DB.Close()
	}
}

func (a *App) Chat(ctx context.Context, text, source, replyHandle string) (builtin.Conversation, core.ExecutionSnapshot, error) {
	historyRows, err := a.DB.RecentMessages(ctx, 20)
	if err != nil {
		return builtin.Conversation{}, core.ExecutionSnapshot{}, err
	}
	history := make([]builtin.ConversationMessage, 0, len(historyRows))
	for _, m := range historyRows {
		history = append(history, builtin.ConversationMessage{Role: m.Role, Content: m.Content, CreatedAt: m.CreatedAt})
	}
	inputID := core.NewID("input")
	messageID := core.NewID("msg")
	if err = a.DB.AddMessage(ctx, storage.Message{ID: messageID, Role: "user", Source: source, ReplyHandle: replyHandle, Content: text, InputID: inputID}); err != nil {
		return builtin.Conversation{}, core.ExecutionSnapshot{}, err
	}
	payload, _ := json.Marshal(builtin.MessageInput{Text: text, Source: source, ReplyHandle: replyHandle, History: history})
	snapshot, runErr := a.Engine.Start(ctx, "alice.chat.default", 0, source, component.Envelope{InputID: inputID, Source: source, ReplyHandle: replyHandle, Schema: "alice.message.v1", Payload: payload})
	if runErr != nil {
		return builtin.Conversation{}, snapshot, runErr
	}
	var conversation builtin.Conversation
	if err = json.Unmarshal(snapshot.Result.Payload, &conversation); err != nil {
		return builtin.Conversation{}, snapshot, err
	}
	_ = a.DB.AddMessage(ctx, storage.Message{ID: core.NewID("msg"), Role: "assistant", Source: "alice", ReplyHandle: replyHandle, Content: conversation.Reply, InputID: inputID, ExecutionID: snapshot.ID})
	return conversation, snapshot, nil
}

func (a *App) CoreChat(ctx context.Context, text string) (string, core.ExecutionSnapshot, error) {
	payload, _ := json.Marshal(map[string]string{"text": text})
	snapshot, err := a.Engine.Start(ctx, "alice.core.dialogue", 0, "core.management", component.Envelope{Source: "core.management", Schema: "alice.core_command.v1", Payload: payload})
	if err != nil {
		return "", snapshot, err
	}
	var result map[string]string
	if err = json.Unmarshal(snapshot.Result.Payload, &result); err != nil {
		return "", snapshot, err
	}
	return result["reply"], snapshot, nil
}
func (a *App) BuildComponent(ctx context.Context, sourceDir string, activate bool) (dynamic.Manifest, core.ExecutionSnapshot, error) {
	payload, _ := json.Marshal(map[string]any{"source_dir": sourceDir, "activate": activate})
	snapshot, err := a.Engine.Start(ctx, "alice.component.build", 0, "core.management", component.Envelope{Source: "core.management", Schema: "alice.component_build.v1", Payload: payload})
	if err != nil {
		return dynamic.Manifest{}, snapshot, err
	}
	var manifest dynamic.Manifest
	if err = json.Unmarshal(snapshot.Result.Payload, &manifest); err != nil {
		return dynamic.Manifest{}, snapshot, err
	}
	return manifest, snapshot, nil
}

func (a *App) Snapshot() map[string]any {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	tasksList := []tasks.Task{}
	if a.Tasks != nil {
		tasksList = a.Tasks.List()
	}
	return map[string]any{"components": a.Registry.List(), "blueprints": a.Blueprints.List(), "blueprint_active": a.Blueprints.ActiveVersions(), "executions": a.DB.ListExecutions(ctx, 100), "tasks": tasksList, "facts": a.Facts.List(), "events": a.Events.List(200), "toolchain": a.Dynamic.Toolchain(), "services": map[string]any{"postgres": a.DB.Status(ctx), "qdrant": a.Vectors.Status(ctx)}, "model": a.Models.PublicConfig(), "model_runtime": a.Models.Status(), "generated_at": time.Now().UnixMilli()}
}

func (a *App) publishBuiltInBlueprints() error {
	now := time.Now().UnixMilli()
	blueprints := []core.Blueprint{{ID: "alice.chat.default", Version: 1, Name: "默认对话", Description: "输入标准化、混合事实召回、上下文组装、模型交流、事实决策和回复输出。", CreatedAt: now, Nodes: []core.Node{{ID: "normalize", ComponentID: "message.normalize"}, {ID: "recall", ComponentID: "memory.fact.query"}, {ID: "context", ComponentID: "context.assemble"}, {ID: "chat", ComponentID: "llm.chat", Config: json.RawMessage(`{"timeout_ms":90000,"max_attempts":2}`)}, {ID: "facts", ComponentID: "memory.fact.process"}, {ID: "output", ComponentID: "output.reply"}}, Edges: []core.Edge{{From: "normalize", To: "recall"}, {From: "recall", To: "context"}, {From: "context", To: "chat"}, {From: "chat", To: "facts"}, {From: "facts", To: "output"}}}, {ID: "alice.core.dialogue", Version: 1, Name: "Core 管理对话", Description: "直接查询和管理 Alice Core 的内置入口。", CreatedAt: now, Nodes: []core.Node{{ID: "operator", ComponentID: "core.operator"}}}, {ID: "alice.component.build", Version: 1, Name: "Go 组件构建", Description: "编译、注册并可选激活 Go stdio 组件。", CreatedAt: now, Nodes: []core.Node{{ID: "build", ComponentID: "component.build.go", Config: json.RawMessage(`{"timeout_ms":120000}`)}}}}
	for _, bp := range blueprints {
		if _, err := a.Blueprints.Get(bp.ID, bp.Version); err == nil {
			continue
		}
		if err := a.Blueprints.Publish(bp, true); err != nil {
			return err
		}
	}
	return nil
}
func envDefault(key, value string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return value
}
