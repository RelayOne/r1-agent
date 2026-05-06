# tests/agent/web/chat-send-message.agent.feature.md

<!-- TAGS: smoke, web, chat -->
<!-- DEPENDS: r1d-server, web-chat-ui -->

## Scenario: User sends a message and sees a streamed response

- Given a fresh r1d daemon at "http://127.0.0.1:3948"
- And the web UI is loaded at "/"
- And a session is started with workdir "/tmp/agentic-test-1"
- When I fill the textbox with name "Message" with "ping"
- And I click the button with name "Send"
- Then within 5 seconds the chat log contains an assistant message matching "pong|ping"
- And the cortex Workspace contains at least one Note tagged "memory-recall"
- And no lane has status "errored"

## Tool mapping (informative, runner derives automatically)
- "loaded at" -> r1.web.navigate
- "fill the textbox" -> r1.web.fill
- "click the button" -> r1.web.click
- "chat log contains" -> r1.web.snapshot
- "cortex Workspace" -> r1.cortex.notes
- "no lane has status" -> r1.lanes.list
