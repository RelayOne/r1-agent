// Package gateway implements STOKE-024: multi-platform
// messaging adapter surface. Lets a user drive a running
// Stoke agent from Telegram / Discord / Slack / etc. via
// plugged-in PlatformAdapter implementations.
//
// Scope of this file:
//
//   - Message + Conversation types (platform-agnostic)
//   - PlatformAdapter interface
//   - Router that fans messages across adapters
//   - DM pairing (signed token + HMAC-verified reply)
//   - Cross-platform conversation continuity (one
//     ConversationID survives across platforms so the agent
//     has a continuous view)
//
// Per-platform adapters live in gateway/platforms/ so each
// can be independently imported (or omitted) without pulling
// every SDK into every build.
package gateway

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"sync"
	"time"
)

// unpairedLog is where the defaultUnpairedHandler writes.
// Stderr by default; callers that embed gateway into a
// structured-logged process can swap via SetUnpairedLog.
var unpairedLog io.Writer = os.Stderr

// SetUnpairedLog overrides where defaultUnpairedHandler
// emits unpaired-sender notices. Exposed mainly for tests
// (keep stderr noise out of test output).
func SetUnpairedLog(w io.Writer) {
	unpairedLog = w
}

// Platform identifies a messaging backend.
type Platform string

const (
	PlatformTelegram Platform = "telegram"
	PlatformDiscord  Platform = "discord"
	PlatformSlack    Platform = "slack"
	PlatformWhatsApp Platform = "whatsapp"
	PlatformSignal   Platform = "signal"
	PlatformMatrix   Platform = "matrix"
	PlatformEmail    Platform = "email"
	PlatformSMS      Platform = "sms"
	PlatformWebhook  Platform = "webhook"
)

// AllPlatforms returns the 9 declared platforms. The gateway
// ships built-in adapter scaffolds for the first three;
// others are stubs operators can fill in via Register.
func AllPlatforms() []Platform {
	return []Platform{
		PlatformTelegram, PlatformDiscord, PlatformSlack,
		PlatformWhatsApp, PlatformSignal, PlatformMatrix,
		PlatformEmail, PlatformSMS, PlatformWebhook,
	}
}

// Message is the platform-agnostic message shape. Each
// adapter translates its native format into this struct.
type Message struct {
	// ID is the message ID as reported by the platform.
	// Opaque to the router; used by adapters to dedupe +
	// reply-by-quote where the platform supports that.
	ID string

	// Platform identifies the origin adapter.
	Platform Platform

	// ConversationID is the stable cross-message identifier
	// for the session. Bound to a single agent + user on
	// pairing; persists across the user's DMs until revoked.
	ConversationID string

	// SenderPlatformID is the user's ID as known to the
	// platform (e.g. Telegram user ID, Discord snowflake,
	// Slack user ID). Kept for audit + anti-spoofing.
	SenderPlatformID string

	// Text is the message body. Adapters pre-process
	// platform-specific markup (emoji, mentions) into plain
	// text before populating this.
	Text string

	// Attachments are opaque per-platform blobs the agent
	// may want to fetch. Optional.
	Attachments []Attachment

	// ReceivedAt is the wall-clock time the gateway handled
	// the inbound message.
	ReceivedAt time.Time
}

// Attachment is a platform-reported file/media reference.
type Attachment struct {
	Kind      string // "image" | "audio" | "video" | "file" | ...
	URL       string
	SizeBytes int64
}

// Outbound is what the gateway asks an adapter to deliver.
type Outbound struct {
	ConversationID   string
	Platform         Platform
	RecipientPlatformID string
	Text             string
	// Attachments are optional; adapters that don't support
	// them drop silently.
	Attachments []Attachment
}

// PlatformAdapter is the per-platform contract. Adapters are
// registered with Router.Register; the router dispatches
// inbound + outbound messages through them.
type PlatformAdapter interface {
	// Platform reports the adapter's Platform identifier.
	Platform() Platform

	// Send delivers an outbound message. Blocking;
	// adapters handle rate-limiting + retry internally.
	Send(ctx context.Context, out Outbound) error

	// Start begins polling / subscribing for inbound
	// messages. Each inbound message is delivered via the
	// supplied callback. Errors don't abort; the adapter
	// retries using whatever backoff it prefers.
	// Implementations call cb from their own goroutines.
	Start(ctx context.Context, cb func(Message)) error
}

