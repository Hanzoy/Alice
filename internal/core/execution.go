package core

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	"alice/pkg/component"
)

type ExecutionStatus string

const (
	ExecutionPending   ExecutionStatus = "pending"
	ExecutionRunning   ExecutionStatus = "running"
	ExecutionCompleted ExecutionStatus = "completed"
	ExecutionFailed    ExecutionStatus = "failed"
	ExecutionCancelled ExecutionStatus = "cancelled"
)

type NodeStatus string

const (
	NodePending   NodeStatus = "pending"
	NodeRunning   NodeStatus = "running"
	NodeCompleted NodeStatus = "completed"
	NodeFailed    NodeStatus = "failed"
	NodeSkipped   NodeStatus = "skipped"
)

type NodeRun struct {
	NodeID           string             `json:"node_id"`
	ComponentID      string             `json:"component_id"`
	ComponentVersion string             `json:"component_version,omitempty"`
	Status           NodeStatus         `json:"status"`
	Attempts         int                `json:"attempts"`
	StartedAt        int64              `json:"started_at,omitempty"`
	FinishedAt       int64              `json:"finished_at,omitempty"`
	Input            component.Envelope `json:"input"`
	Output           component.Envelope `json:"output"`
	Error            string             `json:"error,omitempty"`
}

type RuntimePatch struct {
	ID               string          `json:"id"`
	Operation        string          `json:"operation"`
	TargetNode       string          `json:"target_node"`
	ComponentID      string          `json:"component_id,omitempty"`
	ComponentVersion string          `json:"component_version,omitempty"`
	Config           json.RawMessage `json:"config,omitempty"`
	Reason           string          `json:"reason,omitempty"`
	AppliedAt        int64           `json:"applied_at"`
}

type ExecutionSnapshot struct {
	ID               string             `json:"id"`
	BlueprintID      string             `json:"blueprint_id"`
	BlueprintVersion int                `json:"blueprint_version"`
	Status           ExecutionStatus    `json:"status"`
	Source           string             `json:"source"`
	CreatedAt        int64              `json:"created_at"`
	StartedAt        int64              `json:"started_at,omitempty"`
	FinishedAt       int64              `json:"finished_at,omitempty"`
	Initial          component.Envelope `json:"initial"`
	Result           component.Envelope `json:"result"`
	Error            string             `json:"error,omitempty"`
	NodeRuns         []NodeRun          `json:"node_runs"`
	Patches          []RuntimePatch     `json:"patches"`
}

type execution struct {
	mu sync.RWMutex

	ID               string
	BlueprintID      string
	BlueprintVersion int
	Status           ExecutionStatus
	Source           string
	CreatedAt        int64
	StartedAt        int64
	FinishedAt       int64
	Initial          component.Envelope
	Result           component.Envelope
	Error            string

	nodes   map[string]Node
	edges   []Edge
	runs    map[string]*NodeRun
	skipped map[string]bool
	patches []RuntimePatch
	cancel  context.CancelFunc
}

func (e *execution) snapshot() ExecutionSnapshot {
	e.mu.RLock()
	defer e.mu.RUnlock()
	runs := make([]NodeRun, 0, len(e.runs))
	for _, run := range e.runs {
		runs = append(runs, *run)
	}
	sort.Slice(runs, func(i, j int) bool {
		if runs[i].StartedAt == runs[j].StartedAt {
			return runs[i].NodeID < runs[j].NodeID
		}
		return runs[i].StartedAt < runs[j].StartedAt
	})
	return ExecutionSnapshot{
		ID:               e.ID,
		BlueprintID:      e.BlueprintID,
		BlueprintVersion: e.BlueprintVersion,
		Status:           e.Status,
		Source:           e.Source,
		CreatedAt:        e.CreatedAt,
		StartedAt:        e.StartedAt,
		FinishedAt:       e.FinishedAt,
		Initial:          e.Initial,
		Result:           e.Result,
		Error:            e.Error,
		NodeRuns:         runs,
		Patches:          append([]RuntimePatch(nil), e.patches...),
	}
}

