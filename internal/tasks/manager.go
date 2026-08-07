package tasks

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"alice/internal/builtin"
	"alice/internal/core"
	"alice/pkg/component"
)

type Trigger struct {
	Type string `json:"type"` // manual | at
	At   int64  `json:"at,omitempty"`
}

type Task struct {
	ID               string             `json:"id"`
	Label            string             `json:"label,omitempty"`
	BlueprintID      string             `json:"blueprint_id"`
	BlueprintVersion int                `json:"blueprint_version,omitempty"`
	Trigger          Trigger            `json:"trigger"`
	Input            component.Envelope `json:"input"`
	Status           string             `json:"status"`
	ExecutionID      string             `json:"execution_id,omitempty"`
	Result           string             `json:"result,omitempty"`
	Error            string             `json:"error,omitempty"`
	RememberResult   bool               `json:"remember_result,omitempty"`
	CreatedAt        int64              `json:"created_at"`
	StartedAt        int64              `json:"started_at,omitempty"`
	FinishedAt       int64              `json:"finished_at,omitempty"`
}

type Manager struct {
	mu       sync.RWMutex
	path     string
	engine   *core.Engine
	registry *core.Registry
	tasks    map[string]*Task
	stop     chan struct{}
	done     chan struct{}
	onChange func([]Task) error
}

func (m *Manager) Restore(stored []Task) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tasks = make(map[string]*Task, len(stored))
	for _, task := range stored {
		if task.Status == "running" {
			task.Status = "waiting"
			task.Error = "recovered after Alice restart"
		}
		copy := task
		m.tasks[task.ID] = &copy
	}
}
func (m *Manager) SetOnChange(fn func([]Task) error) { m.mu.Lock(); m.onChange = fn; m.mu.Unlock() }
func (m *Manager) Persist() error                    { m.mu.Lock(); defer m.mu.Unlock(); return m.saveLocked() }

func NewManager(path string, engine *core.Engine, registry *core.Registry) (*Manager, error) {
	m := &Manager{path: path, engine: engine, registry: registry, tasks: make(map[string]*Task), stop: make(chan struct{}), done: make(chan struct{})}
	if err := m.load(); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *Manager) Start() {
	go func() {
		defer close(m.done)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				m.triggerDue()
			case <-m.stop:
				return
			}
		}
	}()
}

func (m *Manager) Close() {
	select {
	case <-m.stop:
		return
	default:
		close(m.stop)
		<-m.done
	}
}

func (m *Manager) Create(task Task) (Task, error) {
	if task.BlueprintID == "" {
		return Task{}, fmt.Errorf("task blueprint_id is required")
	}
	if task.Trigger.Type == "" {
		task.Trigger.Type = "manual"
	}
	if task.Trigger.Type != "manual" && task.Trigger.Type != "at" {
		return Task{}, fmt.Errorf("unsupported trigger type %q", task.Trigger.Type)
	}
	if task.Trigger.Type == "at" && task.Trigger.At == 0 {
		return Task{}, fmt.Errorf("at trigger requires a timestamp")
	}
	if task.ID == "" {
		task.ID = core.NewID("task")
	}
	task.Status = "waiting"
	task.CreatedAt = time.Now().UnixMilli()
	if task.Input.TraceID == "" {
		task.Input.TraceID = core.NewID("trace")
	}
	m.mu.Lock()
	if _, exists := m.tasks[task.ID]; exists {
		m.mu.Unlock()
		return Task{}, fmt.Errorf("task %s already exists", task.ID)
	}
	copy := task
	m.tasks[task.ID] = &copy
	err := m.saveLocked()
	m.mu.Unlock()
	if err != nil {
		return Task{}, err
	}
	return task, nil
}

func (m *Manager) Trigger(id string) error {
	m.mu.Lock()
	task := m.tasks[id]
	if task == nil {
		m.mu.Unlock()
		return fmt.Errorf("task %s not found", id)
	}
	if task.Status != "waiting" {
		m.mu.Unlock()
		return fmt.Errorf("task %s is %s, not waiting", id, task.Status)
	}
	task.Status = "running"
	task.StartedAt = time.Now().UnixMilli()
	_ = m.saveLocked()
	copy := *task
	m.mu.Unlock()
	go m.run(copy)
	return nil
}

func (m *Manager) triggerDue() {
	now := time.Now().UnixMilli()
	m.mu.RLock()
	var due []string
	for id, task := range m.tasks {
		if task.Status == "waiting" && task.Trigger.Type == "at" && task.Trigger.At <= now {
			due = append(due, id)
		}
	}
	m.mu.RUnlock()
	for _, id := range due {
		_ = m.Trigger(id)
	}
}

func (m *Manager) run(task Task) {
	snapshot, err := m.engine.Start(context.Background(), task.BlueprintID, task.BlueprintVersion, "task:"+task.ID, task.Input)
	finished := time.Now().UnixMilli()
	result := resultText(snapshot.Result)
	m.mu.Lock()
	current := m.tasks[task.ID]
	if current == nil {
		m.mu.Unlock()
		return
	}
	current.ExecutionID = snapshot.ID
	current.FinishedAt = finished
	current.Result = result
	if err != nil {
		current.Status = "failed"
		current.Error = err.Error()
	} else {
		current.Status = "completed"
	}
	_ = m.saveLocked()
	completion := builtin.TaskCompletion{TaskID: current.ID, Label: current.Label, Status: current.Status, Result: current.Result, ExecutionID: current.ExecutionID, FinishedAt: current.FinishedAt, Remember: current.RememberResult}
	m.mu.Unlock()

	// Task management owns the completion-to-fact decision handoff. The default
	// component is basic and replaceable; later model versions can ask, merge or ignore.
	if processor, resolveErr := m.registry.Resolve("task.result.fact", ""); resolveErr == nil {
		payload, _ := json.Marshal(completion)
		_, _ = processor.Execute(context.Background(), component.Envelope{TraceID: task.Input.TraceID, ExecutionID: snapshot.ID, Schema: "alice.task_completion.v1", Payload: payload})
	}
}

func (m *Manager) Get(id string) (Task, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	task := m.tasks[id]
	if task == nil {
		return Task{}, false
	}
	return *task, true
}

func (m *Manager) List() []Task {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Task, 0, len(m.tasks))
	for _, task := range m.tasks {
		out = append(out, *task)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt > out[j].CreatedAt })
	return out
}

func (m *Manager) load() error {
	if m.path == "" {
		return nil
	}
	b, err := os.ReadFile(m.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var stored []Task
	if err := json.Unmarshal(b, &stored); err != nil {
		return fmt.Errorf("decode tasks: %w", err)
	}
	for i := range stored {
		task := stored[i]
		if task.Status == "running" {
			task.Status = "waiting"
			task.Error = "recovered after Alice restart"
		}
		copy := task
		m.tasks[task.ID] = &copy
	}
	return nil
}

func (m *Manager) saveLocked() error {
	stored := make([]Task, 0, len(m.tasks))
	for _, task := range m.tasks {
		stored = append(stored, *task)
	}
	if m.onChange != nil {
		if err := m.onChange(stored); err != nil {
			return err
		}
	}
	if m.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(m.path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(stored, "", "  ")
	if err != nil {
		return err
	}
	tmp := m.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, m.path)
}

func resultText(envelope component.Envelope) string {
	var conversation builtin.Conversation
	if json.Unmarshal(envelope.Payload, &conversation) == nil && strings.TrimSpace(conversation.Reply) != "" {
		return conversation.Reply
	}
	return strings.TrimSpace(string(envelope.Payload))
}
