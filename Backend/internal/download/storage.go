package download

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Dubnium-105/ProjectRebound/Backend/internal/config"
	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
)

type ObjectStorage interface {
	PresignPut(context.Context, Version, time.Duration) (SignedRequest, error)
	CreateMultipart(context.Context, Version) (string, error)
	PresignPart(context.Context, Version, string, int32, time.Duration) (SignedRequest, error)
	ListParts(context.Context, Version, string) ([]UploadedPart, error)
	CompleteMultipart(context.Context, Version, string, []UploadedPart) error
	AbortMultipart(context.Context, Version, string) error
	Head(context.Context, string) (ObjectMetadata, error)
	Open(context.Context, string) (io.ReadCloser, error)
	Delete(context.Context, string) error
	PublicURL(string) string
	PublicProbeURL(string) string
}

type S3Storage struct {
	bucket     string
	publicBase string
	probeBase  string
	client     *s3.Client
	presigner  *s3.PresignClient
}

func NewS3Storage(ctx context.Context, cfg config.DownloadConfig) (*S3Storage, error) {
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(cfg.S3Region),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(
			cfg.S3AccessKeyID, cfg.S3SecretAccessKey, "",
		)),
	)
	if err != nil {
		return nil, fmt.Errorf("load S3 configuration: %w", err)
	}
	awsCfg.BaseEndpoint = aws.String(strings.TrimRight(cfg.S3Endpoint, "/"))
	client := s3.NewFromConfig(awsCfg, func(options *s3.Options) {
		options.UsePathStyle = true
	})
	presignCfg := awsCfg
	presignCfg.BaseEndpoint = aws.String(strings.TrimRight(cfg.UploadEndpoint(), "/"))
	presignClient := s3.NewFromConfig(presignCfg, func(options *s3.Options) {
		options.UsePathStyle = true
	})
	return &S3Storage{
		bucket: cfg.S3Bucket, publicBase: strings.TrimRight(cfg.PublicBaseURL, "/"),
		probeBase: strings.TrimRight(cfg.PublicProbeBase(), "/"),
		client:    client, presigner: s3.NewPresignClient(presignClient),
	}, nil
}

func (s *S3Storage) objectInput(version Version) (contentDisposition, cacheControl string, metadata map[string]string) {
	contentDisposition = fmt.Sprintf("attachment; filename=%q", version.OriginalFileName)
	cacheControl = "public, max-age=31536000, immutable"
	metadata = map[string]string{
		"download-version-id": version.ID,
		"sha256":              version.SHA256,
	}
	return
}

func (s *S3Storage) PresignPut(ctx context.Context, version Version, ttl time.Duration) (SignedRequest, error) {
	disposition, cacheControl, metadata := s.objectInput(version)
	result, err := s.presigner.PresignPutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.bucket), Key: aws.String(version.ObjectKey),
		ContentType: aws.String(version.ContentType), ContentDisposition: aws.String(disposition),
		CacheControl: aws.String(cacheControl), Metadata: metadata, IfNoneMatch: aws.String("*"),
	}, s3.WithPresignExpires(ttl))
	if err != nil {
		return SignedRequest{}, fmt.Errorf("presign S3 object upload: %w", err)
	}
	return signedRequest(result.URL, result.Method, result.SignedHeader, ttl), nil
}

func (s *S3Storage) CreateMultipart(ctx context.Context, version Version) (string, error) {
	disposition, cacheControl, metadata := s.objectInput(version)
	result, err := s.client.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{
		Bucket: aws.String(s.bucket), Key: aws.String(version.ObjectKey),
		ContentType: aws.String(version.ContentType), ContentDisposition: aws.String(disposition),
		CacheControl: aws.String(cacheControl), Metadata: metadata,
	})
	if err != nil {
		return "", fmt.Errorf("create S3 multipart upload: %w", err)
	}
	return aws.ToString(result.UploadId), nil
}

