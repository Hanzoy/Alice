package generator

import (
	"context"
	"testing"

	"alice/internal/dynamic"
)

type fakeModel struct{ response string }

func (f fakeModel) Configured() bool { return true }
func (f fakeModel) Chat(context.Context, string, string, bool) (string, error) {
	return f.response, nil
}

type fakeComponents struct{ source dynamic.GeneratedSource }

func (f *fakeComponents) Build(context.Context, string, bool) (dynamic.Manifest, error) {
	return dynamic.Manifest{}, nil
}
func (f *fakeComponents) BuildSource(_ context.Context, source dynamic.GeneratedSource) (dynamic.Manifest, error) {
	f.source = source
	return source.Manifest, nil
}
func (f *fakeComponents) Load() error { return nil }
func (f *fakeComponents) Toolchain() dynamic.ToolchainInfo {
	return dynamic.ToolchainInfo{Available: true}
}

func TestCreateBuildsGeneratedSource(t *testing.T) {
	manager := &fakeComponents{}
	model := fakeModel{response: `{"manifest":{"descriptor":{"id":"text.reverse","version":"1.0.0","kind":"transform","lifecycle":"per_call"}},"main_go":"package main\n// component.ServeStdio"}`}
	service := Service{Model: model, Components: manager}
	reply, err := service.Create(context.Background(), "创建反转文本组件")
	if err != nil {
		t.Fatal(err)
	}
	if manager.source.Manifest.Descriptor.ID != "text.reverse" || !manager.source.Activate {
		t.Fatalf("unexpected source: %+v", manager.source)
	}
	if reply == "" {
		t.Fatal("empty reply")
	}
}
