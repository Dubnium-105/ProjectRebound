#!/usr/bin/env sh
set -eu

required_variables="MINIO_ENDPOINT MINIO_ROOT_USER MINIO_ROOT_PASSWORD DOWNLOAD_S3_BUCKET DOWNLOAD_S3_ACCESS_KEY_ID DOWNLOAD_S3_SECRET_ACCESS_KEY MINIO_CORS_ALLOWED_ORIGINS"
for variable_name in $required_variables; do
  eval "variable_value=\${$variable_name:-}"
  if [ -z "$variable_value" ]; then
    printf 'Missing required MinIO provisioning variable: %s\n' "$variable_name" >&2
    exit 1
  fi
done

case "$DOWNLOAD_S3_BUCKET" in
  ''|*[!a-z0-9.-]*|.*|*.|-*|*-)
    printf 'Invalid MinIO bucket name: %s\n' "$DOWNLOAD_S3_BUCKET" >&2
    exit 1
    ;;
esac
if [ "${#DOWNLOAD_S3_BUCKET}" -lt 3 ] || [ "${#DOWNLOAD_S3_BUCKET}" -gt 63 ]; then
  printf 'Invalid MinIO bucket name length.\n' >&2
  exit 1
fi
case "$DOWNLOAD_S3_ACCESS_KEY_ID" in
  ''|*[!A-Za-z0-9._-]*)
    printf 'Invalid MinIO application access key.\n' >&2
    exit 1
    ;;
esac
if [ "${#DOWNLOAD_S3_ACCESS_KEY_ID}" -lt 3 ] || [ "${#DOWNLOAD_S3_ACCESS_KEY_ID}" -gt 64 ]; then
  printf 'Invalid MinIO application access key length.\n' >&2
  exit 1
fi
if [ "$MINIO_ROOT_USER" = "$DOWNLOAD_S3_ACCESS_KEY_ID" ] || [ "$MINIO_ROOT_PASSWORD" = "$DOWNLOAD_S3_SECRET_ACCESS_KEY" ]; then
  printf 'MinIO root and application credentials must be different.\n' >&2
  exit 1
fi

alias_name="rebound-local"
policy_name="project-rebound-download-writer"
policy_file="/tmp/download-writer-policy.json"
public_policy_file="/tmp/download-public-policy.json"
cors_file="/tmp/download-cors.xml"

mc alias set "$alias_name" "$MINIO_ENDPOINT" "$MINIO_ROOT_USER" "$MINIO_ROOT_PASSWORD" >/dev/null
mc ready "$alias_name"
mc mb --ignore-existing "$alias_name/$DOWNLOAD_S3_BUCKET"

{
  printf '%s\n' \
    '{' \
    '  "Version": "2012-10-17",' \
    '  "Statement": [' \
    '    {' \
    '      "Effect": "Allow",' \
    '      "Action": [' \
    '        "s3:PutObject",' \
    '        "s3:GetObject",' \
    '        "s3:DeleteObject",' \
    '        "s3:AbortMultipartUpload",' \
    '        "s3:ListMultipartUploadParts"' \
    '      ],' \
    '      "Resource": ['
  printf '        "arn:aws:s3:::%s/downloads/*"\n' "$DOWNLOAD_S3_BUCKET"
  printf '%s\n' \
    '      ]' \
    '    }' \
    '  ]' \
    '}'
} >"$policy_file"

mc admin user add "$alias_name" "$DOWNLOAD_S3_ACCESS_KEY_ID" "$DOWNLOAD_S3_SECRET_ACCESS_KEY" >/dev/null
mc admin policy create "$alias_name" "$policy_name" "$policy_file" >/dev/null
mc admin policy attach "$alias_name" "$policy_name" --user "$DOWNLOAD_S3_ACCESS_KEY_ID" >/dev/null

{
  printf '%s\n' '<CORSConfiguration>' '  <CORSRule>'
  previous_ifs="$IFS"
  IFS=','
  for origin in $MINIO_CORS_ALLOWED_ORIGINS; do
    case "$origin" in
      http://*|https://*) ;;
      *)
        printf 'Invalid MinIO CORS origin: %s\n' "$origin" >&2
        exit 1
        ;;
    esac
    case "$origin" in
      *[!A-Za-z0-9:/._-]*)
        printf 'Invalid characters in MinIO CORS origin: %s\n' "$origin" >&2
        exit 1
        ;;
    esac
    printf '    <AllowedOrigin>%s</AllowedOrigin>\n' "$origin"
  done
  IFS="$previous_ifs"
  printf '%s\n' \
    '    <AllowedMethod>PUT</AllowedMethod>' \
    '    <AllowedMethod>HEAD</AllowedMethod>' \
    '    <AllowedHeader>*</AllowedHeader>' \
    '    <ExposeHeader>ETag</ExposeHeader>' \
    '    <MaxAgeSeconds>3600</MaxAgeSeconds>' \
    '  </CORSRule>' \
    '</CORSConfiguration>'
} >"$cors_file"

mc cors set "$alias_name/$DOWNLOAD_S3_BUCKET" "$cors_file"

{
  printf '%s\n' \
    '{' \
    '  "Version": "2012-10-17",' \
    '  "Statement": [' \
    '    {' \
    '      "Effect": "Allow",' \
    '      "Principal": {"AWS": ["*"]},' \
    '      "Action": ["s3:GetObject"],' \
    '      "Resource": ['
  printf '        "arn:aws:s3:::%s/downloads/*"\n' "$DOWNLOAD_S3_BUCKET"
  printf '%s\n' \
    '      ]' \
    '    }' \
    '  ]' \
    '}'
} >"$public_policy_file"

# Deliberately omit s3:ListBucket: the catalog API, not object-store listing,
# is the only supported discovery surface for published download versions.
mc anonymous set-json "$public_policy_file" "$alias_name/$DOWNLOAD_S3_BUCKET" >/dev/null
mc stat "$alias_name/$DOWNLOAD_S3_BUCKET" >/dev/null

printf 'Provisioned MinIO bucket %s for ProjectRebound downloads.\n' "$DOWNLOAD_S3_BUCKET"
