# Download catalog storage

English | [简体中文](download-catalog.zh-CN.md)

The generic download catalog is independent of the launcher update manifest. It is
disabled by default. Create an S3-compatible bucket and public CDN hostname before
setting `DOWNLOADS_ENABLED=true`. Use credentials restricted to the server-generated
object prefix and keep both credential values outside YAML and logs.

Required environment values are `DOWNLOAD_S3_ENDPOINT`, `DOWNLOAD_S3_REGION`,
`DOWNLOAD_S3_BUCKET`, `DOWNLOAD_S3_ACCESS_KEY_ID`,
`DOWNLOAD_S3_SECRET_ACCESS_KEY`, and `DOWNLOAD_PUBLIC_BASE_URL`. R2 normally uses
region `auto`; AWS S3 uses its actual region; MinIO uses the endpoint reachable by
both the control plane and administrator browsers. The public base URL must map the
same object keys to immutable objects and support `HEAD`, `GET`, and byte ranges.

## Bucket CORS

The browser uploads directly to presigned S3 URLs. Configure bucket CORS to allow
the exact Admin Web origins, methods `PUT` and `HEAD`, request headers `*` (or all
AWS signing/content headers), and expose `ETag`. Do not use a wildcard origin with
credentials. See the [Cloudflare R2 CORS guide](https://developers.cloudflare.com/r2/buckets/cors/).
A minimal R2-style policy is:

```json
[
  {
    "AllowedOrigins": ["https://admin.project-rebound.space"],
    "AllowedMethods": ["PUT", "HEAD"],
    "AllowedHeaders": ["*"],
    "ExposeHeaders": ["ETag"],
    "MaxAgeSeconds": 3600
  }
]
```

## Lifecycle and recovery

Files over 64 MiB use 16 MiB multipart parts with at most four concurrent browser
requests. Sessions expire after 24 hours; the worker aborts expired multipart uploads.
The browser can resume uploaded parts, and it can renew the signed request for a small
single-PUT upload. After completion, the worker streams the object and verifies size
and SHA-256 before allowing publication.

Single-PUT requests carry `If-None-Match: *`, so a still-valid signature cannot replace
an object after its first successful creation. Completed multipart upload IDs can no
longer accept parts or be completed again. Do not grant operators or unrelated services
unconditional overwrite permission on the catalog prefix. Provider-side versioning or
object lock is useful additional defense where the selected provider supports it.
See the [Amazon S3 conditional-write guide](https://docs.aws.amazon.com/AmazonS3/latest/userguide/conditional-writes.html)
for the underlying precondition semantics.

Publishing and archiving require `downloads.publish` or `downloads.archive`, an
operation reason, and current-session MFA step-up. Archived database rows and objects
are retained permanently. Confirm bucket lifecycle rules do not delete the catalog
prefix, and include download tables plus storage credentials in backup/secret-rotation
procedures. Audit records contain identifiers and state changes only—never credentials,
presigned URLs, ETags, or uploaded content.

For PostgreSQL lifecycle integration tests, set `TEST_DATABASE_URL`. To exercise
real single and multipart object operations against MinIO/R2/S3, also set
`TEST_DOWNLOAD_S3_ENDPOINT`, `TEST_DOWNLOAD_S3_BUCKET`,
`TEST_DOWNLOAD_S3_ACCESS_KEY_ID`, `TEST_DOWNLOAD_S3_SECRET_ACCESS_KEY`, and optionally
`TEST_DOWNLOAD_S3_REGION`/`TEST_DOWNLOAD_S3_PUBLIC_BASE_URL`, then run
`go test ./internal/download -count=1`. Use an empty dedicated test bucket because
the test creates and removes objects under `downloads/integration/`.