// Router fans inbound messages to the conversation
// coordinator and outbound messages back to the right
// adapter. One Router per process.
type Router struct {
	mu       sync.RWMutex
	adapters map[Platform]PlatformAdapter
	coord    *ConversationCoordinator
	secret   []byte
	unpaired UnpairedHandler
}

// NewRouter constructs a Router. `secret` is the HMAC key
// used to sign pairing tokens; operators should persist a
// stable secret across restarts so outstanding tokens remain
// valid. Empty secret auto-generates a random key (not
// suitable for production).
func NewRouter(secret []byte) *Router {
	if len(secret) == 0 {
		secret = make([]byte, 32)
		_, _ = rand.Read(secret)
	}
	return &Router{
		adapters: map[Platform]PlatformAdapter{},
		coord:    NewConversationCoordinator(),
		secret:   secret,
	}
}

// Register installs (or replaces) an adapter for its
// Platform. Safe to call at any time.
func (r *Router) Register(a PlatformAdapter) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.adapters[a.Platform()] = a
}

// Coordinator returns the Router's ConversationCoordinator.
// Exposed so callers (typically an HTTP / RPC surface that
// handles pairing-token redemption) can Bind new
// (platform, sender) → conversation mappings after a user
// redeems a pairing token.
func (r *Router) Coordinator() *ConversationCoordinator {
	return r.coord
}

// Bind is a convenience that forwards to the coordinator so
// callers don't need to hold both handles. Matches the
// documented pairing flow: (1) agent issues a token via
// IssuePairingToken, (2) user DMs the token to the target
// platform, (3) adapter calls VerifyPairingToken + Router.Bind
// to record the mapping, (4) subsequent inbound DMs resolve
// via ResolveConversation and flow through to the handler.
func (r *Router) Bind(p Platform, senderID, conversationID string) {
	r.coord.Bind(p, senderID, conversationID)
}

// Unbind forwards to coordinator.Unbind. Used on explicit
// user "unpair" or revocation.
func (r *Router) Unbind(p Platform, senderID string) {
	r.coord.Unbind(p, senderID)
}

// RedeemPairingToken is the convenience the adapter calls
// when a user DMs a pairing token to a platform. It
// verifies the token signature + expiry, then binds the
// (platform, senderID) pair to the token's conversation.
// Returns the conversation ID on success. Adapters that
// want to handle token-parsing themselves can use
// VerifyPairingToken + Bind as separate steps.
func (r *Router) RedeemPairingToken(tok PairingToken, p Platform, senderID string) (string, error) {
	cid, err := r.VerifyPairingToken(tok)
	if err != nil {
		return "", err
	}
	r.Bind(p, senderID, cid)
	return cid, nil
}

// Registered returns the sorted list of platforms with
// registered adapters.
func (r *Router) Registered() []Platform {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Platform, 0, len(r.adapters))
	for p := range r.adapters {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// ErrNoAdapter is returned by Send when the target
// platform has no registered adapter.
var ErrNoAdapter = errors.New("gateway: no adapter for platform")

// Send delivers an Outbound via the matching adapter.
func (r *Router) Send(ctx context.Context, out Outbound) error {
	r.mu.RLock()
	a, ok := r.adapters[out.Platform]
	r.mu.RUnlock()
	if !ok {
		return fmt.Errorf("%w: %q", ErrNoAdapter, out.Platform)
	}
	return a.Send(ctx, out)
}

// UnpairedHandler is invoked for messages from a sender
// that has no active pairing. Operators inject a handler
// that either (a) replies to the sender with instructions
// for how to pair, (b) forwards the message to a
// registration flow, or (c) logs + drops. The default
// (used when no handler is installed) is log + drop so
// unpaired DMs don't silently disappear without trace.
type UnpairedHandler func(ctx context.Context, m Message)

// SetUnpairedHandler installs an UnpairedHandler. Nil
// resets to the default log-and-drop behavior. Safe to
// call while StartAll is running.
func (r *Router) SetUnpairedHandler(h UnpairedHandler) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.unpaired = h
}

