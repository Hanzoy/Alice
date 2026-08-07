package component

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
)

// ServeStdio exposes a component through Alice's one-request JSON stdio
// protocol. Dynamically compiled Go components can use this helper.
func ServeStdio(ctx context.Context, c Component, in io.Reader, out io.Writer) error {
	var envelope Envelope
	if err := json.NewDecoder(in).Decode(&envelope); err != nil {
		return fmt.Errorf("decode component input: %w", err)
	}
	result, err := c.Execute(ctx, envelope)
	if err != nil {
		return err
	}
	if err := json.NewEncoder(out).Encode(result); err != nil {
		return fmt.Errorf("encode component output: %w", err)
	}
	return nil
}
