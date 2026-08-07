package dynamic

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"alice/internal/core"
	"alice/pkg/component"
)

type Manifest struct {
	Descriptor component.Descriptor `json:"descriptor"`
	Command    string               `json:"command,omitempty"`
	Args       []string             `json:"args,omitempty"`
	Dir        string               `json:"dir,omitempty"`
}

type Manager struct {
	mu          sync.Mutex
	registry    *core.Registry
	sourceRoot  string
	binRoot     string
	installPath string
	installed   []Manifest
	goBinary    string
	goVersion   string
	buildEnv    []string
}

type RuntimeManager interface {
	Build(context.Context, string, bool) (Manifest, error)
	BuildSource(context.Context, GeneratedSource) (Manifest, error)
	Load() error
	Toolchain() ToolchainInfo
}

type GeneratedSource struct {
	Manifest Manifest `json:"manifest"`
	MainGo   string   `json:"main_go"`
	Activate bool     `json:"activate"`
}

func (m *Manager) BuildSource(ctx context.Context, source GeneratedSource) (Manifest, error) {
	d := source.Manifest.Descriptor
	if d.ID == "" || d.Version == "" {
		return Manifest{}, fmt.Errorf("generated component requires descriptor id and version")
	}
	if !strings.Contains(source.MainGo, "package main") || !strings.Contains(source.MainGo, "component.ServeStdio") {
		return Manifest{}, fmt.Errorf("generated Go source must be package main and use component.ServeStdio")
	}
	source.Manifest.Command = ""
	source.Manifest.Args = nil
	source.Manifest.Dir = ""
	dir := filepath.Join(m.sourceRoot, "generated", sanitize(d.ID))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Manifest{}, err
	}
	manifestBytes, err := json.MarshalIndent(source.Manifest, "", "  ")
	if err != nil {
		return Manifest{}, err
	}
	if err = os.WriteFile(filepath.Join(dir, "component.json"), manifestBytes, 0o600); err != nil {
		return Manifest{}, err
	}
	if err = os.WriteFile(filepath.Join(dir, "main.go"), []byte(source.MainGo), 0o600); err != nil {
		return Manifest{}, err
	}
	return m.Build(ctx, dir, source.Activate)
}

func (m *Manager) Manifests() []Manifest {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Manifest(nil), m.installed...)
}

func NewManager(registry *core.Registry, sourceRoot, binRoot, installPath string) *Manager {
	if absolute, err := filepath.Abs(sourceRoot); err == nil {
		sourceRoot = absolute
	}
	if absolute, err := filepath.Abs(binRoot); err == nil {
		binRoot = absolute
	}
	if absolute, err := filepath.Abs(installPath); err == nil {
		installPath = absolute
	}
	goBinary, goVersion, buildEnv := discoverGoToolchain(sourceRoot)
	return &Manager{registry: registry, sourceRoot: sourceRoot, binRoot: binRoot, installPath: installPath, goBinary: goBinary, goVersion: goVersion, buildEnv: buildEnv}
}

type ToolchainInfo struct {
	Available  bool   `json:"available"`
	GoBinary   string `json:"go_binary,omitempty"`
	Version    string `json:"version,omitempty"`
	SourceRoot string `json:"source_root"`
	BinRoot    string `json:"bin_root"`
}

func (m *Manager) Toolchain() ToolchainInfo {
	return ToolchainInfo{Available: m.goBinary != "", GoBinary: m.goBinary, Version: m.goVersion, SourceRoot: m.sourceRoot, BinRoot: m.binRoot}
}

func (m *Manager) Load() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, err := os.ReadFile(m.installPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if err := json.Unmarshal(b, &m.installed); err != nil {
		return fmt.Errorf("decode installed components: %w", err)
	}
	for _, manifest := range m.installed {
		if err := m.registerLocked(manifest, true); err != nil {
			return err
		}
	}
	return nil
}

// Build compiles a Go main package below sourceRoot, then hot-registers the
// resulting stdio component. Existing invocations keep their resolved process;
// new invocations see activation immediately.
func (m *Manager) Build(ctx context.Context, sourceDir string, activate bool) (Manifest, error) {
	if m.goBinary == "" {
		return Manifest{}, fmt.Errorf("Go toolchain is unavailable; set ALICE_GO_BINARY or install the project-local .tools/go toolchain")
	}
	abs, err := filepath.Abs(sourceDir)
	if err != nil {
		return Manifest{}, err
	}
	root, err := filepath.Abs(m.sourceRoot)
	if err != nil {
		return Manifest{}, err
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return Manifest{}, fmt.Errorf("component source %s is outside %s", abs, root)
	}
	manifestBytes, err := os.ReadFile(filepath.Join(abs, "component.json"))
	if err != nil {
		return Manifest{}, fmt.Errorf("read component manifest: %w", err)
	}
	var manifest Manifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		return Manifest{}, fmt.Errorf("decode component manifest: %w", err)
	}
	if manifest.Descriptor.ID == "" || manifest.Descriptor.Version == "" {
		return Manifest{}, fmt.Errorf("component manifest requires descriptor id and version")
	}
	if err := os.MkdirAll(m.binRoot, 0o755); err != nil {
		return Manifest{}, err
	}
	name := sanitize(manifest.Descriptor.ID) + "-" + sanitize(manifest.Descriptor.Version)
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	commandPath := filepath.Join(m.binRoot, name)
	cmd := exec.CommandContext(ctx, m.goBinary, "build", "-o", commandPath, ".")
	cmd.Dir = abs
	cmd.Env = m.buildEnv
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Run(); err != nil {
		return Manifest{}, fmt.Errorf("build component: %w: %s", err, strings.TrimSpace(output.String()))
	}
	manifest.Command = commandPath
	manifest.Dir = abs
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.registerLocked(manifest, activate); err != nil {
		return Manifest{}, err
	}
	replaced := false
	for i := range m.installed {
		if m.installed[i].Descriptor.ID == manifest.Descriptor.ID && m.installed[i].Descriptor.Version == manifest.Descriptor.Version {
			m.installed[i] = manifest
			replaced = true
			break
		}
	}
	if !replaced {
		m.installed = append(m.installed, manifest)
	}
	if err := m.saveLocked(); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func (m *Manager) registerLocked(manifest Manifest, activate bool) error {
	if _, err := os.Stat(manifest.Command); err != nil {
		return fmt.Errorf("dynamic component %s binary: %w", manifest.Descriptor.ID, err)
	}
	if err := m.registry.Register(&ProcessComponent{manifest: manifest}); err != nil {
		return err
	}
	if activate {
		return m.registry.Activate(manifest.Descriptor.ID, manifest.Descriptor.Version)
	}
	return nil
}