// StartAll starts every registered adapter. Inbound
// messages land in the supplied handler after going through
// the pairing + conversation coordinator. Blocks until ctx
// is canceled.
//
// Error delivery: a per-Router errCh with a dedicated
// drain goroutine so handler errors surface BEFORE
// shutdown. The first error returned by the drain goroutine
// is the one StartAll returns; subsequent errors are
// logged to the debug sink. Drain happens concurrently
// with adapter runtime so a failing handler doesn't block
// every adapter goroutine behind a full channel.
func (r *Router) StartAll(ctx context.Context, handler func(ctx context.Context, m Message) error) error {
	r.mu.RLock()
	adapters := make([]PlatformAdapter, 0, len(r.adapters))
	for _, a := range r.adapters {
		adapters = append(adapters, a)
	}
	r.mu.RUnlock()
	// NOTE: we deliberately do NOT snapshot r.unpaired here.
	// The callback below reads it under r.mu.RLock() on each
	// inbound message so that a runtime SetUnpairedHandler
	// call (which the setter's docstring promises works while
	// StartAll is running) takes effect immediately.

	// Buffer sized generously so handler errors don't back-
	// pressure the adapter callback (which would hold up a
	// platform poll loop behind a full channel — the P1 bug
	// the dedicated reader fixes).
	errCh := make(chan error, 128)
	firstErr := make(chan error, 1)

	// Drain goroutine runs CONCURRENTLY with adapters.
	// Captures the FIRST error and ignores the rest so the
	// adapter goroutines never block on a full errCh.
	var drainWg sync.WaitGroup
	drainWg.Add(1)
	go func() {
		defer drainWg.Done()
		for err := range errCh {
			select {
			case firstErr <- err:
			default:
				// firstErr already holds an error; drop.
				// Production deployments with a structured
				// logger should log these; the gateway
				// binary logs via its own sink.
			}
		}
		close(firstErr)
	}()

	var wg sync.WaitGroup
	for _, a := range adapters {
		a := a
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := a.Start(ctx, func(m Message) {
				// Resolve conversation ID via the
				// coordinator — cross-platform
				// continuity happens here.
				cid, ok := r.coord.ResolveConversation(m)
				if !ok {
					// Unpaired sender — route through the
					// caller-supplied UnpairedHandler (or
					// the default log-and-drop). Critically,
					// we do NOT silently drop — the P0 bug
					// was that unpaired senders vanished.
					// Read r.unpaired LIVE so runtime
					// SetUnpairedHandler calls take effect
					// without restarting StartAll.
					r.mu.RLock()
					handler := r.unpaired
					r.mu.RUnlock()
					if handler != nil {
						handler(ctx, m)
					} else {
						r.defaultUnpairedHandler(m)
					}
					return
				}
				m.ConversationID = cid
				if err := handler(ctx, m); err != nil {
					select {
					case errCh <- err:
					default:
						// errCh full — drop. Shouldn't
						// happen with the 128-slot buffer
						// + concurrent drain.
					}
				}
			})
			if err != nil && ctx.Err() == nil {
				select {
				case errCh <- err:
				default:
				}
			}
		}()
	}
	wg.Wait()
	close(errCh)
	drainWg.Wait()
	// firstErr is closed; nil when no errors arrived.
	if err, ok := <-firstErr; ok {
		return err
	}
	return nil
}

// defaultUnpairedHandler is the log-and-drop fallback when
// no UnpairedHandler is installed. Uses the fmt package to
// write to stderr so operators see the dropped messages at
// least transiently. Real deployments always install a
// handler.
func (r *Router) defaultUnpairedHandler(m Message) {
	fmt.Fprintf(unpairedLog, "gateway: unpaired inbound from platform=%s sender=%s (install SetUnpairedHandler to route these)\n",
		m.Platform, m.SenderPlatformID)
}

// --- DM pairing ---

// PairingToken is the string a user sends to a platform DM
// to bind that platform account to an agent session. Format:
//   <conversation_id>.<expires_unix>.<hmac>
//
// The HMAC is computed over conversation_id + "." +
// expires_unix using the Router's secret. Tokens can be
// issued by the agent (via IssuePairingToken) and verified
// by the adapter when a user DMs it back.
type PairingToken string

