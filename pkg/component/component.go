package component

import (
	"context"
	"encoding/json"
)

// Descriptor is the stable contract Alice Core uses to discover and manage a component.
type Descriptor struct {
	ID           string   `json:"id"`
	Version      string   `json:"version"`
	Description  string   `json:"description,omitempty"`
	Kind         string   `json:"kind"`
	Lifecycle    string   `json:"lifecycle"`
	InputSchema  string   `json:"input_schema,omitempty"`
	OutputSchema string   `json:"output_schema,omitempty"`
	BuiltIn      bool     `json:"built_in,omitempty"`
	Protected    bool     `json:"protected,omitempty"`
	SideEffects  []string `json:"side_effects,omitempty"`
}

// Envelope is the transport shared by all components. Payload is intentionally
// opaque to Core; schemas and component contracts give it meaning.
type Envelope struct {
	TraceID        string            `json:"trace_id"`
	ExecutionID    string            `json:"execution_id,omitempty"`
	InputID        string            `json:"input_id,omitempty"`
	Source         string            `json:"source,omitempty"`
	ReplyHandle    string            `json:"reply_handle,omitempty"`
	Schema         string            `json:"schema,omitempty"`
	Payload        json.RawMessage   `json:"payload,omitempty"`
	Metadata       map[string]string `json:"metadata,omitempty"`
	Artifacts      []string          `json:"artifacts,omitempty"`
	Provenance     []string          `json:"provenance,omitempty"`
	DeadlineUnixMS int64             `json:"deadline_unix_ms,omitempty"`
	Attempt        int               `json:"attempt,omitempty"`
}

// Component is the only execution interface required by the first Core.
// Streaming and persistent processes can be adapted behind this interface.
type Component interface {
	Descriptor() Descriptor
	Execute(context.Context, Envelope) (Envelope, error)
}