func (s *S3Storage) PresignPart(ctx context.Context, version Version, uploadID string, partNumber int32, ttl time.Duration) (SignedRequest, error) {
	result, err := s.presigner.PresignUploadPart(ctx, &s3.UploadPartInput{
		Bucket: aws.String(s.bucket), Key: aws.String(version.ObjectKey),
		UploadId: aws.String(uploadID), PartNumber: aws.Int32(partNumber),
	}, s3.WithPresignExpires(ttl))
	if err != nil {
		return SignedRequest{}, fmt.Errorf("presign S3 upload part: %w", err)
	}
	return signedRequest(result.URL, result.Method, result.SignedHeader, ttl), nil
}

func (s *S3Storage) ListParts(ctx context.Context, version Version, uploadID string) ([]UploadedPart, error) {
	items := make([]UploadedPart, 0)
	var marker *string
	for {
		result, err := s.client.ListParts(ctx, &s3.ListPartsInput{
			Bucket: aws.String(s.bucket), Key: aws.String(version.ObjectKey),
			UploadId: aws.String(uploadID), PartNumberMarker: marker,
		})
		if err != nil {
			return nil, fmt.Errorf("list S3 upload parts: %w", err)
		}
		for _, part := range result.Parts {
			items = append(items, UploadedPart{
				PartNumber: aws.ToInt32(part.PartNumber), ETag: aws.ToString(part.ETag),
				SizeBytes: aws.ToInt64(part.Size),
			})
		}
		if !aws.ToBool(result.IsTruncated) {
			break
		}
		marker = result.NextPartNumberMarker
	}
	return items, nil
}

func (s *S3Storage) CompleteMultipart(ctx context.Context, version Version, uploadID string, parts []UploadedPart) error {
	completed := make([]s3types.CompletedPart, 0, len(parts))
	for _, part := range parts {
		completed = append(completed, s3types.CompletedPart{
			ETag: aws.String(part.ETag), PartNumber: aws.Int32(part.PartNumber),
		})
	}
	_, err := s.client.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{
		Bucket: aws.String(s.bucket), Key: aws.String(version.ObjectKey), UploadId: aws.String(uploadID),
		MultipartUpload: &s3types.CompletedMultipartUpload{Parts: completed},
	})
	if err != nil {
		return fmt.Errorf("complete S3 multipart upload: %w", err)
	}
	return nil
}

func (s *S3Storage) AbortMultipart(ctx context.Context, version Version, uploadID string) error {
	if strings.TrimSpace(uploadID) == "" {
		return nil
	}
	_, err := s.client.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{
		Bucket: aws.String(s.bucket), Key: aws.String(version.ObjectKey), UploadId: aws.String(uploadID),
	})
	if err != nil {
		return fmt.Errorf("abort S3 multipart upload: %w", err)
	}
	return nil
}

func (s *S3Storage) Head(ctx context.Context, objectKey string) (ObjectMetadata, error) {
	result, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(s.bucket), Key: aws.String(objectKey),
	})
	if err != nil {
		return ObjectMetadata{}, fmt.Errorf("head S3 object: %w", err)
	}
	return ObjectMetadata{SizeBytes: aws.ToInt64(result.ContentLength), ContentType: aws.ToString(result.ContentType)}, nil
}

func (s *S3Storage) Open(ctx context.Context, objectKey string) (io.ReadCloser, error) {
	result, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket), Key: aws.String(objectKey),
	})
	if err != nil {
		return nil, fmt.Errorf("open S3 object: %w", err)
	}
	return result.Body, nil
}

func (s *S3Storage) Delete(ctx context.Context, objectKey string) error {
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.bucket), Key: aws.String(objectKey),
	})
	if err != nil {
		return fmt.Errorf("delete S3 object: %w", err)
	}
	return nil
}

func (s *S3Storage) PublicURL(objectKey string) string {
	return s.publicBase + "/" + strings.TrimLeft(objectKey, "/")
}

func (s *S3Storage) PublicProbeURL(objectKey string) string {
	return s.probeBase + "/" + strings.TrimLeft(objectKey, "/")
}

func signedRequest(rawURL, method string, headers http.Header, ttl time.Duration) SignedRequest {
	values := make(map[string]string, len(headers))
	for key, value := range headers {
		if len(value) > 0 {
			values[key] = value[0]
		}
	}
	return SignedRequest{
		URL: rawURL, Method: method, Headers: values, ExpiresAt: time.Now().UTC().Add(ttl),
	}
}