type Engine struct {
	registry       *Registry
	blueprints     *BlueprintStore
	events         *EventLog
	maxTransitions int

	mu           sync.RWMutex
	executions   map[string]*execution
	snapshotSink func(ExecutionSnapshot)
}

func (e *Engine) SetSnapshotSink(sink func(ExecutionSnapshot)) {
	e.mu.Lock()
	e.snapshotSink = sink
	e.mu.Unlock()
}

func NewEngine(registry *Registry, blueprints *BlueprintStore, events *EventLog) *Engine {
	return &Engine{
		registry:       registry,
		blueprints:     blueprints,
		events:         events,
		maxTransitions: 128,
		executions:     make(map[string]*execution),
	}
}

func (e *Engine) Start(ctx context.Context, blueprintID string, version int, source string, input component.Envelope) (ExecutionSnapshot, error) {
	bp, err := e.blueprints.Get(blueprintID, version)
	if err != nil {
		return ExecutionSnapshot{}, err
	}
	if input.TraceID == "" {
		input.TraceID = newID("trace")
	}
	if input.InputID == "" {
		input.InputID = newID("input")
	}
	nodes := make(map[string]Node, len(bp.Nodes))
	runs := make(map[string]*NodeRun, len(bp.Nodes))
	for _, node := range bp.Nodes {
		nodes[node.ID] = node
		runs[node.ID] = &NodeRun{NodeID: node.ID, ComponentID: node.ComponentID, ComponentVersion: node.ComponentVersion, Status: NodePending}
	}
	execCtx, cancel := context.WithCancel(ctx)
	ex := &execution{
		ID:               newID("exec"),
		BlueprintID:      bp.ID,
		BlueprintVersion: bp.Version,
		Status:           ExecutionPending,
		Source:           source,
		CreatedAt:        time.Now().UnixMilli(),
		Initial:          input,
		nodes:            nodes,
		edges:            append([]Edge(nil), bp.Edges...),
		runs:             runs,
		skipped:          make(map[string]bool),
		cancel:           cancel,
	}
	input.ExecutionID = ex.ID
	ex.Initial = input
	e.mu.Lock()
	e.executions[ex.ID] = ex
	e.mu.Unlock()
	defer func() {
		e.mu.RLock()
		sink := e.snapshotSink
		e.mu.RUnlock()
		if sink != nil {
			sink(ex.snapshot())
		}
	}()
	e.appendEvent("ExecutionCreated", ex.ID, "", map[string]any{"blueprint_id": bp.ID, "version": bp.Version, "source": source})

	err = e.run(execCtx, ex)
	cancel()
	return ex.snapshot(), err
}

func (e *Engine) run(ctx context.Context, ex *execution) error {
	ex.mu.Lock()
	ex.Status = ExecutionRunning
	ex.StartedAt = time.Now().UnixMilli()
	ex.mu.Unlock()
	e.appendEvent("ExecutionStarted", ex.ID, "", nil)

	order, predecessors, sinks, err := executionOrder(ex.nodes, ex.edges)
	if err != nil {
		return e.finishFailed(ex, err)
	}
	if len(order) > e.maxTransitions {
		return e.finishFailed(ex, fmt.Errorf("execution requires %d transitions, limit is %d", len(order), e.maxTransitions))
	}
	outputs := make(map[string]component.Envelope, len(order))
	for _, nodeID := range order {
		if err := ctx.Err(); err != nil {
			return e.finishCancelled(ex, err)
		}
		input, err := mergeNodeInput(ex.Initial, predecessors[nodeID], outputs)
		if err != nil {
			return e.finishFailed(ex, fmt.Errorf("prepare node %s input: %w", nodeID, err))
		}
		input.ExecutionID = ex.ID
		ex.mu.RLock()
		node := ex.nodes[nodeID]
		skipped := ex.skipped[nodeID]
		ex.mu.RUnlock()
		if skipped {
			ex.mu.Lock()
			run := ex.runs[nodeID]
			run.Status = NodeSkipped
			run.StartedAt = time.Now().UnixMilli()
			run.FinishedAt = run.StartedAt
			run.Input = input
			run.Output = input
			ex.mu.Unlock()
			outputs[nodeID] = input
			e.appendEvent("NodeSkipped", ex.ID, nodeID, nil)
			continue
		}
		output, err := e.executeNode(ctx, ex, node, input)
		if err != nil {
			return e.finishFailed(ex, err)
		}
		outputs[nodeID] = output
	}

	result, err := mergeNodeInput(ex.Initial, sinks, outputs)
	if err != nil {
		return e.finishFailed(ex, fmt.Errorf("assemble execution result: %w", err))
	}
	ex.mu.Lock()
	ex.Status = ExecutionCompleted
	ex.FinishedAt = time.Now().UnixMilli()
	ex.Result = result
	ex.mu.Unlock()
	e.appendEvent("ExecutionCompleted", ex.ID, "", nil)
	return nil
}

