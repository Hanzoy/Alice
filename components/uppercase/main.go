package main

import (
	"context"
	"encoding/json"
	"os"
	"strings"

	"alice/pkg/component"
)

type uppercase struct{}

func (uppercase) Descriptor() component.Descriptor {
	return component.Descriptor{ID: "text.uppercase", Version: "1.0.0", Kind: "transform", Lifecycle: "per_call", InputSchema: "alice.text.v1", OutputSchema: "alice.text.v1"}
}

func (uppercase) Execute(_ context.Context, in component.Envelope) (component.Envelope, error) {
	var value struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(in.Payload, &value); err != nil {
		return component.Envelope{}, err
	}
	value.Text = strings.ToUpper(value.Text)
	in.Payload, _ = json.Marshal(value)
	return in, nil
}

func main() {
	if err := component.ServeStdio(context.Background(), uppercase{}, os.Stdin, os.Stdout); err != nil {
		panic(err)
	}
}
