# RT-CANCEL-INTERRUPT — Live-Interrupt Mid-Stream Cancellation Research

**Date:** 2026-05-02
**Scope:** r1-agent live-interrupt mode. Side concern publishes a critical finding mid-turn. We cancel the in-flight Anthropic Messages stream, inject context, and start a new turn — without corrupting `messages[]`.

**Bottom line:** Yes, mid-stream cancel + replay is safe **iff** we maintain one invariant: every `tool_use` block sent in the next request has a matching `tool_result` immediately after, and we never send half-emitted `tool_use` JSON. The Anthropic API is strict about this and returns HTTP 400 otherwise. The proven repair pattern is to either (a) drop the entire partial assistant message before adding new user content, or (b) keep the partial message and append synthetic `tool_result` stubs (`"[Tool execution interrupted by user]"`) for every `tool_use` it contains.

---

## 1. Anthropic Messages API streaming cancellation

### What happens when the client closes the SSE stream mid-response

- The HTTP/2 stream is reset (RST_STREAM). The server stops generating but **does not retroactively send a `message_stop` event** — the partial state is lost server-side.
- The client gets whatever events it had already buffered. **Nothing is recoverable from the API after disconnect.** There is no resume token / no "give me the rest" endpoint.
- Anthropic's `error_recovery` docs explicitly call out: "Tool use and extended thinking blocks **cannot be partially recovered**. You can resume streaming from the most recent text block." — [docs](https://platform.claude.com/docs/en/api/messages-streaming) §"Error recovery best practices".
- For Claude 4.5 and earlier, the documented recovery is to start a new request with the partial assistant text as a prefilled assistant turn. For Claude 4.6+, the documented recovery is to add a *user* message saying "Your previous response was interrupted and ended with [...]. Continue from where you left off." (same docs page).

### Best practice for recording the partial assistant message

