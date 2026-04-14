# microsoft-teams

> Microsoft Teams: chat, meetings, channels. Programmatic access via Microsoft Graph API (messages, channels, teams) or Incoming Webhooks (one-way channel notifications). Bot Framework for interactive bots.

<!-- keywords: microsoft teams, teams, microsoft graph, incoming webhook, bot framework -->

**Official docs:** https://learn.microsoft.com/en-us/microsoftteams/platform/  |  **Verified:** 2026-04-14.

## Three integration paths

1. **Incoming Webhook**: post to one channel. Simplest. Deprecated for tenants using "Workflows" — classic connectors being phased out for workflow-based webhooks.
2. **Microsoft Graph**: full Teams API — list channels, send messages, read membership, etc. Requires Azure AD app + OAuth2.
3. **Bot Framework / Teams Toolkit**: interactive bots, tabs, adaptive cards, message extensions.

## Incoming Webhook (classic — verify not deprecated in your tenant)

Channel → Connectors → Incoming Webhook → copy URL. Post JSON:

```ts
await fetch(WEBHOOK_URL, {
  method: "POST",
  headers: { "Content-Type": "application/json" },
  body: JSON.stringify({
    "@type": "MessageCard",
    "@context": "https://schema.org/extensions",
    themeColor: "0076D7",
    title: "Deploy complete",
    text: "v1.4.2 shipped to prod",
    sections: [{ facts: [{ name: "Env", value: "prod" }, { name: "Duration", value: "4m" }] }],
  }),
});
```

**Heads up (2025):** Microsoft announced retirement of "Office 365 Connectors" — migrate to **Workflows** (Power Automate) triggered by HTTP POST. Workflows webhook URLs replace classic connectors.

## Workflows webhook (recommended replacement)

In Teams channel: "Workflows" → "Post to a channel when a webhook request is received" → get Flow URL. Payload format uses Adaptive Cards:

```json
{
  "type": "message",
  "attachments": [{
    "contentType": "application/vnd.microsoft.card.adaptive",
    "content": {
      "$schema": "http://adaptivecards.io/schemas/adaptive-card.json",
      "type": "AdaptiveCard",
      "version": "1.4",
      "body": [{ "type": "TextBlock", "text": "Deploy complete" }]
    }
  }]
}
```

## Microsoft Graph — send channel message

```
POST https://graph.microsoft.com/v1.0/teams/{team-id}/channels/{channel-id}/messages
Authorization: Bearer <access-token>

{ "body": { "content": "Hello" } }
```

Scopes: `ChannelMessage.Send` (delegated) or `ChannelMessage.Send.Group` (application, resource-specific consent).

## OAuth2 (Azure AD)

```
GET https://login.microsoftonline.com/{tenant}/oauth2/v2.0/authorize
  ?client_id=...&response_type=code&redirect_uri=...&scope=https://graph.microsoft.com/.default
```

Client credentials flow for daemon apps; authorization code for user-context. Token endpoint: `/oauth2/v2.0/token`.

## Adaptive Cards (interactive)

Adaptive Cards run in Teams, Outlook, Windows. Designer: https://adaptivecards.io/designer/. Use for rich interactive notifications — action buttons, inputs, images.

## Bot Framework (interactive bot)

```ts
import { TeamsActivityHandler, TurnContext } from "botbuilder";
class MyBot extends TeamsActivityHandler {
  constructor() {
    super();
    this.onMessage(async (ctx, next) => {
      await ctx.sendActivity(`Echo: ${ctx.activity.text}`);
      await next();
    });
  }
}
```

Host on Azure Bot Service or self-host with ngrok for dev. Manifest uploaded via Developer Portal in Teams.

## Common gotchas

- **Incoming webhooks (classic connectors) are deprecated** — migrate to Workflows. Rollout through 2025; verify your tenant's effective date.
- **Graph app permissions require tenant admin consent** — `application` scope apps cannot just grant themselves access.
- **Resource-Specific Consent (RSC)** is the modern path for per-team app permissions — Teams admin consents per team rather than tenant-wide.
- **Rate limits: 4 messages/sec per app per channel**; 30 req/sec tenant-wide for chat messages.
- **Adaptive Cards version matters** — Teams supports up to 1.5 in most surfaces; 1.6+ features may not render.

## Key reference URLs

- Graph Teams API: https://learn.microsoft.com/en-us/graph/api/resources/teams-api-overview
- Workflows webhook: https://support.microsoft.com/en-us/office/post-a-workflow-when-a-webhook-request-is-received-in-microsoft-teams-8ae491c7-0394-4861-ba59-055e33f75498
- Adaptive Cards: https://adaptivecards.io/
- Bot Framework: https://learn.microsoft.com/en-us/microsoftteams/platform/bots/what-are-bots
- Connectors retirement: https://devblogs.microsoft.com/microsoft365dev/retirement-of-office-365-connectors-within-microsoft-teams/
