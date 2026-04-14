# slack

> Slack Web API (`chat.postMessage`) + Incoming Webhooks + Block Kit. Webhooks for one-way notifications; full Web API for interactivity and threading.

<!-- keywords: slack, slack webhook, slack api, chat.postmessage, block kit, slack bot, slack app, incoming webhook -->

**Official docs:** https://docs.slack.dev  |  **Verified:** 2026-04-14 via web search.

## Incoming Webhooks (simple, one-way)

Easiest path for alert/notification channels. Create app at api.slack.com/apps → Incoming Webhooks → Add to workspace → copy webhook URL.

```js
await fetch(WEBHOOK_URL, {
  method: "POST",
  headers: { "Content-Type": "application/json" },
  body: JSON.stringify({
    text: "Order #42 placed",       // fallback + notification preview text
    blocks: [
      { type: "section", text: { type: "mrkdwn", text: "*Order #42 placed*\n$199 · Customer: Alex" } },
      { type: "actions", elements: [
        { type: "button", text: { type: "plain_text", text: "View" }, url: "https://app/orders/42" }
      ]},
    ],
  }),
});
```

Webhooks post to ONE channel (configured at install). Can't receive replies or button clicks. For interactivity, use a full Slack app.

## Web API (Bot token + richer surface)

Bot token: `xoxb-...` from your app's OAuth & Permissions page. Minimum scopes: `chat:write`, `channels:read`, `users:read`.

```js
import { WebClient } from "@slack/web-api";
const slack = new WebClient(SLACK_BOT_TOKEN);

const resp = await slack.chat.postMessage({
  channel: "C1234567890",           // channel ID, not name
  text: "Deploy complete",
  blocks: [...blockKitBlocks],
  thread_ts: "1713100000.000100",   // reply in thread (optional)
});
// resp.ts is the timestamp-id of the posted message — store if you want to update it later
```

Update / delete:

```js
await slack.chat.update({ channel, ts, text: "...", blocks: [...] });
await slack.chat.delete({ channel, ts });
await slack.chat.postEphemeral({ channel, user, text, blocks });  // visible to one user only
```

## Block Kit

Composable JSON layout blocks. Main types: `section`, `actions`, `divider`, `header`, `context`, `image`, `input`. See Block Kit Builder (app.slack.com/block-kit-builder) for interactive design.

```json
{
  "blocks": [
    { "type": "header", "text": { "type": "plain_text", "text": "Deploy succeeded" } },
    { "type": "section", "fields": [
      { "type": "mrkdwn", "text": "*Environment:*\nproduction" },
      { "type": "mrkdwn", "text": "*Commit:*\n<https://github.com/...|abc1234>" }
    ]},
    { "type": "actions", "elements": [
      { "type": "button", "text": { "type": "plain_text", "text": "View build" }, "url": "..." }
    ]}
  ]
}
```

`mrkdwn` supports Slack's markdown subset (bold `*...*`, italic `_..._`, code `` `...` ``, links `<url|label>`). NOT CommonMark.

## Interactivity (buttons, modals, slash commands)

Requires Event Subscriptions + Interactivity URL pointing to your server. Slack POSTs payloads; verify signing secret:

```js
import crypto from "crypto";
function verify(timestamp, body, signature, signingSecret) {
  const base = `v0:${timestamp}:${body}`;
  const expected = "v0=" + crypto.createHmac("sha256", signingSecret).update(base).digest("hex");
  return crypto.timingSafeEqual(Buffer.from(expected), Buffer.from(signature));
}
```

Respond within **3 seconds** or Slack times out. Return `200 OK` immediately; post follow-ups via `response_url` up to 5x within 30 minutes.

## Common gotchas

- **Channel ID vs name**: API wants IDs (`C1234567890`), not `#general`. Look up via `conversations.list`.
- **Thread replies**: pass `thread_ts`; don't reply to the user-facing formatted timestamp.
- **Rate limits**: Tier 2-4 for most methods (~20-100/min). `429` with `Retry-After` header.
- **Unfurling**: `unfurl_links: false` to suppress auto-previews when you've already included Block Kit content.

## Key reference URLs

- chat.postMessage: https://docs.slack.dev/reference/methods/chat.postMessage/
- Incoming Webhooks: https://api.slack.com/incoming-webhooks
- Block Kit reference: https://api.slack.com/reference/block-kit
- Signing secret verification: https://api.slack.com/authentication/verifying-requests-from-slack
- Web API (Node): https://docs.slack.dev/tools/node-slack-sdk/web-api/