func (m *Manager) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(m.installPath), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(m.installed, "", "  ")
	if err != nil {
		return err
	}
	tmp := m.installPath + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, m.installPath)
}

type ProcessComponent struct{ manifest Manifest }

func (c *ProcessComponent) Descriptor() component.Descriptor { return c.manifest.Descriptor }

func (c *ProcessComponent) Execute(ctx context.Context, input component.Envelope) (component.Envelope, error) {
	b, err := json.Marshal(input)
	if err != nil {
		return component.Envelope{}, err
	}
	cmd := exec.CommandContext(ctx, c.manifest.Command, c.manifest.Args...)
	cmd.Dir = c.manifest.Dir
	cmd.Stdin = bytes.NewReader(b)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return component.Envelope{}, fmt.Errorf("dynamic component %s: %w: %s", c.manifest.Descriptor.ID, err, strings.TrimSpace(stderr.String()))
	}
	var result component.Envelope
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		return component.Envelope{}, fmt.Errorf("dynamic component %s returned invalid JSON: %w", c.manifest.Descriptor.ID, err)
	}
	return result, nil
}

type BuilderComponent struct{ Manager RuntimeManager }

func (BuilderComponent) Descriptor() component.Descriptor {
	return component.Descriptor{ID: "component.build.go", Version: "builtin-1", Description: "Compile and hot-register a Go stdio component from Alice's component source tree.", Kind: "compiler", Lifecycle: "singleton", InputSchema: "alice.component_build.v1", OutputSchema: "alice.component_manifest.v1", BuiltIn: true, Protected: true, SideEffects: []string{"compile", "component_register"}}
}

func (c BuilderComponent) Execute(ctx context.Context, input component.Envelope) (component.Envelope, error) {
	var request struct {
		SourceDir string `json:"source_dir"`
		Activate  bool   `json:"activate"`
	}
	if err := json.Unmarshal(input.Payload, &request); err != nil {
		return component.Envelope{}, fmt.Errorf("decode component build request: %w", err)
	}
	manifest, err := c.Manager.Build(ctx, request.SourceDir, request.Activate)
	if err != nil {
		return component.Envelope{}, err
	}
	input.Schema = "alice.component_manifest.v1"
	input.Payload, _ = json.Marshal(manifest)
	return input, nil
}

func sanitize(value string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, value)
}

func discoverGoToolchain(sourceRoot string) (string, string, []string) {
	root, _ := filepath.Abs(sourceRoot)
	workspace := filepath.Dir(root)
	goBinary := strings.TrimSpace(os.Getenv("ALICE_GO_BINARY"))
	if goBinary == "" {
		name := "go"
		if runtime.GOOS == "windows" {
			name = "go.exe"
		}
		local := filepath.Join(workspace, ".tools", "go", "bin", name)
		if _, err := os.Stat(local); err == nil {
			goBinary = local
		} else if found, lookErr := exec.LookPath("go"); lookErr == nil {
			goBinary = found
		}
	}
	if goBinary == "" {
		return "", "", nil
	}
	cacheRoot := filepath.Join(workspace, ".cache")
	_ = os.MkdirAll(filepath.Join(cacheRoot, "go-build"), 0o755)
	_ = os.MkdirAll(filepath.Join(cacheRoot, "gopath"), 0o755)
	_ = os.MkdirAll(filepath.Join(cacheRoot, "gomod"), 0o755)
	env := append([]string(nil), os.Environ()...)
	env = append(env,
		"GOCACHE="+filepath.Join(cacheRoot, "go-build"),
		"GOPATH="+filepath.Join(cacheRoot, "gopath"),
		"GOMODCACHE="+filepath.Join(cacheRoot, "gomod"),
		"GOTOOLCHAIN=local",
	)
	cmd := exec.Command(goBinary, "version")
	cmd.Env = env
	versionBytes, err := cmd.Output()
	if err != nil {
		return "", "", nil
	}
	return goBinary, strings.TrimSpace(string(versionBytes)), env
}