type nodeConfig struct {
	TimeoutMS   int `json:"timeout_ms"`
	MaxAttempts int `json:"max_attempts"`
}

func (e *Engine) executeNode(ctx context.Context, ex *execution, node Node, input component.Envelope) (component.Envelope, error) {
	c, err := e.registry.Resolve(node.ComponentID, node.ComponentVersion)
	if err != nil {
		return component.Envelope{}, fmt.Errorf("node %s: %w", node.ID, err)
	}
	resolved := c.Descriptor()
	var cfg nodeConfig
	_ = json.Unmarshal(node.Config, &cfg)
	if cfg.MaxAttempts < 1 {
		cfg.MaxAttempts = 1
	}
	if input.Metadata == nil {
		input.Metadata = make(map[string]string)
	}
	if len(node.Config) > 0 {
		input.Metadata["alice.node_config"] = string(node.Config)
	}

	ex.mu.Lock()
	run := ex.runs[node.ID]
	run.Status = NodeRunning
	run.StartedAt = time.Now().UnixMilli()
	run.Input = input
	run.ComponentID = resolved.ID
	run.ComponentVersion = resolved.Version
	ex.mu.Unlock()
	e.appendEvent("NodeStarted", ex.ID, node.ID, map[string]any{"component_id": resolved.ID, "version": resolved.Version})

	var output component.Envelope
	var lastErr error
	for attempt := 1; attempt <= cfg.MaxAttempts; attempt++ {
		callCtx := ctx
		cancel := func() {}
		if cfg.TimeoutMS > 0 {
			callCtx, cancel = context.WithTimeout(ctx, time.Duration(cfg.TimeoutMS)*time.Millisecond)
		}
		input.Attempt = attempt
		output, lastErr = c.Execute(callCtx, input)
		cancel()
		ex.mu.Lock()
		run.Attempts = attempt
		ex.mu.Unlock()
		if lastErr == nil {
			break
		}
		e.appendEvent("NodeAttemptFailed", ex.ID, node.ID, map[string]any{"attempt": attempt, "error": lastErr.Error()})
	}
	if lastErr != nil {
		ex.mu.Lock()
		run.Status = NodeFailed
		run.FinishedAt = time.Now().UnixMilli()
		run.Error = lastErr.Error()
		ex.mu.Unlock()
		e.appendEvent("NodeFailed", ex.ID, node.ID, map[string]any{"error": lastErr.Error()})
		return component.Envelope{}, fmt.Errorf("node %s (%s@%s): %w", node.ID, resolved.ID, resolved.Version, lastErr)
	}
	output.ExecutionID = ex.ID
	if output.TraceID == "" {
		output.TraceID = input.TraceID
	}
	if output.InputID == "" {
		output.InputID = input.InputID
	}
	ex.mu.Lock()
	run.Status = NodeCompleted
	run.FinishedAt = time.Now().UnixMilli()
	run.Output = output
	ex.mu.Unlock()
	e.appendEvent("NodeCompleted", ex.ID, node.ID, nil)
	return output, nil
}

