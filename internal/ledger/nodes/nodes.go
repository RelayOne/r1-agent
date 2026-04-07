// Package nodes defines the Go structs for every node type in the ledger.
// The canonical schema source is 06-the-node-types.md in the Stoke guide.
package nodes

import (
	"fmt"
	"sync"
)

// NodeTyper is implemented by all ledger node types.
type NodeTyper interface {
	NodeType() string
	SchemaVersion() int
	Validate() error
}

var (
	registryMu sync.RWMutex
	registry   = map[string]func() NodeTyper{}
)

// Register adds a node type factory to the registry.
func Register(name string, factory func() NodeTyper) {
	registryMu.Lock()
	defer registryMu.Unlock()
	if _, ok := registry[name]; ok {
		panic(fmt.Sprintf("nodes: duplicate registration for %q", name))
	}
	registry[name] = factory
}

// New creates a zero-value instance of the named node type.
func New(name string) (NodeTyper, error) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	f, ok := registry[name]
	if !ok {
		return nil, fmt.Errorf("nodes: unknown node type %q", name)
	}
	return f(), nil
}

// All returns the sorted list of registered node type names.
func All() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	names := make([]string, 0, len(registry))
	for name := range registry {
		names = append(names, name)
	}
	// Sort for determinism.
	sortStrings(names)
	return names
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