- Accumulate every event you receive into a `Message`-shaped struct as it streams (the Go SDK provides `message.Accumulate(event)` for this exact purpose; see [docs example](https://platform.claude.com/docs/en/api/messages-streaming) "Get the final message without handling events").
- On cancel, you have a partial `Message` whose `content` array may contain: complete `text` blocks, an in-progress `text` block (safe — text is just bytes), and possibly a `tool_use` block whose `input` is **partial JSON** (unsafe — can't be sent back).

### Stream events recap (relevant to cancellation midpoints)

```
message_start
  content_block_start (index 0, text)
    content_block_delta * N  (text_delta)
  content_block_stop
  content_block_start (index 1, tool_use)         <-- if cancelled here, block exists but input={}
    content_block_delta * M  (input_json_delta)   <-- partial JSON, unparseable
  content_block_stop                              <-- input is now valid JSON object
message_delta (stop_reason: "tool_use" or "end_turn")
message_stop
```

The unsafe windows are:
1. After `content_block_start(tool_use)` but before final `content_block_stop` for that block — `input` is an unparseable partial JSON string.
2. After `content_block_stop(tool_use)` but before the next request includes a `tool_result` — would orphan the tool_use.

---

## 2. Tool-use cancellation specifics

If you cancel during `input_json_delta` flow:
- The API server simply terminates its stream. There is no client-visible API state to "clean up" server-side; the request is fully closed.
- However, **your local message state now has a `tool_use` block that is either incomplete (bad JSON) or complete-but-unanswered.** Sending it back to the API will produce one of these 400s ([Cline issue #3003](https://github.com/anthropics/claude-code/issues/3003)):
  ```
  messages.N: tool_use ids were found without tool_result blocks immediately after: toolu_xxx.
  Each tool_use block must have a corresponding tool_result block in the next message.
  ```
- You **can** send the next request without a `tool_result` only if the partial `tool_use` block is **removed from the assistant message you persist**.

---

## 3. Replay-safe message history — the canonical pattern

There are two valid patterns. Both are battle-tested in production agents.

### Pattern A — Drop the partial assistant message (cleanest, what we recommend)

After cancel:
1. Discard the in-progress assistant message entirely. Do not append it to `messages[]`.
2. Append the new user message (e.g., `"<system-interrupt>Critical finding from concern X: ...</system-interrupt>\n\nOriginal request continues: ..."`).
3. Send the next request. `messages[]` ends with `user` — totally valid.

Cost: you "lose" the model's partial reasoning. In practice, for an interrupt scenario this is almost always desirable — you *want* the model to re-plan with the new info.

### Pattern B — Keep the partial assistant message, repair tool_use orphans

Used by LiteLLM ([`sanitize_messages_for_tool_calling`](https://docs.litellm.ai/docs/completion/message_sanitization)) when `modify_params=True`:

1. Accumulate the partial assistant message.
2. Strip any `tool_use` block whose `input` is incomplete JSON (block didn't reach `content_block_stop`).
3. For every remaining `tool_use` block (input is valid), append a synthetic `tool_result`:
   ```json
   {
     "role": "user",
     "content": [{
       "type": "tool_result",
       "tool_use_id": "toolu_abc",
       "content": "[System: Tool execution skipped/interrupted by user. No result provided.]",
       "is_error": true
     }]
   }
   ```
4. Then append the real new user message describing the interrupt.

This preserves the partial reasoning text. Cline does a variant of this in `StreamResponseHandler.ts` where `pendingToolUses` are emitted with `partial: true` flags so the rest of the system can decide to drop or repair them.

### Real-world examples

- **Cline** (`src/core/task/index.ts`, `StreamResponseHandler.ts` — see `gh api repos/cline/cline/contents/src/core/task/index.ts`): uses `taskState.abort` flag, `didFinishAbortingStream`, `handleHookCancellation`, and persists `pendingToolUses` map of in-flight tool_use blocks all marked `partial: true`. On abort: ALWAYS save state regardless of cancellation source (line ~977), then call `cancelTask()`.
- **Claude Code** ([Issue #3003](https://github.com/anthropics/claude-code/issues/3003), [#33949 RCA](https://github.com/anthropics/claude-code/issues/33949)): currently *broken* — orphan rate 2.4–14% across 1571 sessions / 148k tool calls. Their proposed fix: atomic message-chain writes plus streaming idle timeout. Workaround today is "start a new conversation."
- **Roo Code** ([Issue #4903](https://github.com/RooCodeInc/Roo-Code/issues/4903) + PR #4904): differentiates "API Streaming Failed" vs "API Request Cancelled" but both cleanly tear down state.
- **LiteLLM**: synthetic `tool_result` stubs when missing (`_add_missing_tool_results` in `factory.py`).
- **Twilio ConversationRelay + Claude voice** ([blog post](https://www.twilio.com/en-us/blog/anthropic-conversationrelay-token-streaming-interruptions-javascript)): `handleInterrupt` finds the last assistant message containing `utteranceUntilInterrupt`, **truncates that message at the interruption boundary**, removes any subsequent assistant messages, then continues. This is Pattern A with a smart truncation — only viable for pure-text replies.
- **Vercel AI SDK** ([stopping-streams](https://ai-sdk.dev/docs/advanced/stopping-streams)): `onAbort: ({ steps }) => savePartialResults(steps)` callback, `isAborted` flag in `onFinish`. Note their docs: "Stream abort functionality conflicts with resumption — choose one or the other."

---

## 4. Go context cancellation patterns for HTTP/2 SSE

### The standard pattern

```go
// outer ctx is the loop's parent context
turnCtx, cancelTurn := context.WithCancel(ctx)
defer cancelTurn()  // ALWAYS — defensive cleanup on every path

// SDK call — anthropic-sdk-go propagates context to underlying http.Request
stream := client.Messages.NewStreaming(turnCtx, params)

for stream.Next() {
    select {
    case <-turnCtx.Done():
        // cancel observed — fall through, stream.Err() will be ctx.Err() next iteration
    default:
    }
    event := stream.Current()
    accumulator.Accumulate(event)
    // ... dispatch text/tool_use deltas
}
// After loop exits, MUST check stream.Err()
if err := stream.Err(); err != nil {
    if errors.Is(err, context.Canceled) {
        // expected on interrupt — proceed to repair messages[]
    }
}
// stream.Close() / response body close is handled by SDK on stream end
```

### Anthropic Go SDK behavior

- `client.Messages.NewStreaming(ctx, params)` returns `ssestream.Stream[MessageStreamEventUnion]`.
- The SDK respects context cancellation and closes the response body before retry / on error ([DeepWiki](https://deepwiki.com/anthropics/anthropic-sdk-go/3.4-error-handling)).
- `stream.Err()` will return `context.Canceled` after cancel.
- `message.Accumulate(event)` is the official partial-recovery helper — keep calling it for every event, on cancel you'll have a `Message` with whatever content blocks completed.

### Connection pool & goroutine-leak gotchas

- **Always pair `WithCancel` with `defer cancel()`.** Otherwise the cancel func leaks until the parent ctx ends. ([Go docs `context.WithCancel`](https://pkg.go.dev/context#WithCancel))
- **Always drain or close the response body.** Even on cancel. The SDK handles this for `NewStreaming`, but if you ever drop to raw `http.Client`, you must `io.Copy(io.Discard, resp.Body); resp.Body.Close()` — otherwise the HTTP/2 stream stays mapped on the connection and you leak (golang/go#21229).
- **HTTP/2 cancellation reliability has a long history of bugs** — issues like [#21229](https://github.com/golang/go/issues/21229) (cancel doesn't release stream) and [#49366](https://github.com/golang/go/issues/49366) (spurious context-canceled error from Body.Close). Mitigation: don't share clients across totally unrelated contexts; let SDK-managed clients handle it; on Go 1.22+ these are largely fixed but still verify.
- **Don't sleep waiting for cancel to "settle."** Just `defer cancel()`, observe `stream.Err()`, proceed.
- **Goroutines reading the stream must observe the context** — if the consumer goroutine ignores `ctx.Done()` it will keep reading until the underlying TCP closes, which can be slow on bad networks. The standard `for stream.Next()` loop handles this correctly because the SDK's reader honors the request context.

### Idle-timeout watchdog (recommended belt-and-suspenders)

Anthropic API sends `:ping` SSE comments periodically. Hung connections that go silent with no ping for >30s are dead. Add:

```go
lastEvent := time.Now()
go func() {
    t := time.NewTicker(5 * time.Second)
    defer t.Stop()
    for {
        select {
        case <-turnCtx.Done(): return
        case <-t.C:
            if time.Since(lastEvent) > 30*time.Second {
                cancelTurn() // triggers stream.Next() to return with ctx.Err()
                return
            }
        }
    }
}()
// in stream loop: lastEvent = time.Now()
```

This is the fix proposed in [Claude Code #33949 RCA](https://github.com/anthropics/claude-code/issues/33949) for hung streams.

---

## 5. Idempotency & extracting value from partial work

### Do we lose the work the model already did?

If we use **Pattern A** (drop partial): yes, we discard the partial reasoning. **Best for true interrupts** — the side concern fired because the existing trajectory is wrong; the model should not be biased by its prior tokens.

If we use **Pattern B** (keep partial + synthetic tool_result): the partial assistant text is in the next prompt. Model can reference it. **Best for additive context** — e.g., "you missed considering X, continue."

### Cost / billing

- Anthropic bills `input_tokens` for the request and `output_tokens` for whatever was generated — even on cancel. The `message_delta` event carries the `usage` field, but it's only emitted at end. On cancel you'll typically not see it; Anthropic finalizes usage server-side and you can fetch it via response headers if you used `WithResponseInto`. Practically, log the accumulated text/tool_use length as a proxy.

### Idempotency

- The Messages API has no `idempotency-key` header today (unlike OpenAI). A retry after cancel is a *new* request — you cannot resume the prior one. So "interrupt then send revised user message" always sends a fresh billed request.
- This is *fine* for our use case. We're not trying to dedupe — we're explicitly asking the model to re-plan.

---

## 6. Recommended pattern for r1-agent

r1 already enforces "tool_use/tool_result pair integrity" in `internal/agentloop/loop.go` (search: "preserve tool_use/tool_result pair integrity"). The interrupt path should reuse that invariant.

### Recommended: Pattern A (drop partial) with optional Pattern B fallback

```go
// In the agentloop turn runner:
type Turn struct {
    parentCtx context.Context
    cancel    context.CancelFunc
    msgs      []Message               // committed history; never mutated mid-turn
    partial   *Message                // accumulated assistant message (in-flight)
    interrupt chan InterruptPayload   // side-concern channel
}

func (t *Turn) Run() (next []Message, reason StopReason, err error) {
    turnCtx, cancel := context.WithCancel(t.parentCtx)
    defer cancel()

    stream := provider.NewStreaming(turnCtx, buildReq(t.msgs))
    var acc Message  // anthropic.Message{}; use stream events to populate

    streamDone := make(chan error, 1)
    go func() {
        for stream.Next() {
            ev := stream.Current()
            _ = acc.Accumulate(ev)
            // emit text deltas to UI
        }
        streamDone <- stream.Err()
    }()

    select {
    case err := <-streamDone:
        if err != nil { return t.msgs, StopErr, err }
        // normal completion — append acc, run tools, etc.
        return append(t.msgs, acc), reasonFrom(acc), nil

    case ip := <-t.interrupt:
        cancel()                  // (1) tear down stream
        <-streamDone              // (2) drain the reader goroutine — no leak
        // (3) Pattern A: discard `acc` entirely. The committed history t.msgs
        //     is unchanged and ends with the original user message.
        injected := injectInterrupt(t.msgs, ip) // append a NEW user message
        return injected, StopInterrupted, nil
    }
}

func injectInterrupt(msgs []Message, ip InterruptPayload) []Message {
    return append(msgs, Message{
        Role: "user",
        Content: []ContentBlock{{
            Type: "text",
            Text: fmt.Sprintf(
                "<system-interrupt source=%q severity=%q>\n%s\n</system-interrupt>\n\n"+
                "Re-plan with the above in mind.",
                ip.Source, ip.Severity, ip.Finding,
            ),
        }},
    })
}
```

### The invariant r1 must maintain

> **After every cancel, `messages[]` must satisfy: every `tool_use` block in any assistant message is followed (in the very next `user` message) by a `tool_result` block with the matching `tool_use_id`. The simplest way to honor this is: *never persist a partial assistant message to the committed history.* Hold it in a separate `partial *Message` field; on normal completion, commit; on interrupt, drop.**

### Additional safeguards

1. `defer cancel()` on every code path that opens a `WithCancel`.
2. Drain the reader goroutine via `<-streamDone` after `cancel()` — prevents goroutine leak on slow TCP teardown.
3. Watchdog: 30s idle timeout monitoring `:ping`/event arrival → calls `cancel()`.
4. If we ever need Pattern B (keep partial), implement a `repairOrphans(msgs)` helper that scans the last assistant message and either strips incomplete `tool_use` blocks or appends synthetic `tool_result` stubs (LiteLLM-style: `"[Tool execution interrupted by user]"` with `is_error: true`).
5. Unit test: feed a fake stream that is cut mid-`input_json_delta`, assert `messages[]` after interrupt+inject still passes the API's pairing rule (we can simulate this with our existing sanitize logic).

---

## Citations

- Anthropic streaming docs: https://platform.claude.com/docs/en/api/messages-streaming (esp. "Error recovery" — "Tool use and extended thinking blocks cannot be partially recovered.")
- Claude Code interrupt-corruption bug: https://github.com/anthropics/claude-code/issues/3003
- Claude Code SSE hang RCA + proposed fixes: https://github.com/anthropics/claude-code/issues/33949
- Claude Code MCP cancel 422: https://github.com/anthropics/claude-code/issues/7673
- Anthropic SDK TS streaming truncation issue: https://github.com/anthropics/anthropic-sdk-typescript/issues/842
- Anthropic SDK TS idle-timeout proposal: https://github.com/anthropics/anthropic-sdk-typescript/issues/867
- Anthropic Go SDK error handling: https://deepwiki.com/anthropics/anthropic-sdk-go/3.4-error-handling
- Cline source: `src/core/task/index.ts` (taskState.abort, handleHookCancellation, didFinishAbortingStream); `src/core/task/StreamResponseHandler.ts` (`pendingToolUses` map, `partial: true` flagging)
- Roo Code cancel-state bug: https://github.com/RooCodeInc/Roo-Code/issues/4903
- LiteLLM message sanitization: https://docs.litellm.ai/docs/completion/message_sanitization
- Twilio ConversationRelay interrupt pattern: https://www.twilio.com/en-us/blog/anthropic-conversationrelay-token-streaming-interruptions-javascript
- Vercel AI SDK stopping streams: https://ai-sdk.dev/docs/advanced/stopping-streams
- Go HTTP/2 cancel-doesn't-release-stream: https://github.com/golang/go/issues/21229
- Go HTTP/2 spurious context-canceled on Body.Close: https://github.com/golang/go/issues/49366
