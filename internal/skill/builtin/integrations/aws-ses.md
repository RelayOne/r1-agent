# aws-ses

> Amazon Simple Email Service. Cheapest high-volume email in the industry (~$0.10/1000 emails). SMTP or HTTP API. Requires domain verification + (in some regions) production-access approval.

<!-- keywords: ses, amazon ses, aws ses, email, bulk email, smtp, sesv2 -->

**Official docs:** https://docs.aws.amazon.com/ses  |  **Verified:** 2026-04-14 (SESv2 current).

## Regions + auth

- SES is regional: `email.us-east-1.amazonaws.com`, `email.eu-west-1.amazonaws.com`, etc. Pick the closest to senders.
- Auth: AWS Signature v4 (use `@aws-sdk/client-sesv2`); IAM role on EC2/Lambda, or access key pair.
- API v2 is current; v1 still works but no new features.

## Install + init

```bash
pnpm add @aws-sdk/client-sesv2
```

```ts
import { SESv2Client, SendEmailCommand } from "@aws-sdk/client-sesv2";
const ses = new SESv2Client({ region: "us-east-1" });
```

## Send email

```ts
await ses.send(new SendEmailCommand({
  FromEmailAddress: "noreply@yourdomain.com",
  Destination: { ToAddresses: ["user@example.com"] },
  Content: {
    Simple: {
      Subject: { Data: "Welcome" },
      Body: {
        Text: { Data: "..." },
        Html: { Data: "<p>...</p>" },
      },
    },
  },
  ConfigurationSetName: "default-config",    // for tracking events → SNS/Kinesis
  EmailTags: [{ Name: "category", Value: "welcome" }],
}));
```

## Template sending

```ts
// One-shot with template
await ses.send(new SendEmailCommand({
  FromEmailAddress: "noreply@yourdomain.com",
  Destination: { ToAddresses: ["user@example.com"] },
  Content: { Template: {
    TemplateName: "welcome-template",
    TemplateData: JSON.stringify({ name: "Alex" }),
  }},
}));

// Bulk template via SendBulkEmailCommand (up to 50 destinations)
```

Templates created via `CreateEmailTemplate` API with Handlebars-style `{{name}}`.

## Sandbox → production

Fresh SES accounts start in **sandbox mode**: can only send to verified addresses; quota 200/day. Request production access in AWS Console → SES → Account Dashboard → "Request production access" — typically approved in 24h with a clear description of your volume + opt-in source.

## Domain verification + DKIM

```ts
// Verify domain
await ses.send(new PutEmailIdentityCommand({
  EmailIdentity: "yourdomain.com",
  DkimSigningAttributes: { NextSigningKeyLength: "RSA_2048_BIT" },
}));
// Returns CNAME records to add to DNS. DKIM must be enabled for good deliverability.
```

SPF + DKIM + DMARC → configure all three. SES auto-provides DKIM if you use the `PutEmailIdentity` with signing attributes.

## Bounce + complaint handling (REQUIRED for good sender reputation)

Configuration sets route events (send, delivery, bounce, complaint, open, click) to SNS, Kinesis Firehose, or CloudWatch.

```ts
await ses.send(new PutConfigurationSetEventDestinationCommand({
  ConfigurationSetName: "default-config",
  EventDestinationName: "bounce-sns",
  EventDestination: {
    Enabled: true,
    MatchingEventTypes: ["BOUNCE", "COMPLAINT", "REJECT"],
    SnsDestination: { TopicArn: "arn:aws:sns:..." },
  },
}));
```

Subscribe to the SNS topic and remove bounce/complaint addresses from your list. Failure to do this → AWS pauses your sending.

## Common gotchas

- **Sandbox silent limits**: new accounts throttle at 1 msg/second, 200/day. Request quota increase along with production access.
- **From address must be verified** (identity OR domain). Unverified → 554.
- **Reputation dashboard** in console — bounce >5% or complaint >0.1% and AWS will pause your account. Keep hygiene aggressive.
- **Configuration set name must be explicitly set** on every `SendEmail` call to get tracking events. Not global default.

## Key reference URLs

- SESv2 API: https://docs.aws.amazon.com/ses/latest/APIReference-V2/
- Sending email: https://docs.aws.amazon.com/ses/latest/dg/send-email.html
- Domain verification: https://docs.aws.amazon.com/ses/latest/dg/creating-identities.html
- Event publishing (bounce/complaint): https://docs.aws.amazon.com/ses/latest/dg/monitor-sending-activity.html
- Getting production access: https://docs.aws.amazon.com/ses/latest/dg/request-production-access.html