// IssuePairingToken returns a token the agent can share with
// the user over an already-authenticated side channel (web
// UI, existing adapter). The user pastes the token into a DM
// on the target platform; the adapter verifies + binds.
func (r *Router) IssuePairingToken(conversationID string, ttl time.Duration) PairingToken {
	expires := time.Now().Add(ttl).Unix()
	msg := fmt.Sprintf("%s.%d", conversationID, expires)
	mac := hmac.New(sha256.New, r.secret)
	mac.Write([]byte(msg))
	sig := hex.EncodeToString(mac.Sum(nil))
	return PairingToken(msg + "." + sig)
}

// VerifyPairingToken verifies a pairing token and returns
// the conversation ID it authorizes. Errors on bad signature
// or expired token.
func (r *Router) VerifyPairingToken(tok PairingToken) (string, error) {
	parts := splitDots(string(tok))
	if len(parts) != 3 {
		return "", fmt.Errorf("gateway: token shape must be <cid>.<expires>.<hmac>")
	}
	cid, expiresStr, sig := parts[0], parts[1], parts[2]
	msg := cid + "." + expiresStr
	mac := hmac.New(sha256.New, r.secret)
	mac.Write([]byte(msg))
	want := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(sig), []byte(want)) {
		return "", fmt.Errorf("gateway: token signature invalid")
	}
	// Parse expiry.
	var expiresUnix int64
	if _, err := fmt.Sscanf(expiresStr, "%d", &expiresUnix); err != nil {
		return "", fmt.Errorf("gateway: token expiry unparseable: %w", err)
	}
	if time.Now().Unix() > expiresUnix {
		return "", fmt.Errorf("gateway: token expired")
	}
	return cid, nil
}

// splitDots splits s on the first two '.' characters so a
// ConversationID containing dots doesn't break parsing.
// Pairing tokens always have exactly 3 components:
// <cid>.<expires>.<hmac>. The HMAC never contains a dot; the
// expires is always numeric. So we find the LAST dot
// (separating sig) and the SECOND-TO-LAST dot (separating
// expires). The cid can then contain anything.
func splitDots(s string) []string {
	// Find last dot.
	last := -1
	secondLast := -1
	for i, c := range s {
		if c == '.' {
			secondLast = last
			last = i
		}
	}
	if last < 0 || secondLast < 0 {
		return nil
	}
	return []string{s[:secondLast], s[secondLast+1 : last], s[last+1:]}
}

// --- Conversation continuity ---

// ConversationCoordinator maps (platform, senderID) pairs to
// a stable cross-platform ConversationID. Once a user has
// paired Discord + Telegram to the same ConversationID, a
// message from either surfaces under the same conversation
// to the agent.
type ConversationCoordinator struct {
	mu      sync.Mutex
	byPair  map[pairKey]string // (platform, senderID) -> conversation ID
	history map[string][]Message
}

type pairKey struct {
	platform Platform
	sender   string
}

// NewConversationCoordinator returns an empty coordinator.
func NewConversationCoordinator() *ConversationCoordinator {
	return &ConversationCoordinator{
		byPair:  map[pairKey]string{},
		history: map[string][]Message{},
	}
}

// Bind records that (platform, senderID) belongs to the
// named conversation. Called by an adapter after a pairing
// token verifies.
func (c *ConversationCoordinator) Bind(p Platform, senderID, conversationID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.byPair[pairKey{p, senderID}] = conversationID
}

// Unbind drops a (platform, senderID) → conversation
// mapping. Called on explicit user "unpair" or revocation.
func (c *ConversationCoordinator) Unbind(p Platform, senderID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.byPair, pairKey{p, senderID})
}

// ResolveConversation returns the conversation ID for the
// message's (platform, sender). ok=false when the sender
// isn't paired.
func (c *ConversationCoordinator) ResolveConversation(m Message) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	cid, ok := c.byPair[pairKey{m.Platform, m.SenderPlatformID}]
	return cid, ok
}

// Append adds a message to the conversation's history for
// cross-platform echo. Kept bounded at a configurable cap.
func (c *ConversationCoordinator) Append(cid string, m Message) {
	c.mu.Lock()
	defer c.mu.Unlock()
	const maxHistory = 256
	h := c.history[cid]
	h = append(h, m)
	if len(h) > maxHistory {
		h = h[len(h)-maxHistory:]
	}
	c.history[cid] = h
}

