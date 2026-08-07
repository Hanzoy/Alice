package dynamic

import (
	"context"
	"encoding/json"
	"path/filepath"
	"runtime"
	"testing"

	"alice/internal/core"
	"alice/pkg/component"
)

func TestBuildRegisterAndExecuteGoComponent(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	projectRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	sourceRoot := filepath.Join(projectRoot, "components")
	dataRoot := t.TempDir()
	registry := core.NewRegistry()
	manager := NewManager(registry, sourceRoot, filepath.Join(dataRoot, "bin"), filepath.Join(dataRoot, "installed.json"))
	if !manager.Toolchain().Available {
		t.Skip("Go toolchain is unavailable")
	}
	manifest, err := manager.Build(context.Background(), filepath.Join(sourceRoot, "uppercase"), true)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Descriptor.ID != "text.uppercase" {
		t.Fatalf("unexpected manifest: %+v", manifest)
	}
	c, err := registry.Resolve("text.uppercase", "")
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]string{"text": "Alice"})
	result, err := c.Execute(context.Background(), component.Envelope{Schema: "alice.text.v1", Payload: payload})
	if err != nil {
		t.Fatal(err)
	}
	var output map[string]string
	if err := json.Unmarshal(result.Payload, &output); err != nil {
		t.Fatal(err)
	}
	if output["text"] != "ALICE" {
		t.Fatalf("dynamic output = %q", output["text"])
	}
}
