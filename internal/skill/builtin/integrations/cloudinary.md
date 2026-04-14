# cloudinary

> Cloudinary: image/video upload + URL-based transformation. Signed server uploads for trust; unsigned uploads with upload presets for client-side. Transformations encoded in the URL path.

<!-- keywords: cloudinary, image upload, video upload, image transformation, media cdn, cloudinary sdk, upload preset -->

**Official docs:** https://cloudinary.com/documentation  |  **Verified:** 2026-04-14 via web search.

## Install + client

```bash
pnpm add cloudinary
```

```ts
import { v2 as cloudinary } from "cloudinary";
cloudinary.config({
  cloud_name: process.env.CLOUDINARY_CLOUD_NAME,
  api_key: process.env.CLOUDINARY_API_KEY,
  api_secret: process.env.CLOUDINARY_API_SECRET,   // server-only
  secure: true,
});
```

## Server-side upload (signed)

```ts
const result = await cloudinary.uploader.upload(localPathOrUrl, {
  folder: "avatars",
  public_id: `user_${userId}`,        // stable ID you control
  overwrite: true,
  resource_type: "image",             // or "video", "raw"
  tags: ["avatar"],
  eager: [                             // generate derived assets at upload time
    { width: 128, height: 128, crop: "fill", gravity: "face" },
    { width: 512, height: 512, crop: "fill", gravity: "face" },
  ],
});
// result.secure_url, result.public_id, result.version, result.bytes, result.format
```

## Client-side upload (unsigned; uses upload preset)

Configure unsigned preset in Settings → Upload → Upload presets. Then:

```ts
const form = new FormData();
form.append("file", file);
form.append("upload_preset", "my_unsigned_preset");
const resp = await fetch(
  `https://api.cloudinary.com/v1_1/${cloudName}/image/upload`,
  { method: "POST", body: form }
);
const { secure_url, public_id } = await resp.json();
```

Unsigned presets can't pass arbitrary transformations — whitelist allowed ones in the preset.

## Signed client upload (more secure)

Server signs params; client uploads with signature:

```ts
// Server: build signature
const timestamp = Math.round(Date.now() / 1000);
const paramsToSign = { timestamp, folder: "private", public_id: `secure_${id}` };
const signature = cloudinary.utils.api_sign_request(paramsToSign, API_SECRET);
// Return { timestamp, signature, apiKey, cloudName, folder, publicId } to client.

// Client: POST to Cloudinary with those params + the file
```

## Transformation URLs

Transformations are path segments between `/upload/` and the public_id.

```
https://res.cloudinary.com/{cloud_name}/image/upload/w_400,h_400,c_fill,g_face,f_auto,q_auto/user_42.jpg
```

Common transformations:

| Token | Meaning |
|---|---|
| `w_400,h_400` | Width/height in pixels |
| `c_fill` / `c_fit` / `c_scale` / `c_thumb` | Crop mode |
| `g_face` / `g_auto` | Gravity (face detection, auto content-aware) |
| `f_auto` | Auto-pick best format (WebP/AVIF/JPEG) per browser |
| `q_auto` | Auto-optimize quality |
| `r_max` | Rounded corners (max = circle) |
| `e_blur:400` | Blur effect |
| `l_watermark,o_30` | Overlay another asset at 30% opacity |

Chain via `/` between independent transformations:

```
/image/upload/w_400/l_watermark,g_south_east,x_10,y_10/user_42.jpg
```

## Delivery URL helper

```ts
const url = cloudinary.url("user_42", {
  width: 400, height: 400, crop: "fill", gravity: "face",
  fetch_format: "auto", quality: "auto",
});
```

Prefer the helper over manual URL construction — param names change over time, helper tracks them.

## Auto-upload (proxy from origin URL)

Set up Delivery Type → Auto-upload in Settings → Upload. Then fetching `https://res.cloudinary.com/{cn}/image/upload/remote_images/{path}` auto-uploads the underlying remote asset once, caches it, and serves transformations.

## Webhooks

Configure notification URL in Settings → Upload → Notification URL. Receives POSTs for uploads, moderation, backup completion. Verify signature:

```ts
const signature = req.headers["x-cld-signature"];
const timestamp = req.headers["x-cld-timestamp"];
const expected = cloudinary.utils.verify_notification_signature(
  JSON.stringify(req.body),
  timestamp,
  signature,
  4000,   // valid-for window in seconds
);
```

## Common gotchas

- **Quality vs file size**: `q_auto` usually halves file size with imperceptible quality loss — always use it for web-delivered images.
- **`f_auto` + CDN cache**: per-browser variants are cached separately; first hit per format is a miss.
- **Video transformations are more expensive**: plan your plan limits accordingly.
- **Folder moves** don't rewrite `public_id`; use `rename`/`explicit` API or you'll break existing transformation URLs.

## Key reference URLs

- Upload API: https://cloudinary.com/documentation/image_upload_api_reference
- Upload images guide: https://cloudinary.com/documentation/upload_images
- Transformation reference: https://cloudinary.com/documentation/transformation_reference
- Node.js SDK: https://cloudinary.com/documentation/node_integration
