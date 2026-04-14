# discord

> Discord bot API + webhooks. Bot tokens for interactive apps; webhooks for one-way notifications. Interaction Gateway (WebSocket) for real-time; HTTP REST for send-only.

<!-- keywords: discord, discord bot, discord webhook, discord api, interactions -->

**Official docs:** https://discord.com/developers/docs  |  **Verified:** 2026-04-14.

## Token types

- **Bot token** (`Bot ...`): for a full bot that can DM, react, manage roles, receive gateway events. Full Discord app.
- **Webhook URL**: for posting messages to ONE channel. No interactivity. No replies.
- **OAuth2 user tokens**: for acting on behalf of a Discord user (e.g. "connect Discord" in a dashboard).

## Incoming webhooks (simplest)

Create via channel settings → Integrations → Webhooks → New Webhook → copy URL.

```js
await fetch(WEBHOOK_URL, {
  method: "POST",
  headers: { "Content-Type": "application/json" },
  body: JSON.stringify({
    content: "Deploy complete: `v1.4.2`",
    username: "DeployBot",
    avatar_url: "https://...",
    embeds: [{
      title: "Release v1.4.2",
      description: "Changelog: ...",
      color: 0x00ff00,
      fields: [{ name: "Env", value: "prod", inline: true }],
      timestamp: new Date().toISOString(),
    }],
  }),
});
```

## Bot: send message via REST

```
POST https://discord.com/api/v10/channels/{channel_id}/messages
Authorization: Bot <BOT_TOKEN>
Content-Type: application/json

{ "content": "Hello", "embeds": [...] }
```

## Bot: slash commands (Interactions API)

Register commands:

```
PUT /applications/{app_id}/guilds/{guild_id}/commands    (guild-scoped, instant)
PUT /applications/{app_id}/commands                       (global, 1h propagation)

Body: [{ "name": "status", "description": "Check deploy status", "options": [...] }]
```

Receive interactions: Discord POSTs to your configured "Interactions Endpoint URL" for every slash command invocation. **Must respond within 3 seconds.**

```js
import nacl from "tweetnacl";
// Verify signature
const sig = req.headers["x-signature-ed25519"];
const ts = req.headers["x-signature-timestamp"];
const verified = nacl.sign.detached.verify(
  Buffer.from(ts + rawBody),
  Buffer.from(sig, "hex"),
  Buffer.from(PUBLIC_KEY, "hex"),
);
if (!verified) return res.status(401).end();

if (body.type === 1) return res.json({ type: 1 });  // PING
if (body.type === 2) {                                 // APPLICATION_COMMAND
  return res.json({ type: 4, data: { content: "Deploying..." } });
  // Or defer with type: 5, then follow-up via webhook URL within 15 min
}
```

## Gateway (WebSocket) for live events

Use the official SDK (`discord.js`) unless you have a reason to roll your own — gateway handshake + heartbeat + reconnection is fiddly.

```ts
import { Client, GatewayIntentBits } from "discord.js";
const client = new Client({ intents: [GatewayIntentBits.Guilds, GatewayIntentBits.GuildMessages] });
client.on("ready", () => console.log("Logged in as", client.user?.tag));
client.on("messageCreate", msg => { if (msg.content === "!ping") msg.reply("pong"); });
await client.login(BOT_TOKEN);
```

## Rate limits

- Per-route + global limits. Response headers `X-RateLimit-*` + `Retry-After` on 429.
- `discord.js` handles automatically; if rolling own, respect `Retry-After` with jitter.

## Common gotchas

- **3-second interaction timeout**: acknowledge with deferred response (type 5) if work takes longer, then update via follow-up.
- **Global commands cache for up to 1 hour** — test with guild-scoped commands (instant).
- **Ed25519 signature verification is REQUIRED** — Discord rejects apps whose endpoint fails verification even once.
- **Message content intent** (privileged): required to read message bodies; must be enabled in Developer Portal.

## Key reference URLs

- Interactions: https://discord.com/developers/docs/interactions/overview
- Webhooks: https://discord.com/developers/docs/resources/webhook
- Slash commands: https://discord.com/developers/docs/interactions/application-commands
- discord.js guide: https://discordjs.guide/
- Signature verification: https://discord.com/developers/docs/interactions/receiving-and-responding#security-and-authorization
