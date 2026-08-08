package download

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Dubnium-105/ProjectRebound/Backend/internal/config"
	"github.com/google/uuid"
)

func TestPresignUsesDedicatedBrowserEndpoint(t *testing.T) {
	storage, err := NewS3Storage(context.Background(), config.DownloadConfig{
		S3Endpoint: "http://minio:9000", S3UploadEndpoint: "https://s3.example.com",
		S3Region: "us-east-1", S3Bucket: "downloads", S3AccessKeyID: "test-access",
		S3SecretAccessKey: "test-secret", PublicBaseURL: "https://downloads.example.com/downloads",
		PublicProbeBaseURL: "http://minio:9000/downloads",
	})
	if err != nil {
		t.Fatal(err)
	}
	request, err := storage.PresignPut(context.Background(), Version{
		ID: "dver_test", OriginalFileName: "fixture.zip", ContentType: "application/zip",
		SHA256: strings.Repeat("a", 64), ObjectKey: "downloads/test/fixture.zip",
	}, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(request.URL)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Scheme != "https" || parsed.Host != "s3.example.com" ||
		!strings.HasPrefix(parsed.Path, "/downloads/downloads/test/fixture.zip") {
		t.Fatalf("presigned browser URL = %q", request.URL)
	}
	if got := storage.PublicProbeURL("downloads/test/fixture.zip"); got != "http://minio:9000/downloads/downloads/test/fixture.zip" {
		t.Fatalf("internal public probe URL = %q", got)
	}
}

func TestS3StorageAgainstCompatibleService(t *testing.T) {
	endpoint := os.Getenv("TEST_DOWNLOAD_S3_ENDPOINT")
	bucket := os.Getenv("TEST_DOWNLOAD_S3_BUCKET")
	accessKey := os.Getenv("TEST_DOWNLOAD_S3_ACCESS_KEY_ID")
	secretKey := os.Getenv("TEST_DOWNLOAD_S3_SECRET_ACCESS_KEY")
	if endpoint == "" || bucket == "" || accessKey == "" || secretKey == "" {
		t.Skip("TEST_DOWNLOAD_S3_ENDPOINT, TEST_DOWNLOAD_S3_BUCKET, and test credentials are not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	publicBase := os.Getenv("TEST_DOWNLOAD_S3_PUBLIC_BASE_URL")
	if publicBase == "" {
		publicBase = strings.TrimRight(endpoint, "/") + "/" + bucket
	}
	storage, err := NewS3Storage(ctx, config.DownloadConfig{
		S3Endpoint: endpoint, S3Region: envOr("TEST_DOWNLOAD_S3_REGION", "us-east-1"),
		S3UploadEndpoint: envOr("TEST_DOWNLOAD_S3_UPLOAD_ENDPOINT", endpoint),
		S3Bucket:         bucket, S3AccessKeyID: accessKey, S3SecretAccessKey: secretKey, PublicBaseURL: publicBase,
	})
	if err != nil {
		t.Fatal(err)
	}

	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")
	smallBody := []byte("project-rebound download storage fixture")
	small := Version{
		ID: "dver_" + suffix, OriginalFileName: "fixture.zip", ContentType: "application/zip",
		SHA256: checksum(smallBody), ObjectKey: "downloads/integration/" + suffix + "/fixture.zip",
	}
	t.Cleanup(func() { _ = storage.Delete(context.Background(), small.ObjectKey) })
	request, err := storage.PresignPut(ctx, small, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := uploadSigned(ctx, request, smallBody); err != nil {
		t.Fatal(err)
	}
	if _, err := uploadSigned(ctx, request, []byte("replacement")); err == nil {
		t.Fatal("conditional single-PUT signature replaced an existing immutable object")
	}
	metadata, err := storage.Head(ctx, small.ObjectKey)
	if err != nil || metadata.SizeBytes != int64(len(smallBody)) || metadata.ContentType != small.ContentType {
		t.Fatalf("small object metadata = %#v, %v", metadata, err)
	}
	reader, err := storage.Open(ctx, small.ObjectKey)
	if err != nil {
		t.Fatal(err)
	}
	readBack, readErr := io.ReadAll(reader)
	closeErr := reader.Close()
	if readErr != nil || closeErr != nil || !bytes.Equal(readBack, smallBody) {
		t.Fatalf("small object read = %d bytes, readErr=%v closeErr=%v", len(readBack), readErr, closeErr)
	}

	partSize := 5 << 20
	multipartBody := bytes.Repeat([]byte("m"), partSize+17)
	multipart := Version{
		ID: "dver_mp_" + suffix, OriginalFileName: "multipart.zip", ContentType: "application/zip",
		SHA256: checksum(multipartBody), ObjectKey: "downloads/integration/" + suffix + "/multipart.zip",
	}
	t.Cleanup(func() { _ = storage.Delete(context.Background(), multipart.ObjectKey) })
	uploadID, err := storage.CreateMultipart(ctx, multipart)
	if err != nil {
		t.Fatal(err)
	}
	abortOnFailure := true
	defer func() {
		if abortOnFailure {
			_ = storage.AbortMultipart(context.Background(), multipart, uploadID)
		}
	}()
	parts := make([]UploadedPart, 0, 2)
	for index, body := range [][]byte{multipartBody[:partSize], multipartBody[partSize:]} {
		partNumber := int32(index + 1)
		signed, err := storage.PresignPart(ctx, multipart, uploadID, partNumber, 5*time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		etag, err := uploadSigned(ctx, signed, body)
		if err != nil {
			t.Fatal(err)
		}
		parts = append(parts, UploadedPart{PartNumber: partNumber, ETag: etag, SizeBytes: int64(len(body))})
	}
	listed, err := storage.ListParts(ctx, multipart, uploadID)
	if err != nil || len(listed) != 2 || listed[0].SizeBytes != int64(partSize) || listed[1].SizeBytes != 17 {
		t.Fatalf("listed multipart parts = %#v, %v", listed, err)
	}
	if err := storage.CompleteMultipart(ctx, multipart, uploadID, parts); err != nil {
		t.Fatal(err)
	}
	abortOnFailure = false
	metadata, err = storage.Head(ctx, multipart.ObjectKey)
	if err != nil || metadata.SizeBytes != int64(len(multipartBody)) {
		t.Fatalf("multipart object metadata = %#v, %v", metadata, err)
	}
	if got := storage.PublicURL(multipart.ObjectKey); !strings.HasSuffix(got, multipart.ObjectKey) {
		t.Fatalf("public URL = %q", got)
	}
}

func uploadSigned(ctx context.Context, signed SignedRequest, body []byte) (string, error) {
	request, err := http.NewRequestWithContext(ctx, signed.Method, signed.URL, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	for key, value := range signed.Headers {
		if strings.EqualFold(key, "host") {
			request.Host = value
			continue
		}
		if !strings.EqualFold(key, "content-length") {
			request.Header.Set(key, value)
		}
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		payload, _ := io.ReadAll(io.LimitReader(response.Body, 2048))
		return "", fmt.Errorf("signed upload returned HTTP %d: %s", response.StatusCode, payload)
	}
	return response.Header.Get("ETag"), nil
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
