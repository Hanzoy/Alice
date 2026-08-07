package core

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type Event struct {
	ID          string          `json:"id"`
	Type        string          `json:"type"`
	ExecutionID string          `json:"execution_id,omitempty"`
	NodeID      string          `json:"node_id,omitempty"`
	Time        int64           `json:"time"`
	Data        json.RawMessage `json:"data,omitempty"`
}

// EventLog is Alice's append-only global timeline for execution-level events.
type EventLog struct {
	mu     sync.Mutex
	path   string
	events []Event
}

func NewEventLog(path string) *EventLog {
	log := &EventLog{path: path}
	if path == "" {
		return log
	}
	f, err := os.Open(path)
	if err != nil {
		return log
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	buffer := make([]byte, 64*1024)
	scanner.Buffer(buffer, 4*1024*1024)
	for scanner.Scan() {
		var event Event
		if json.Unmarshal(scanner.Bytes(), &event) == nil {
			log.events = append(log.events, event)
		}
	}
	return log
}

func (l *EventLog) Append(event Event) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if event.Time == 0 {
		event.Time = time.Now().UnixMilli()
	}
	if event.ID == "" {
		event.ID = newID("evt")
	}
	l.events = append(l.events, event)
	if l.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(l.path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	b, err := json.Marshal(event)
	if err != nil {
		return err
	}
	_, err = f.Write(append(b, '\n'))
	return err
}

func (l *EventLog) List(limit int) []Event {
	l.mu.Lock()
	defer l.mu.Unlock()
	start := 0
	if limit > 0 && len(l.events) > limit {
		start = len(l.events) - limit
	}
	return append([]Event(nil), l.events[start:]...)
}
