// Package agentmsg implements an inter-agent communication protocol.
// Inspired by OmX's multi-agent coordination and OpenHands' event stream:
//
// When multiple agents work in parallel (e.g., Claude implements while Codex
// reviews), they need to communicate:
// - Task handoffs (implementation → review)
// - Discovery sharing (agent A found relevant context for agent B)
// - Conflict alerts (two agents editing the same file)
// - Status broadcasts (progress, completion, failure)
//
// Messages are typed, routed by topic, and support request-response patterns.
package agentmsg

import (
	"fmt"
	"sync"
	"time"
)

// MsgType classifies a message.
type MsgType string

const (
	MsgHandoff   MsgType = "handoff"   // task transfer between agents
	MsgDiscovery MsgType = "discovery" // shared finding/context
	MsgConflict  MsgType = "conflict"  // file/resource conflict alert
	MsgStatus    MsgType = "status"    // progress update
	MsgRequest   MsgType = "request"   // ask another agent for help
	MsgResponse  MsgType = "response"  // reply to a request
	MsgBroadcast MsgType = "broadcast" // sent to all agents
)

// Msg is an inter-agent message.
type Msg struct {
	ID        string         `json:"id"`
	Type      MsgType        `json:"type"`
	From      string         `json:"from"`
	To        string         `json:"to,omitempty"` // empty for broadcast
	Topic     string         `json:"topic"`
	Payload   map[string]any `json:"payload,omitempty"`
	ReplyTo   string         `json:"reply_to,omitempty"` // for request-response
	Timestamp time.Time      `json:"timestamp"`
}

// Handler processes incoming messages. Returns a response payload or nil.
type Handler func(msg Msg) map[string]any

// Bus routes messages between agents.
type Bus struct {
	mu       sync.RWMutex
	agents   map[string]*agentReg
	messages []Msg
	nextID   int
}

type agentReg struct {
	name     string
	handler  Handler
	inbox    []Msg
	topics   map[string]bool // subscribed topics
}

// NewBus creates a message bus.
func NewBus() *Bus {
	return &Bus{
		agents: make(map[string]*agentReg),
	}
}

// Register adds an agent to the bus.
func (b *Bus) Register(name string, handler Handler, topics ...string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	topicSet := make(map[string]bool)
	for _, t := range topics {
		topicSet[t] = true
	}

	b.agents[name] = &agentReg{
		name:    name,
		handler: handler,
		topics:  topicSet,
	}
}

// Unregister removes an agent from the bus.
func (b *Bus) Unregister(name string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.agents, name)
}

// Send delivers a message to a specific agent.
func (b *Bus) Send(from, to string, msgType MsgType, topic string, payload map[string]any) string {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.nextID++
	id := fmt.Sprintf("msg-%d", b.nextID)

	msg := Msg{
		ID:        id,
		Type:      msgType,
		From:      from,
		To:        to,
		Topic:     topic,
		Payload:   payload,
		Timestamp: time.Now(),
	}

	b.messages = append(b.messages, msg)

	if agent, ok := b.agents[to]; ok {
		agent.inbox = append(agent.inbox, msg)
	}

	return id
}

// Broadcast sends a message to all agents (except sender).
func (b *Bus) Broadcast(from string, topic string, payload map[string]any) string {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.nextID++
	id := fmt.Sprintf("msg-%d", b.nextID)

	msg := Msg{
		ID:        id,
		Type:      MsgBroadcast,
		From:      from,
		Topic:     topic,
		Payload:   payload,
		Timestamp: time.Now(),
	}

	b.messages = append(b.messages, msg)

	for name, agent := range b.agents {
		if name != from {
			if len(agent.topics) == 0 || agent.topics[topic] {
				agent.inbox = append(agent.inbox, msg)
			}
		}
	}

	return id
}

// Request sends a request and waits for a synchronous response.
func (b *Bus) Request(from, to, topic string, payload map[string]any) (map[string]any, error) {
	b.mu.Lock()
	b.nextID++
	id := fmt.Sprintf("msg-%d", b.nextID)

	msg := Msg{
		ID:        id,
		Type:      MsgRequest,
		From:      from,
		To:        to,
		Topic:     topic,
		Payload:   payload,
		Timestamp: time.Now(),
	}

	agent, ok := b.agents[to]
	if !ok {
		b.mu.Unlock()
		return nil, fmt.Errorf("agent %s not found", to)
	}

	handler := agent.handler
	b.messages = append(b.messages, msg)
	b.mu.Unlock()

	if handler == nil {
		return nil, fmt.Errorf("agent %s has no handler", to)
	}

	response := handler(msg)
	return response, nil
}

// Receive returns pending messages for an agent and clears its inbox.
func (b *Bus) Receive(agentName string) []Msg {
	b.mu.Lock()
	defer b.mu.Unlock()

	agent, ok := b.agents[agentName]
	if !ok {
		return nil
	}

	msgs := agent.inbox
	agent.inbox = nil
	return msgs
}

// Peek returns pending messages without clearing.
func (b *Bus) Peek(agentName string) []Msg {
	b.mu.RLock()
	defer b.mu.RUnlock()

	agent, ok := b.agents[agentName]
	if !ok {
		return nil
	}

	result := make([]Msg, len(agent.inbox))
	copy(result, agent.inbox)
	return result
}

// PendingCount returns the number of unread messages for an agent.
func (b *Bus) PendingCount(agentName string) int {
	b.mu.RLock()
	defer b.mu.RUnlock()

	if agent, ok := b.agents[agentName]; ok {
		return len(agent.inbox)
	}
	return 0
}

// Agents returns the list of registered agent names.
func (b *Bus) Agents() []string {
	b.mu.RLock()
	defer b.mu.RUnlock()

	var names []string
	for name := range b.agents {
		names = append(names, name)
	}
	return names
}

// MessageCount returns total messages sent through the bus.
func (b *Bus) MessageCount() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.messages)
}

// History returns all messages, optionally filtered by topic.
func (b *Bus) History(topic string) []Msg {
	b.mu.RLock()
	defer b.mu.RUnlock()

	if topic == "" {
		result := make([]Msg, len(b.messages))
		copy(result, b.messages)
		return result
	}

	var filtered []Msg
	for _, m := range b.messages {
		if m.Topic == topic {
			filtered = append(filtered, m)
		}
	}
	return filtered
}