// History returns a copy of the conversation's message log.
func (c *ConversationCoordinator) History(cid string) []Message {
	c.mu.Lock()
	defer c.mu.Unlock()
	h := c.history[cid]
	out := make([]Message, len(h))
	copy(out, h)
	return out
}

// --- Cron scheduling ---

// ScheduledTask is a periodic or one-shot task delivered to
// a conversation at a specified time.
type ScheduledTask struct {
	ID             string
	ConversationID string
	DeliverAt      time.Time
	// Repeat: if > 0, the task re-schedules itself at this
	// interval after each fire. Zero means one-shot.
	Repeat time.Duration
	Text   string
}

// CronScheduler tracks ScheduledTasks and fires them through
// the supplied Sender callback at the right time. Fires are
// best-effort; callers handle adapter retries themselves.
type CronScheduler struct {
	mu     sync.Mutex
	tasks  map[string]*ScheduledTask
	send   func(ctx context.Context, cid, text string) error
	now    func() time.Time
	ticker *time.Ticker
	stop   chan struct{}
}

// NewCronScheduler constructs a scheduler that calls send()
// when a task fires. now() is the clock (swappable for
// tests).
func NewCronScheduler(send func(ctx context.Context, cid, text string) error) *CronScheduler {
	return &CronScheduler{
		tasks: map[string]*ScheduledTask{},
		send:  send,
		now:   func() time.Time { return time.Now().UTC() },
		stop:  make(chan struct{}),
	}
}

// SetClock overrides the scheduler's clock. Tests only.
func (s *CronScheduler) SetClock(clock func() time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.now = clock
}

// Schedule registers a task. Replaces any existing task
// with the same ID.
func (s *CronScheduler) Schedule(task ScheduledTask) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := task
	s.tasks[task.ID] = &t
}

// Cancel removes a task.
func (s *CronScheduler) Cancel(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.tasks, id)
}

// List returns a sorted-by-DeliverAt snapshot.
func (s *CronScheduler) List() []ScheduledTask {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]ScheduledTask, 0, len(s.tasks))
	for _, t := range s.tasks {
		out = append(out, *t)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].DeliverAt.Before(out[j].DeliverAt)
	})
	return out
}

// Tick fires any due tasks. Exposed so tests can advance
// the scheduler without running a real ticker loop. In
// production, Start wires Tick to a background ticker.
func (s *CronScheduler) Tick(ctx context.Context) error {
	s.mu.Lock()
	now := s.now()
	var fire []*ScheduledTask
	for _, t := range s.tasks {
		if !t.DeliverAt.After(now) {
			fire = append(fire, t)
		}
	}
	s.mu.Unlock()

	for _, t := range fire {
		if err := s.send(ctx, t.ConversationID, t.Text); err != nil {
			return fmt.Errorf("cron fire %s: %w", t.ID, err)
		}
		s.mu.Lock()
		if t.Repeat > 0 {
			t.DeliverAt = t.DeliverAt.Add(t.Repeat)
		} else {
			delete(s.tasks, t.ID)
		}
		s.mu.Unlock()
	}
	return nil
}

// Start runs the scheduler loop until ctx is canceled.
// Ticks at 1s granularity — cron tasks in this package are
// coarse-scheduled; sub-second timing should use a dedicated
// scheduler.
func (s *CronScheduler) Start(ctx context.Context) {
	s.ticker = time.NewTicker(time.Second)
	defer s.ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.stop:
			return
		case <-s.ticker.C:
			_ = s.Tick(ctx)
		}
	}
}

// Stop halts the scheduler.
func (s *CronScheduler) Stop() {
	close(s.stop)
}

// --- Adapter scaffolds ---

// AdapterConfig is a shared scaffold for the platform
// adapter implementations in gateway/platforms/. Keeps the
// common fields (name, secret) in one place.
//
// The channel-typed inbound queue is NOT stored here — it
// would break JSON marshal (Go can't marshal a chan) and
// persisting a channel value across restarts is meaningless
// anyway. Adapters that want a queue construct one at
// start-up; this struct persists only the serializable
// fields.
type AdapterConfig struct {
	Name   string `json:"name"`
	Secret string `json:"secret"`
}

// MarshalConfig is a convenience helper so operators can
// persist the AdapterConfig between restarts without
// reaching for encoding/json themselves.
func MarshalConfig(c AdapterConfig) ([]byte, error) {
	return json.Marshal(c)
}