func (e *Engine) ApplyPatch(executionID string, patch RuntimePatch) error {
	e.mu.RLock()
	ex := e.executions[executionID]
	e.mu.RUnlock()
	if ex == nil {
		return fmt.Errorf("execution %s not found", executionID)
	}
	ex.mu.Lock()
	defer ex.mu.Unlock()
	node, ok := ex.nodes[patch.TargetNode]
	if !ok {
		return fmt.Errorf("patch target node %s not found", patch.TargetNode)
	}
	if ex.runs[patch.TargetNode].Status != NodePending {
		return fmt.Errorf("patch target node %s is no longer pending", patch.TargetNode)
	}
	switch patch.Operation {
	case "replace_component":
		if patch.ComponentID == "" {
			return fmt.Errorf("replace_component requires component_id")
		}
		if _, err := e.registry.Resolve(patch.ComponentID, patch.ComponentVersion); err != nil {
			return err
		}
		node.ComponentID = patch.ComponentID
		node.ComponentVersion = patch.ComponentVersion
		ex.nodes[patch.TargetNode] = node
	case "set_config":
		node.Config = append(json.RawMessage(nil), patch.Config...)
		ex.nodes[patch.TargetNode] = node
	case "skip_node":
		ex.skipped[patch.TargetNode] = true
	default:
		return fmt.Errorf("unsupported runtime patch operation %q", patch.Operation)
	}
	if patch.ID == "" {
		patch.ID = newID("patch")
	}
	patch.AppliedAt = time.Now().UnixMilli()
	ex.patches = append(ex.patches, patch)
	go e.appendEvent("RuntimePatchApplied", executionID, patch.TargetNode, patch)
	return nil
}

func (e *Engine) Cancel(id string) error {
	e.mu.RLock()
	ex := e.executions[id]
	e.mu.RUnlock()
	if ex == nil {
		return fmt.Errorf("execution %s not found", id)
	}
	ex.mu.RLock()
	cancel := ex.cancel
	ex.mu.RUnlock()
	if cancel != nil {
		cancel()
	}
	return nil
}

func (e *Engine) Get(id string) (ExecutionSnapshot, bool) {
	e.mu.RLock()
	ex := e.executions[id]
	e.mu.RUnlock()
	if ex == nil {
		return ExecutionSnapshot{}, false
	}
	return ex.snapshot(), true
}

func (e *Engine) List() []ExecutionSnapshot {
	e.mu.RLock()
	executions := make([]*execution, 0, len(e.executions))
	for _, ex := range e.executions {
		executions = append(executions, ex)
	}
	e.mu.RUnlock()
	out := make([]ExecutionSnapshot, 0, len(executions))
	for _, ex := range executions {
		out = append(out, ex.snapshot())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt > out[j].CreatedAt })
	return out
}

// BlueprintCandidate strips run data and promotes the execution's private graph
// snapshot (including accepted Runtime Patches) into a new immutable candidate.
func (e *Engine) BlueprintCandidate(executionID, id, name string, version int) (Blueprint, error) {
	e.mu.RLock()
	ex := e.executions[executionID]
	e.mu.RUnlock()
	if ex == nil {
		return Blueprint{}, fmt.Errorf("execution %s not found", executionID)
	}
	if id == "" || version < 1 {
		return Blueprint{}, fmt.Errorf("candidate blueprint id and positive version are required")
	}
	ex.mu.RLock()
	nodes := make([]Node, 0, len(ex.nodes))
	for _, node := range ex.nodes {
		node.Config = append(json.RawMessage(nil), node.Config...)
		nodes = append(nodes, node)
	}
	edges := append([]Edge(nil), ex.edges...)
	baseID, baseVersion := ex.BlueprintID, ex.BlueprintVersion
	ex.mu.RUnlock()
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].ID < nodes[j].ID })
	if name == "" {
		name = id
	}
	candidate := Blueprint{ID: id, Version: version, Name: name, Description: fmt.Sprintf("Promoted from execution %s based on %s v%d.", executionID, baseID, baseVersion), Nodes: nodes, Edges: edges, CreatedAt: time.Now().UnixMilli()}
	if err := candidate.Validate(); err != nil {
		return Blueprint{}, err
	}
	return candidate, nil
}

