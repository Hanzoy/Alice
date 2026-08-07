package core

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"alice/pkg/component"
)

type testComponent struct {
	id      string
	version string
	value   string
}

func (c testComponent) Descriptor() component.Descriptor {
	return component.Descriptor{ID: c.id, Version: c.version, Kind: "transform", Lifecycle: "per_call"}
}

func (c testComponent) Execute(_ context.Context, in component.Envelope) (component.Envelope, error) {
	in.Payload, _ = json.Marshal(map[string]string{"value": c.value})
	return in, nil
}

func TestRegistryHotActivation(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register(testComponent{id: "example", version: "v1", value: "one"}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(testComponent{id: "example", version: "v2", value: "two"}); err != nil {
		t.Fatal(err)
	}
	first, _ := registry.Resolve("example", "")
	if first.Descriptor().Version != "v1" {
		t.Fatalf("initial active version = %s", first.Descriptor().Version)
	}
	if err := registry.Activate("example", "v2"); err != nil {
		t.Fatal(err)
	}
	second, _ := registry.Resolve("example", "")
	if second.Descriptor().Version != "v2" {
		t.Fatalf("hot active version = %s", second.Descriptor().Version)
	}
	if first.Descriptor().Version != "v1" {
		t.Fatal("resolved in-flight instance changed after activation")
	}
}

func TestBlueprintImmutableAndExecutionSnapshot(t *testing.T) {
	registry := NewRegistry()
	_ = registry.Register(testComponent{id: "example", version: "v1", value: "done"})
	blueprints := NewBlueprintStore()
	bp := Blueprint{ID: "test", Version: 1, Name: "test", CreatedAt: time.Now().UnixMilli(), Nodes: []Node{{ID: "node", ComponentID: "example"}}}
	if err := blueprints.Publish(bp, true); err != nil {
		t.Fatal(err)
	}
	bp.Nodes[0].ComponentID = "mutated"
	stored, err := blueprints.Get("test", 1)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Nodes[0].ComponentID != "example" {
		t.Fatal("published blueprint was mutated through caller-owned memory")
	}
	if err := blueprints.Publish(stored, true); err == nil {
		t.Fatal("expected immutable duplicate version rejection")
	}
	engine := NewEngine(registry, blueprints, NewEventLog(""))
	execution, err := engine.Start(context.Background(), "test", 0, "test", component.Envelope{Schema: "test", Payload: json.RawMessage(`{"input":true}`)})
	if err != nil {
		t.Fatal(err)
	}
	if execution.Status != ExecutionCompleted || len(execution.NodeRuns) != 1 {
		t.Fatalf("unexpected execution: %+v", execution)
	}
}
