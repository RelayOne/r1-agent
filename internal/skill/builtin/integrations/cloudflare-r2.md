# cloudflare-r2

> Cloudflare R2: S3-compatible object storage with zero egress fees. Same API surface as AWS S3, so S3 SDKs work with endpoint override. Presigned URLs, multipart upload, event notifications, CORS.

<!-- keywords: cloudflare r2, r2, s3 alternative, object storage, zero egress -->

**Official docs:** https://developers.cloudflare.com/r2  |  **Verified:** 2026-04-14.

## Auth + endpoint

- S3 API endpoint: `https://<ACCOUNT_ID>.r2.cloudflarestorage.com`
- Credentials: Access Key ID + Secret Access Key created in Dashboard → R2 → Manage R2 API Tokens.
- Region: always `auto` for R2 (uses `us-east-1` for signing).

```ts
import { S3Client, PutObjectCommand } from "@aws-sdk/client-s3";
const r2 = new S3Client({
  region: "auto",
  endpoint: `https://${ACCOUNT_ID}.r2.cloudflarestorage.com`,
  credentials: { accessKeyId: KEY_ID, secretAccessKey: SECRET },
});
```

## Basic ops (S3 SDK — same as S3)

```ts
await r2.send(new PutObjectCommand({
  Bucket: "uploads",
  Key: "user-42/avatar.png",
  Body: fileBuffer,
  ContentType: "image/png",
}));

const getRes = await r2.send(new GetObjectCommand({ Bucket, Key }));
```

## Presigned URLs (direct-from-browser upload)

```ts
import { getSignedUrl } from "@aws-sdk/s3-request-presigner";
const url = await getSignedUrl(r2, new PutObjectCommand({ Bucket, Key, ContentType: "image/png" }), { expiresIn: 3600 });
// frontend PUTs file directly to `url` — bytes never hit your server
```

## Public access (custom domain)

Dashboard → R2 bucket → Settings → Public Access → connect custom domain. Objects then served at `https://assets.yourdomain.com/key`. No egress fees.

For private objects: sign URLs or proxy through Workers.

## Workers integration (native R2 binding — no API keys)

```toml
# wrangler.toml
[[r2_buckets]]
binding = "MY_BUCKET"
bucket_name = "uploads"
```

```ts
export default {
  async fetch(req, env) {
    const obj = await env.MY_BUCKET.get("avatar.png");
    return new Response(obj.body);
  }
};
```

Bindings avoid API key management + are faster than hitting the S3 endpoint.

## Event notifications

Configure via dashboard or API to stream bucket events (create, delete) to a Workers queue or external webhook. Use for cache invalidation, image processing pipelines, etc.

## CORS

```ts
await r2.send(new PutBucketCorsCommand({
  Bucket: "uploads",
  CORSConfiguration: {
    CORSRules: [{
      AllowedOrigins: ["https://app.example.com"],
      AllowedMethods: ["GET", "PUT"],
      AllowedHeaders: ["*"],
      MaxAgeSeconds: 3600,
    }],
  },
}));
```

## Common gotchas

- **`region: "auto"`** — if you set `us-east-1` the SDK sometimes omits region from signed URLs and presigned PUTs 403.
- **No egress fees but operations cost** — Class A (writes) $4.50/million, Class B (reads) $0.36/million. Storage $0.015/GB-month.
- **Multipart upload minimum part size** is 5 MB (same as S3) except for the last part.
- **Max file size** via single PUT: 5 GB. Beyond that, multipart is required.
- **No S3 Select, Lifecycle with storage tiers, Intelligent-Tiering, Glacier** — R2 is flat storage only. If you need lifecycle archival, S3 Glacier still wins.
- **API tokens are account-scoped by default** — scope to specific buckets via token policy in dashboard.

## Key reference URLs

- R2 overview: https://developers.cloudflare.com/r2/
- S3 API compatibility: https://developers.cloudflare.com/r2/api/s3/api/
- Workers bindings: https://developers.cloudflare.com/r2/api/workers/workers-api-reference/
- Pricing: https://developers.cloudflare.com/r2/pricing/
