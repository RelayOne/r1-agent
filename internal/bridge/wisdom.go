package bridge

import (
	"context"
	"encoding/json"
	"time"

	"github.com/RelayOne/r1/internal/bus"
	"github.com/RelayOne/r1/internal/ledger"
	"github.com/RelayOne/r1/internal/wisdom"
)

// WisdomBridge wraps a wisdom.Store and emits bus events when learnings are recorded.
type WisdomBridge struct {
	store  *wisdom.Store
	bus    *bus.Bus
	ledger *ledger.Ledger
}

// NewWisdomBridge creates a WisdomBridge backed by a fresh wisdom.Store.
func NewWisdomBridge(b *bus.Bus, l *ledger.Ledger) *WisdomBridge {
	return &WisdomBridge{
		store:  wisdom.NewStore(),
		bus:    b,
		ledger: l,
	}
}

// Record adds a learning, publishes a bus event, and writes a ledger node.
func (wb *WisdomBridge) Record(taskID string, l wisdom.Learning) {
	wb.store.Record(taskID, l)

	payload, _ := json.Marshal(struct {
		TaskID   string          `json:"task_id"`
		Category string          `json:"category"`
		Desc     string          `json:"description"`
		File     string          `json:"file,omitempty"`
		Pattern  string          `json:"failure_pattern,omitempty"`
	}{
		TaskID:   taskID,
		Category: l.Category.String(),
		Desc:     l.Description,
		File:     l.File,
		Pattern:  l.FailurePattern,
	})

	_ = wb.bus.Publish(bus.Event{
		Type:      EvtLearningRecorded,
		Timestamp: time.Now(),
		EmitterID: "bridge.wisdom",
		Scope:     bus.Scope{TaskID: taskID},
		Payload:   payload,
	})

	_, _ = wb.ledger.AddNode(context.Background(), ledger.Node{
		Type:          "wisdom_learning",
		SchemaVersion: 1,
		CreatedBy:     "bridge.wisdom",
		Content:       payload,
	})
}

// ForPrompt formats accumulated learnings for prompt injection.
func (wb *WisdomBridge) ForPrompt() string {
	return wb.store.ForPrompt()
}

// FindByPattern returns the first learning matching the given failure hash.
func (wb *WisdomBridge) FindByPattern(hash string) *wisdom.Learning {
	return wb.store.FindByPattern(hash)
}
