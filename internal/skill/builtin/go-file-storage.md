# go-file-storage

> Production file handling in Go with R2, GCS, Cloudflare CDN: upload security, presigned URLs, image/video processing, and storage cost optimization

<!-- keywords: file, upload, storage, r2, gcs, s3, cloudflare, cdn, image, video, presigned, multipart, go -->

## When to Use
- Building file upload endpoints in Go
- Integrating with R2, GCS, or S3-compatible storage
- Processing images or videos (thumbnailing, transcoding, format conversion)
- Implementing presigned URL workflows for direct-to-storage uploads
- Designing content moderation or document processing pipelines

## When NOT to Use
- Static asset bundling at build time (use embedded FS or CI artifacts)
- Small config files stored in environment variables or secrets
- Database blob storage for sub-1MB payloads

## Behavioral Guidance

### Upload Security: Defense in Depth

**Never trust file extensions or Content-Type headers.** Use `gabriel-vasile/mimetype` for magic-byte detection (200+ formats, pure Go, no CGO):

```go
mtype, err := mimetype.DetectReader(r) // reads first 3072 bytes
if err != nil { return fmt.Errorf("detection failed: %w", err) }
for _, a := range allowed {
    if mtype.Is(a) { return nil }
}
return fmt.Errorf("disallowed type: %s", mtype.String())
```

**Enforce size at three layers:** Cloudflare (100MB Free), reverse proxy (`client_max_body_size`), application (`http.MaxBytesReader`).

```go
r.Body = http.MaxBytesReader(w, r.Body, 10<<20) // 10 MB
```

**Never use user-provided filenames for storage paths.** Generate server-side keys:

```go
func GenerateObjectKey(ext string) string {
    b := make([]byte, 16)
    crypto_rand.Read(b)
    return hex.EncodeToString(b) + ext
}
```

**Image re-encoding strips payloads.** Pass untrusted images through Go's native `image.Decode` then re-encode. This neutralizes ImageTragick-class exploits without ImageMagick.

**Zip bomb detection:** Check compression ratio (flag >100:1), enforce max decompressed size with `io.LimitReader`, limit nesting depth to 2-3, use `filepath.IsLocal()` on every entry name.

### Presigned URLs for Direct Upload

For files over a few MB, presigned URLs let clients upload directly to storage:

```go
// R2 via aws-sdk-go-v2
presignClient := s3.NewPresignClient(r2Client)
result, _ := presignClient.PresignPutObject(ctx, &s3.PutObjectInput{
    Bucket:      aws.String(bucket),
    Key:         aws.String(key),
    ContentType: aws.String("image/jpeg"),
}, s3.WithPresignExpires(15*time.Minute))
```

Set 15 minutes for uploads, 1 hour for downloads. Include `Content-Type` in signature to restrict upload types. Configure CORS on the bucket.

### Content-Addressable Storage

Store files keyed by SHA-256 hash. Set `Cache-Control: public, max-age=31536000, immutable`. New content gets a new URL, so cache purging is never needed:

```go
h := sha256.New()
io.Copy(h, content)
hash := hex.EncodeToString(h.Sum(nil))
key := fmt.Sprintf("objects/%s/%s", hash[:2], hash)
```

### Image Processing Libraries

| Library | CGO | WebP/AVIF | Speed | Use Case |
|---------|-----|-----------|-------|----------|
| `disintegration/imaging` | No | No | 1x | Low volume, simple ops |
| `h2non/bimg` | Yes (libvips) | Yes | ~5x | High throughput, simple API |
| `davidbyttow/govips` v2 | Yes (libvips) | Yes | ~5x | Advanced pipelines, full control |

Use `govips` for production. Set `MALLOC_ARENA_MAX=2` in libvips containers to prevent memory fragmentation.

### Video Processing

Use `exec.Command` for FFmpeg integration -- no abstraction leakage, matches FFmpeg docs directly. Use `hibiken/asynq` (Redis-backed) for async task queues. Limit concurrent FFmpeg processes to CPU core count.

### Storage Abstraction

Abstract behind a `FileStore` interface for testability:

```go
type FileStore interface {
    Upload(ctx context.Context, key string, r io.Reader, meta ObjectMeta) error
    Download(ctx context.Context, key string) (io.ReadCloser, *ObjectMeta, error)
    Delete(ctx context.Context, key string) error
    SignURL(ctx context.Context, key string, opts SignedURLOptions) (string, error)
}
```

Implement `R2Store` (aws-sdk-go-v2), `GCSStore` (cloud.google.com/go/storage). Use `gocloud.dev/blob` if you want a ready-made multi-provider abstraction.

### Testing

- **MinIO** via `testcontainers-go/modules/minio` for S3/R2-compatible local testing
- **`fsouza/fake-gcs-server`** embeds in Go tests with `fakestorage.NewServer()`
- Unit tests use interface mocks; integration tests spin up containers

### Resilience

Wrap storage calls in `sony/gobreaker` v2 (circuit breaker) with `cenkalti/backoff/v4` (exponential retry). Exclude 4xx from failure counts.

## Gotchas
- **Multipart upload cleanup.** Incomplete multipart uploads accumulate in S3/R2 and cost storage. Set lifecycle rules to abort incomplete uploads after 24 hours.
- **R2 vs S3 API differences.** `aws-sdk-go-v2` v1.73.0+ changed default checksum behavior that is incompatible with R2. Pin to v1.72.x or set `RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired`.
- **Signed URL expiry clock skew.** If server and client clocks differ, URLs expire early or appear valid when they should not. Use 15-minute minimum expiry for uploads.
- **Streaming uploads with `io.Pipe` deadlock.** If the reader goroutine panics or returns error without closing the pipe, the writer blocks forever. Always defer `pw.Close()` and handle errors on both sides.
- **`http.MaxBytesReader` does not apply to multipart parts.** It limits the total body but individual parts can still be large. Validate each part size separately when parsing multipart forms.
- **GCS signed URLs have 7-day max expiry.** S3/R2 presigned URLs max at 7 days with IAM user creds, 12 hours with STS.
- **R2 does not support object versioning** (as of 2026). Implement application-level versioning with suffixed keys if needed.
- **LibreOffice is single-threaded per user profile.** For concurrent document conversion, use one container per conversion or isolated HOME directories.
- **Content-Length header can be spoofed.** Check it for fast rejection but never rely on it as the sole size enforcement.
- **SVG XXE attacks.** SVGs are XML -- `encoding/xml` disables external entities by default, but strip `<script>` tags and event handlers, or convert to raster before serving.
- **Range requests need explicit server-side support.** R2/S3 support range requests natively, but custom download endpoints must parse the `Range` header and return `206 Partial Content` with correct `Content-Range`. Without this, resumable downloads and video seeking break silently.
- **CSAM scanning must be first in any moderation pipeline.** US law (18 USC 2258A) mandates reporting to NCMEC with fines up to $300K per violation.
