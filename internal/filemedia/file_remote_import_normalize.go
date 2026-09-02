package filemedia

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	mediaauth "github.com/echovisionlab/geul-mediaauth"
)

const (
	managedRemoteImportMaxDimension   = 4096
	managedRemoteImportFinalMaxSize   = model.ManagedRasterFinalMaxSize
	managedRemoteImportSelectionLimit = 20 * 1024 * 1024
	managedRemoteImportStartQuality   = 100
	managedRemoteImportMinQuality     = 56
	managedRemoteImportQualityStep    = 8
	managedRemoteImportFallbackScale  = 0.85
	managedRemoteImportMinDimension   = 512
)

func isManagedRasterUploadType(uploadType managev1.UploadType) bool {
	switch uploadType {
	case managev1.UploadType_UPLOAD_TYPE_USER_AVATAR,
		managev1.UploadType_UPLOAD_TYPE_ARTIST_IMAGE,
		managev1.UploadType_UPLOAD_TYPE_EDITOR_IMAGE,
		managev1.UploadType_UPLOAD_TYPE_FEATURED_IMAGE,
		managev1.UploadType_UPLOAD_TYPE_WORK_FEATURED_IMAGE,
		managev1.UploadType_UPLOAD_TYPE_SERIES_FEATURED_IMAGE,
		managev1.UploadType_UPLOAD_TYPE_FORM_FEATURED_IMAGE,
		managev1.UploadType_UPLOAD_TYPE_PROGRAM_EVENT_POSTER,
		managev1.UploadType_UPLOAD_TYPE_MAP_IMAGE,
		managev1.UploadType_UPLOAD_TYPE_RELEASE_ARTWORK,
		managev1.UploadType_UPLOAD_TYPE_SITE_OG_BACKGROUND:
		return true
	default:
		return false
	}
}

func isNormalizableManagedRasterMimeType(mimeType string) bool {
	switch normalizeMimeType(mimeType) {
	case "image/jpeg", "image/png", "image/webp", "image/avif":
		return true
	default:
		return false
	}
}

func shouldNormalizeManagedRemoteImport(uploadType managev1.UploadType, mimeType string) bool {
	return isManagedRasterUploadType(uploadType) && isNormalizableManagedRasterMimeType(mimeType)
}

func getRemoteImportSelectionMaxSize(uploadType managev1.UploadType, fallbackMaxSize int64) int64 {
	if isManagedRasterUploadType(uploadType) && fallbackMaxSize > managedRemoteImportSelectionLimit {
		return managedRemoteImportSelectionLimit
	}

	return fallbackMaxSize
}

func buildManagedImageVariantURL(baseURL string, maxDimension, quality int) (string, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return "", fmt.Errorf("failed to parse managed image URL: %w", err)
	}

	query := parsed.Query()
	query.Set("w", strconv.Itoa(maxDimension))
	query.Set("h", strconv.Itoa(maxDimension))
	query.Set("q", strconv.Itoa(quality))
	parsed.RawQuery = query.Encode()

	return parsed.String(), nil
}

func normalizeRemoteImportStoredFileName(fileName, mimeType string) string {
	trimmed := strings.TrimSpace(fileName)
	if trimmed == "" {
		return ""
	}

	ext := model.GetExtensionFromMime(mimeType)
	if ext == "" || ext == "bin" {
		return trimmed
	}

	currentExt := path.Ext(trimmed)
	if currentExt == "" {
		return trimmed + "." + ext
	}

	return strings.TrimSuffix(trimmed, currentExt) + "." + ext
}

func (s *FileService) uploadRemoteImportObject(ctx context.Context, fileKey string, body io.Reader, mimeType string) error {
	uploader := transfermanager.New(s.s3Client, func(options *transfermanager.Options) {
		options.PartSizeBytes = chunkSize
	})

	_, err := uploader.UploadObject(ctx, &transfermanager.UploadObjectInput{
		Bucket:      aws.String(s.s3Bucket),
		Key:         aws.String(fileKey),
		Body:        body,
		ContentType: aws.String(mimeType),
	})
	if err != nil {
		return fmt.Errorf("failed to upload %s to S3: %w", fileKey, err)
	}

	return nil
}

func (s *FileService) deleteS3ObjectBestEffort(ctx context.Context, fileKey string) {
	if fileKey == "" {
		return
	}

	cleanupCtx, cancel := newStorageCompensationContext(ctx)
	defer cancel()
	if _, err := s.s3Client.DeleteObject(cleanupCtx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.s3Bucket),
		Key:    aws.String(fileKey),
	}); err != nil {
		slog.Warn("Failed to delete temporary S3 object", "error", err, "fileKey", fileKey)
	}
}

func (s *FileService) fetchManagedImageVariant(ctx context.Context, client *http.Client, variantURL string) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, variantURL, nil)
	if err != nil {
		return nil, "", fmt.Errorf("failed to build managed image request: %w", err)
	}
	req.Header.Set("Accept", "image/webp,image/*;q=0.8,*/*;q=0.1")

	resp, err := client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("failed to fetch managed image variant: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("managed image variant request failed with status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, managedRemoteImportFinalMaxSize+1))
	if err != nil {
		return nil, "", fmt.Errorf("failed to read managed image variant body: %w", err)
	}

	return body, normalizeMimeType(resp.Header.Get("Content-Type")), nil
}

func newRemoteNormalizationHTTPClient() *remoteImportHTTPClient {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	return &remoteImportHTTPClient{
		Client: &http.Client{
			Timeout:   remoteImportHTTPTimeout,
			Transport: transport,
		},
		transport: transport,
	}
}

func nextManagedImageDimension(current int) int {
	next := int(math.Round(float64(current) * managedRemoteImportFallbackScale))
	if next >= current {
		next = current - 1
	}
	if next < managedRemoteImportMinDimension {
		return managedRemoteImportMinDimension
	}

	return next
}

func (s *FileService) normalizeManagedRemoteImport(ctx context.Context, fileID string, sourceMime string) ([]byte, string, error) {
	sourceURL, err := BuildSignedMediaFileURL(
		s.mediaDomain,
		fileID,
		mediaExtension(&sourceMime),
		s.mediaSecret,
		mediaauth.InlineTTL,
		mediaauth.PurposeInline,
	)
	if err != nil {
		return nil, "", fmt.Errorf("sign managed image source: %w", err)
	}
	client := newRemoteNormalizationHTTPClient()
	defer client.CloseIdleConnections()

	dimension := managedRemoteImportMaxDimension
	for {
		for quality := managedRemoteImportStartQuality; quality >= managedRemoteImportMinQuality; quality -= managedRemoteImportQualityStep {
			variantURL, err := buildManagedImageVariantURL(sourceURL, dimension, quality)
			if err != nil {
				return nil, "", err
			}

			body, contentType, err := s.fetchManagedImageVariant(ctx, client.Client, variantURL)
			if err != nil {
				return nil, "", err
			}

			if len(body) == 0 {
				return nil, "", fmt.Errorf("managed image variant response was empty")
			}

			if contentType == "" {
				contentType = "image/webp"
			}

			if int64(len(body)) <= managedRemoteImportFinalMaxSize {
				return body, contentType, nil
			}
		}

		if dimension <= managedRemoteImportMinDimension {
			break
		}

		nextDimension := nextManagedImageDimension(dimension)
		if nextDimension == dimension {
			break
		}
		dimension = nextDimension
	}

	return nil, "", fmt.Errorf("normalized image exceeds maximum size %d", managedRemoteImportFinalMaxSize)
}
