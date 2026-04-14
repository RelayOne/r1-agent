# aws-s3

> AWS S3 with SDK v3 (`@aws-sdk/client-s3`, `@aws-sdk/s3-request-presigner`). Presigned URLs for direct browser uploads; server-side SDK calls for internal transfers.

<!-- keywords: s3, aws s3, aws-sdk, presigned url, object storage, bucket, putobject, getobject, multipart upload -->

**Official docs:** https://docs.aws.amazon.com/AmazonS3  |  **Verified:** 2026-04-14 via web search.

## Install

```bash
pnpm add @aws-sdk/client-s3 @aws-sdk/s3-request-presigner
```

## Client init

```ts
import { S3Client } from "@aws-sdk/client-s3";
const s3 = new S3Client({
  region: "us-east-1",
  credentials: {
    accessKeyId: process.env.AWS_ACCESS_KEY_ID!,
    secretAccessKey: process.env.AWS_SECRET_ACCESS_KEY!,
    // Or use default provider chain (IAM role on EC2/Lambda/ECS).
  },
});
```

In production on AWS, prefer **IAM role** credentials (no keys in env) — attach an IAM role to the Lambda/ECS task/EC2 instance with only the S3 permissions it needs.

## Upload (server-side)

```ts
import { PutObjectCommand } from "@aws-sdk/client-s3";

await s3.send(new PutObjectCommand({
  Bucket: "my-bucket",
  Key: "uploads/user-42/avatar.png",
  Body: fileBuffer,
  ContentType: "image/png",
  CacheControl: "public, max-age=31536000, immutable",
  Metadata: { "user-id": "42" },     // x-amz-meta-* headers
}));
```

## Presigned URL (browser uploads direct to S3)

```ts
import { getSignedUrl } from "@aws-sdk/s3-request-presigner";
import { PutObjectCommand } from "@aws-sdk/client-s3";

const url = await getSignedUrl(
  s3,
  new PutObjectCommand({
    Bucket: "my-bucket",
    Key: `uploads/${userId}/${fileId}`,
    ContentType: "image/png",
  }),
  { expiresIn: 300 },    // seconds; max 604800 (7 days)
);
// Client then: await fetch(url, { method: "PUT", body: file, headers: { "Content-Type": "image/png" } });
```

**Critical:** the client's PUT must send the EXACT same `Content-Type` the URL was signed with. Mismatch → 403 SignatureDoesNotMatch.

## Download / read

```ts
import { GetObjectCommand } from "@aws-sdk/client-s3";

const resp = await s3.send(new GetObjectCommand({ Bucket, Key }));
const bytes = await resp.Body?.transformToByteArray();
// OR presign a GET for temporary browser access to a private object:
const url = await getSignedUrl(s3, new GetObjectCommand({ Bucket, Key }), { expiresIn: 60 });
```

## Multipart upload (files > 5GB or resumable uploads)

```ts
import { Upload } from "@aws-sdk/lib-storage";

const upload = new Upload({
  client: s3,
  params: { Bucket, Key, Body: readStream, ContentType },
  queueSize: 4,       // parallel parts
  partSize: 1024 * 1024 * 8,   // 8MB per part (min 5MB except last)
  leavePartsOnError: false,
});
upload.on("httpUploadProgress", (p) => console.log(p.loaded, "/", p.total));
await upload.done();
```

For browser-driven resumable uploads, presign `UploadPartCommand`s per part and reassemble via `CompleteMultipartUploadCommand` server-side.

## Bucket configuration essentials

- **Block public access** at the bucket level (unless serving truly public content).
- **Enable versioning** on user-content buckets — recover from accidental delete/overwrite.
- **Encryption**: SSE-S3 (managed) or SSE-KMS (per-key audit). Enabled by default for new buckets since 2023.
- **Lifecycle rules**: expire multipart uploads > 7 days (otherwise you pay for orphan parts), transition to Glacier for cold archives.
- **CORS**: required if browsers upload/download directly. Allow only your origin + `Content-Type` header.

```xml
<CORSRule>
  <AllowedOrigin>https://app.example.com</AllowedOrigin>
  <AllowedMethod>PUT</AllowedMethod>
  <AllowedMethod>GET</AllowedMethod>
  <AllowedHeader>*</AllowedHeader>
  <MaxAgeSeconds>3600</MaxAgeSeconds>
</CORSRule>
```

## Common gotchas

- **Cross-account access with KMS**: need both the bucket policy AND KMS key policy to grant access; missing KMS permission gives "access denied" that looks like S3's fault.
- **Presigned URL max lifetime = 7 days** for signature v4 with long-term creds; but IAM role creds can be shorter (capped at session duration).
- **Eventual consistency gotcha (historical)**: as of 2020 S3 is strong read-after-write consistent; no more "LIST may not show the object yet." You can ignore older advice on this.
- **List operations are expensive**: prefer path-prefix `Prefix` filter to narrow results.

## Key reference URLs

- Presigned URL upload: https://docs.aws.amazon.com/AmazonS3/latest/userguide/PresignedUrlUploadObject.html
- Multipart upload: https://docs.aws.amazon.com/AmazonS3/latest/userguide/mpuoverview.html
- SDK v3 quickstart: https://docs.aws.amazon.com/sdk-for-javascript/v3/developer-guide/getting-started.html
- CORS: https://docs.aws.amazon.com/AmazonS3/latest/userguide/cors.html