func (e *Engine) finishFailed(ex *execution, err error) error {
	ex.mu.Lock()
	ex.Status = ExecutionFailed
	ex.FinishedAt = time.Now().UnixMilli()
	ex.Error = err.Error()
	ex.mu.Unlock()
	e.appendEvent("ExecutionFailed", ex.ID, "", map[string]any{"error": err.Error()})
	return err
}

func (e *Engine) finishCancelled(ex *execution, err error) error {
	ex.mu.Lock()
	ex.Status = ExecutionCancelled
	ex.FinishedAt = time.Now().UnixMilli()
	ex.Error = err.Error()
	ex.mu.Unlock()
	e.appendEvent("ExecutionCancelled", ex.ID, "", map[string]any{"error": err.Error()})
	return err
}

func (e *Engine) appendEvent(kind, executionID, nodeID string, data any) {
	if e.events == nil {
		return
	}
	var raw json.RawMessage
	if data != nil {
		raw, _ = json.Marshal(data)
	}
	_ = e.events.Append(Event{Type: kind, ExecutionID: executionID, NodeID: nodeID, Data: raw})
}

func executionOrder(nodes map[string]Node, edges []Edge) (order []string, predecessors map[string][]string, sinks []string, err error) {
	indegree := make(map[string]int, len(nodes))
	adj := make(map[string][]string, len(nodes))
	outdegree := make(map[string]int, len(nodes))
	predecessors = make(map[string][]string, len(nodes))
	for id := range nodes {
		indegree[id] = 0
	}
	for _, edge := range edges {
		if _, ok := nodes[edge.From]; !ok {
			return nil, nil, nil, fmt.Errorf("edge source %s missing", edge.From)
		}
		if _, ok := nodes[edge.To]; !ok {
			return nil, nil, nil, fmt.Errorf("edge target %s missing", edge.To)
		}
		adj[edge.From] = append(adj[edge.From], edge.To)
		predecessors[edge.To] = append(predecessors[edge.To], edge.From)
		indegree[edge.To]++
		outdegree[edge.From]++
	}
	var queue []string
	for id, degree := range indegree {
		if degree == 0 {
			queue = append(queue, id)
		}
	}
	sort.Strings(queue)
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		order = append(order, id)
		for _, next := range adj[id] {
			indegree[next]--
			if indegree[next] == 0 {
				queue = append(queue, next)
				sort.Strings(queue)
			}
		}
	}
	if len(order) != len(nodes) {
		return nil, nil, nil, fmt.Errorf("execution graph contains a cycle")
	}
	for id := range nodes {
		if outdegree[id] == 0 {
			sinks = append(sinks, id)
		}
	}
	sort.Strings(sinks)
	return order, predecessors, sinks, nil
}

func mergeNodeInput(initial component.Envelope, predecessors []string, outputs map[string]component.Envelope) (component.Envelope, error) {
	if len(predecessors) == 0 {
		return initial, nil
	}
	if len(predecessors) == 1 {
		return outputs[predecessors[0]], nil
	}
	sort.Strings(predecessors)
	payloads := make(map[string]json.RawMessage, len(predecessors))
	merged := initial
	for _, id := range predecessors {
		out, ok := outputs[id]
		if !ok {
			return component.Envelope{}, fmt.Errorf("predecessor %s has no output", id)
		}
		payloads[id] = out.Payload
	}
	b, err := json.Marshal(payloads)
	if err != nil {
		return component.Envelope{}, err
	}
	merged.Schema = "alice.join.v1"
	merged.Payload = b
	return merged, nil
}
