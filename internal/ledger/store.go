package ledger

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Store handles filesystem-backed persistence of nodes and edges.
// Nodes are stored as JSON files under {rootDir}/nodes/{id}.json.
// Edges are stored under {rootDir}/edges/{from}-{to}-{type}.json.
type Store struct {
	nodesDir string
	edgesDir string
}

// NewStore opens or creates the filesystem store under rootDir.
func NewStore(rootDir string) (*Store, error) {
	nodesDir := filepath.Join(rootDir, "nodes")
	edgesDir := filepath.Join(rootDir, "edges")

	if err := os.MkdirAll(nodesDir, 0o755); err != nil {
		return nil, fmt.Errorf("create nodes dir: %w", err)
	}
	if err := os.MkdirAll(edgesDir, 0o755); err != nil {
		return nil, fmt.Errorf("create edges dir: %w", err)
	}

	return &Store{nodesDir: nodesDir, edgesDir: edgesDir}, nil
}

// WriteNode persists a node as a JSON file. It does not overwrite an
// existing node with the same ID (append-only).
func (s *Store) WriteNode(n Node) error {
	path := filepath.Join(s.nodesDir, n.ID+".json")
	if _, err := os.Stat(path); err == nil {
		// Node already exists; content-addressed dedup -- not an error.
		return nil
	}
	data, err := json.MarshalIndent(n, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal node: %w", err)
	}
	return os.WriteFile(path, data, 0o644)
}

// ReadNode loads a node from its JSON file.
func (s *Store) ReadNode(id NodeID) (Node, error) {
	path := filepath.Join(s.nodesDir, id+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		return Node{}, fmt.Errorf("read node %s: %w", id, err)
	}
	var n Node
	if err := json.Unmarshal(data, &n); err != nil {
		return Node{}, fmt.Errorf("unmarshal node %s: %w", id, err)
	}
	return n, nil
}

// WriteEdge persists an edge as a JSON file.
func (s *Store) WriteEdge(e Edge) error {
	filename := e.From + "-" + e.To + "-" + string(e.Type) + ".json"
	path := filepath.Join(s.edgesDir, filename)
	if _, err := os.Stat(path); err == nil {
		return nil // already exists
	}
	data, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal edge: %w", err)
	}
	return os.WriteFile(path, data, 0o644)
}

// ListNodes reads all node files from the store.
func (s *Store) ListNodes() ([]Node, error) {
	entries, err := os.ReadDir(s.nodesDir)
	if err != nil {
		return nil, fmt.Errorf("read nodes dir: %w", err)
	}
	var nodes []Node
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.nodesDir, e.Name()))
		if err != nil {
			continue
		}
		var n Node
		if err := json.Unmarshal(data, &n); err != nil {
			continue
		}
		nodes = append(nodes, n)
	}
	return nodes, nil
}

// ListEdges reads all edge files from the store.
func (s *Store) ListEdges() ([]Edge, error) {
	entries, err := os.ReadDir(s.edgesDir)
	if err != nil {
		return nil, fmt.Errorf("read edges dir: %w", err)
	}
	var edges []Edge
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.edgesDir, e.Name()))
		if err != nil {
			continue
		}
		var edge Edge
		if err := json.Unmarshal(data, &edge); err != nil {
			continue
		}
		edges = append(edges, edge)
	}
	return edges, nil
}
