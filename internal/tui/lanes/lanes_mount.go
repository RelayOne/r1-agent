package lanes

import (
	"sync"

	tea "charm.land/bubbletea/v2"
)

// Mount is the thin composition hook referenced by doc.go §Boundaries
// and specs/tui-lanes.md checklist item 25: the ONE symbol non-v2 code
// (the chat-interactive line REPL in cmd/r1) is allowed to call to
// attach the lanes panel.
//
// Deviation from the spec sketch, recorded here and in the spec
// checklist: item 25 sketched `Mount(parent *tea.Program) (subModel
// tea.Model, cleanup func())` for embedding the panel inside an
// existing Bubble Tea v2 program. No v2 parent program exists anywhere
// in the repo — the chat-interactive host is a plain line REPL and the
// legacy internal/tui stack pins Bubble Tea v1, which cannot host a v2
// model. The shipped Mount therefore constructs and OWNS the v2
// program instead of embedding into one:
//
//	p, m, cleanup := lanes.Mount(sessionID, lanes.NewLocalTransport(ws), nil)
//	go func() { _, _ = p.Run() }()
//	... workload runs; panel renders lane events live ...
//	cleanup() // stop producer + quit program (idempotent)
//
// teaOpts is forwarded verbatim to tea.NewProgram — production callers
// pass nil (default stdin/stdout TTY handling); tests pass
// tea.WithInput(nil) / tea.WithOutput(io.Discard) / tea.WithWindowSize
// to run headless.
//
// The returned cleanup is idempotent and safe to call from any
// goroutine, but MUST only be called after p.Run() has been started
// (or has returned): tea.Program.Send blocks on an unbuffered channel
// until the event loop is draining or the program context is done.
// Both production call sites (cmd/r1 chat-interactive) and the tests
// obey this by starting p.Run() in a goroutine before doing any work.
//
// Mount does NOT call p.Run itself so the caller decides threading —
// per doc.go, this package never spawns more goroutines than the one
// producer.
func Mount(sessionID string, t Transport, teaOpts []tea.ProgramOption, opts ...Option) (*tea.Program, *Model, func()) {
	m := New(sessionID, t, opts...)
	p := tea.NewProgram(m, teaOpts...)
	var once sync.Once
	cleanup := func() {
		once.Do(func() {
			m.stopProducer()
			p.Quit()
		})
	}
	return p, m, cleanup
}

// stopProducer cancels the runProducer goroutine spawned by Init, if
// any. Safe to call from outside the Update loop (m.mu serializes the
// cancel handoff against Init's swap and the quit-key path in
// handleKey, both of which touch m.cancel under m.mu). Idempotent:
// the second call sees nil and returns.
func (m *Model) stopProducer() {
	m.mu.Lock()
	c := m.cancel
	m.cancel = nil
	m.mu.Unlock()
	if c != nil {
		c()
	}
}
