package dynamic

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"alice/internal/core"
	"alice/pkg/component"
)

type RemoteManager struct {
	registry       *core.Registry
	baseURL, token string
	client         *http.Client
	toolchain      ToolchainInfo
}

func NewRemoteManager(registry *core.Registry, baseURL, token string) *RemoteManager {
	return &RemoteManager{registry: registry, baseURL: strings.TrimRight(baseURL, "/"), token: token, client: &http.Client{Timeout: 130 * time.Second}}
}
func (m *RemoteManager) Toolchain() ToolchainInfo { return m.toolchain }
func (m *RemoteManager) Load() error {
	var response struct {
		Toolchain  ToolchainInfo `json:"toolchain"`
		Components []Manifest    `json:"components"`
	}
	if err := m.call(context.Background(), http.MethodGet, "/api/components", nil, &response); err != nil {
		return fmt.Errorf("component host: %w", err)
	}
	m.toolchain = response.Toolchain
	for _, manifest := range response.Components {
		if err := m.register(manifest, true); err != nil {
			return err
		}
	}
	return nil
}
func (m *RemoteManager) Build(ctx context.Context, sourceDir string, activate bool) (Manifest, error) {
	var manifest Manifest
	err := m.call(ctx, http.MethodPost, "/api/build", map[string]any{"source_dir": sourceDir, "activate": activate}, &manifest)
	if err != nil {
		return manifest, err
	}
	if err = m.register(manifest, activate); err != nil {
		return manifest, err
	}
	return manifest, nil
}
func (m *RemoteManager) BuildSource(ctx context.Context, source GeneratedSource) (Manifest, error) {
	var manifest Manifest
	err := m.call(ctx, http.MethodPost, "/api/build-source", source, &manifest)
	if err != nil {
		return manifest, err
	}
	if err = m.register(manifest, source.Activate); err != nil {
		return manifest, err
	}
	return manifest, nil
}
func (m *RemoteManager) register(manifest Manifest, activate bool) error {
	proxy := &RemoteComponent{manifest: manifest, manager: m}
	if err := m.registry.Register(proxy); err != nil {
		return err
	}
	if activate {
		return m.registry.Activate(manifest.Descriptor.ID, manifest.Descriptor.Version)
	}
	return nil
}
func (m *RemoteManager) call(ctx context.Context, method, path string, input, out any) error {
	var body bytes.Buffer
	if input != nil {
		if err := json.NewEncoder(&body).Encode(input); err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, m.baseURL+path, &body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if m.token != "" {
		req.Header.Set("Authorization", "Bearer "+m.token)
	}
	resp, err := m.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var e map[string]string
		_ = json.NewDecoder(resp.Body).Decode(&e)
		return fmt.Errorf("component host returned %s: %s", resp.Status, e["error"])
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

type RemoteComponent struct {
	manifest Manifest
	manager  *RemoteManager
}

func (c *RemoteComponent) Descriptor() component.Descriptor { return c.manifest.Descriptor }
func (c *RemoteComponent) Execute(ctx context.Context, input component.Envelope) (component.Envelope, error) {
	var output component.Envelope
	err := c.manager.call(ctx, http.MethodPost, "/api/invoke", map[string]any{"component_id": c.manifest.Descriptor.ID, "version": c.manifest.Descriptor.Version, "input": input}, &output)
	return output, err
}
