# Download catalog and local MinIO

English | [简体中文](download-catalog.zh-CN.md)

The generic download catalog is independent from launcher update manifests. The
production and development Control Plane Compose deployments start a local MinIO
service and enable download management by default. Direct binary launches remain
opt-in so missing storage credentials cannot accidentally enable the feature.

## Default topology

Production uses three endpoints:

- `MINIO_S3_SITE` is the public MinIO S3 API host used by browser presigned PUT
  requests.
- `DOWNLOADS_SITE` accepts only `GET` and `HEAD` under `/<bucket>/downloads/*`.
- `127.0.0.1:MINIO_CONSOLE_PORT` exposes the MinIO Console through an SSH tunnel.

Point DNS for `MINIO_S3_SITE` and `DOWNLOADS_SITE` at the Control Plane host.
Caddy obtains TLS for both names. Server-side verification, multipart completion,
and cleanup use the private `http://minio:9000` Docker endpoint, so they never
depend on Cloudflare or public hairpin routing. Never expose MinIO ports 9000 or
9001 directly in production.

`scripts/generate-control-plane-env.sh` generates separate MinIO root and
`DOWNLOAD_S3_*` application credentials. Replace the example admin, S3, and
download hostnames before deployment and retain this relationship:

```dotenv
DOWNLOADS_ENABLED=true
DOWNLOAD_S3_ENDPOINT=http://minio:9000
DOWNLOAD_S3_UPLOAD_ENDPOINT=https://s3.example.com
DOWNLOAD_S3_REGION=us-east-1
DOWNLOAD_S3_BUCKET=project-rebound-downloads
DOWNLOAD_PUBLIC_BASE_URL=https://downloads.example.com/project-rebound-downloads
DOWNLOAD_PUBLIC_PROBE_BASE_URL=http://minio:9000/project-rebound-downloads
MINIO_CORS_ALLOWED_ORIGINS=https://admin.example.com
```

`DOWNLOAD_S3_ENDPOINT` is the server-only API endpoint, while
`DOWNLOAD_S3_UPLOAD_ENDPOINT` is embedded in browser presigned URLs. Do not set
the server endpoint to the Cloudflare-proxied hostname for same-host MinIO.
`DOWNLOAD_PUBLIC_BASE_URL` remains the external redirect target, while
`DOWNLOAD_PUBLIC_PROBE_BASE_URL` lets publication verify anonymous object access
directly over the private MinIO network without a Cloudflare hairpin. Both bases
must include the bucket because the service appends the
server-generated `downloads/<item-slug>/<version-id>/<filename>` object key.

## Automatic provisioning

The repeatable `minio-provision` one-shot container:

1. creates `DOWNLOAD_S3_BUCKET`;
2. creates or updates the dedicated control-plane application user;
3. grants only the PUT, GET, DELETE, and multipart operations needed under
   `downloads/*`;
4. sets bucket PUT/HEAD CORS for the Admin Web origins and exposes `ETag`;
5. grants anonymous `GetObject` only under `downloads/`, explicitly omitting bucket
   listing permission.

Root credentials are injected only into MinIO and the provisioning container,
never into the Control Plane or Admin Web. The Admin Web CSP permits only the S3
API host, while the public download host rejects upload and management requests.
See the [MinIO CORS documentation](https://docs.min.io/aistor/administration/cors-configuration/).

## Operations

The console is loopback-only. Access it with:

```bash
ssh -L 9001:127.0.0.1:9001 user@CONTROL_HOST
```

Then open `http://127.0.0.1:9001`. Do not use root credentials as application
credentials or place `.env`, presigned URLs, or ETags in logs and tickets.

Objects live in the `minio-data` volume. A single-node volume is not a backup:
production should use locally attached redundant disks or distributed MinIO plus
off-host backups. Do not configure an expiry lifecycle for `downloads/`; archived
database rows and objects must be retained permanently. Changing MinIO endpoints
requires updating the browser upload host, external public base, internal probe
base, Admin Web CSP, and bucket CORS together.

Files larger than 64 MiB use 16 MiB parts with up to four browser workers. Sessions
expire after 24 hours. After completion, the background verifier streams the object
from MinIO and recomputes its size and SHA-256 before publication is allowed.

## Verification

Start the default deployment:

```bash
docker compose --env-file deployments/control-plane/.env \
  -f deployments/control-plane/docker-compose.yaml up -d
```

Inspect provisioning and the public catalog:

```bash
docker compose --env-file deployments/control-plane/.env \
  -f deployments/control-plane/docker-compose.yaml logs minio-provision control-plane
curl -fsS https://api.example.com/v1/downloads
```

Use a dedicated test bucket for real storage tests. Set
`TEST_DOWNLOAD_S3_ENDPOINT`, `TEST_DOWNLOAD_S3_BUCKET`,
`TEST_DOWNLOAD_S3_ACCESS_KEY_ID`, and `TEST_DOWNLOAD_S3_SECRET_ACCESS_KEY`;
`TEST_DOWNLOAD_S3_REGION`, `TEST_DOWNLOAD_S3_UPLOAD_ENDPOINT`, and
`TEST_DOWNLOAD_S3_PUBLIC_BASE_URL` are optional.
Run:

```bash
go test ./internal/download -run TestS3StorageAgainstCompatibleService -count=1
```

The test only creates and removes objects under `downloads/integration/`.
