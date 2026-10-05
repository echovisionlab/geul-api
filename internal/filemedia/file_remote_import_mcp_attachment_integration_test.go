//go:build integration

package filemedia

import (
	"bytes"
	"context"
	"crypto/sha256"
	"image"
	"image/color"
	"image/png"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	mediaauth "github.com/echovisionlab/geul-api/internal/mediaauth"
	"github.com/echovisionlab/geul-api/internal/model"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

func TestMCPAttachmentStandaloneRemoteImportRetryWithMinIOIntegration(t *testing.T) {
	stack := testutil.SetupSharedDirectMediaRuntimeStack(t)
	owner := stack.CreateUser(t, policyv1.Role.Author().ID())
	other := stack.CreateUser(t, policyv1.Role.Author().ID())
	ownerInfo, otherInfo := owner.AuthUserInfo(), other.AuthUserInfo()
	ctx := releaseAuditContext(t, ownerInfo.IdentityID.String(), ownerInfo.MemberID.String())
	otherCtx := releaseAuditContext(t, otherInfo.IdentityID.String(), otherInfo.MemberID.String())
	body := []byte("Original independent chat attachment bytes\n")
	var downloads atomic.Int32
	remote := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		downloads.Add(1)
		// This untrusted hint must not override the actual text bytes.
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(body)
	}))
	t.Cleanup(remote.Close)
	remoteURL, err := url.Parse(remote.URL)
	require.NoError(t, err)
	s3Client := runtimeS3Client(t, stack)
	service := NewFileService(stack.DB, s3Client, &hardCutAsyncPublisher{}, stack.S3MediaBucket,
		stack.CDNURL, stack.MediaURL, stack.MediaSigningSecret, &recordingFileTranscoderPublisher{}, stack.SpiceDBClient,
		WithFileDomainAuditWriter(apitelemetry.NewDurableWriter(stack.DB)))
	service.remoteImportResolver = runtimeRemoteImportResolver{ip: net.ParseIP("8.8.8.8")}
	service.remoteImportDialer = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, remoteURL.Host)
	}
	transport, ok := remote.Client().Transport.(*http.Transport)
	require.True(t, ok)
	service.remoteImportBaseTransport = transport
	input := RemoteFileImportInput{
		UploadType: managev1.UploadType_UPLOAD_TYPE_GENERAL_FILE,
		SourceURL:  "https://attachments.example.com/download?signature=private-source",
		FileName:   `folder\Original name.wrong`, CorrelationID: uuid.NewString(),
	}
	first, err := service.ImportRemoteFile(ctx, input)
	require.NoError(t, err)
	require.Equal(t, "text/plain", first.GetDelivery().GetMimeType())
	require.Equal(t, "Original name.txt", first.GetDelivery().GetFileName())
	require.NotContains(t, first.String(), "private-source")
	verify := func(response *managev1.DownloadFromUrlResponse, memberID string) {
		t.Helper()
		var file model.File
		require.NoError(t, stack.DB.Where("id = ?", response.GetFileId()).Take(&file).Error)
		require.Equal(t, "Original name", file.FileName)
		require.Equal(t, "text/plain", file.MimeType)
		require.NotNil(t, file.UploadedByMemberID)
		require.Equal(t, memberID, *file.UploadedByMemberID)
		digest := sha256.Sum256(body)
		require.Equal(t, digest[:], file.SHA256)
		var binding model.FileIngestBinding
		require.NoError(t, stack.DB.Where("file_id = ?", file.ID).Take(&binding).Error)
		require.Equal(t, input.UploadType.String(), binding.UploadType)
		require.Empty(t, binding.EntityID)
		require.Nil(t, binding.EntityType)
		var usages, creates int64
		require.NoError(t, stack.DB.Table("content_block_attachment").Where("file_id = ?", file.ID).Count(&usages).Error)
		require.Zero(t, usages)
		require.NoError(t, stack.DB.Table("domain_audit").Where("target_id = ? AND action = ?", file.ID, "file.created").Count(&creates).Error)
		require.EqualValues(t, 1, creates)
		key, err := mediaauth.MediaObjectKey(file.ID, file.Extension)
		require.NoError(t, err)
		t.Cleanup(func() {
			_, _ = s3Client.DeleteObject(context.Background(), &s3.DeleteObjectInput{Bucket: aws.String(stack.S3MediaBucket), Key: aws.String(key)})
		})
		object, err := s3Client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(stack.S3MediaBucket), Key: aws.String(key)})
		require.NoError(t, err)
		stored, err := io.ReadAll(object.Body)
		require.NoError(t, err)
		require.NoError(t, object.Body.Close())
		require.Equal(t, body, stored)
	}
	verify(first, ownerInfo.MemberID.String())
	input.SourceURL = "https://expired.example.invalid/download?signature=refreshed"
	input.FileName = "new hint must not rename.txt"
	retry, err := service.ImportRemoteFile(ctx, input)
	require.NoError(t, err)
	require.Equal(t, first.GetFileId(), retry.GetFileId())
	require.Equal(t, first.GetDelivery().GetFileName(), retry.GetDelivery().GetFileName())
	require.EqualValues(t, 1, downloads.Load(), "retry must restore verified bytes without downloading again")
	var count int64
	require.NoError(t, stack.DB.Model(&model.File{}).Where("id = ?", first.GetFileId()).Count(&count).Error)
	require.EqualValues(t, 1, count)
	input.SourceURL, input.FileName = "https://attachments.example.com/download?signature=private-source", "Original name.txt"
	otherResult, err := service.ImportRemoteFile(otherCtx, input)
	require.NoError(t, err)
	require.NotEqual(t, first.GetFileId(), otherResult.GetFileId(), "another Member must not restore the first Member's private File")
	require.EqualValues(t, 2, downloads.Load())
	verify(otherResult, otherInfo.MemberID.String())
}

func TestMCPAttachmentStandaloneImageImportWithMinIOIntegration(t *testing.T) {
	stack := testutil.SetupSharedDirectMediaRuntimeStackWithCDN(t)
	owner := stack.CreateUser(t, policyv1.Role.Author().ID())
	ownerInfo := owner.AuthUserInfo()
	ctx := releaseAuditContext(t, ownerInfo.IdentityID.String(), ownerInfo.MemberID.String())
	raster := image.NewNRGBA(image.Rect(0, 0, 16, 16))
	for y := 0; y < 16; y++ {
		for x := 0; x < 16; x++ {
			raster.SetNRGBA(x, y, color.NRGBA{R: uint8(x * 16), G: uint8(y * 16), B: 96, A: 255})
		}
	}
	var original bytes.Buffer
	require.NoError(t, png.Encode(&original, raster))
	var downloads atomic.Int32
	remote := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		downloads.Add(1)
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(original.Bytes())
	}))
	t.Cleanup(remote.Close)
	remoteURL, err := url.Parse(remote.URL)
	require.NoError(t, err)
	s3Client := runtimeS3Client(t, stack)
	service := NewFileService(stack.DB, s3Client, &hardCutAsyncPublisher{}, stack.S3MediaBucket,
		stack.CDNURL, stack.MediaURL, stack.MediaSigningSecret, &recordingFileTranscoderPublisher{}, stack.SpiceDBClient,
		WithFileDomainAuditWriter(apitelemetry.NewDurableWriter(stack.DB)))
	service.remoteImportResolver = runtimeRemoteImportResolver{ip: net.ParseIP("8.8.8.8")}
	service.remoteImportDialer = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, remoteURL.Host)
	}
	transport, ok := remote.Client().Transport.(*http.Transport)
	require.True(t, ok)
	service.remoteImportBaseTransport = transport
	input := RemoteFileImportInput{
		UploadType: managev1.UploadType_UPLOAD_TYPE_EDITOR_IMAGE,
		SourceURL:  "https://attachments.example.com/download?signature=private-image-source",
		FileName:   "Chat raster.png", CorrelationID: uuid.NewString(),
	}
	first, err := service.ImportRemoteFile(ctx, input)
	require.NoError(t, err)
	require.Equal(t, "image/webp", first.GetDelivery().GetMimeType())
	require.Equal(t, "Chat raster.webp", first.GetDelivery().GetFileName())
	require.NotContains(t, first.String(), "private-image-source")
	require.NotNil(t, first.GetDelivery().GetAsset())
	var file model.File
	require.NoError(t, stack.DB.Where("id = ?", first.GetFileId()).Take(&file).Error)
	require.Equal(t, "Chat raster", file.FileName)
	require.Equal(t, "image/webp", file.MimeType)
	require.NotNil(t, file.UploadedByMemberID)
	require.Equal(t, ownerInfo.MemberID.String(), *file.UploadedByMemberID)
	var binding model.FileIngestBinding
	require.NoError(t, stack.DB.Where("file_id = ?", file.ID).Take(&binding).Error)
	require.Equal(t, input.UploadType.String(), binding.UploadType)
	require.Empty(t, binding.EntityID)
	require.Nil(t, binding.EntityType)
	var usages int64
	require.NoError(t, stack.DB.Table("content_block_attachment").Where("file_id = ?", file.ID).Count(&usages).Error)
	require.Zero(t, usages)
	fileKey, err := mediaauth.MediaObjectKey(file.ID, file.Extension)
	require.NoError(t, err)
	var asset model.PublicAsset
	require.NoError(t, stack.DB.Where("id = ?", first.GetDelivery().GetAsset().GetAssetId()).Take(&asset).Error)
	require.NotNil(t, asset.SourceFileID)
	require.Equal(t, file.ID, *asset.SourceFileID)
	t.Cleanup(func() {
		for _, key := range []string{fileKey, asset.ObjectKey} {
			_, _ = s3Client.DeleteObject(context.Background(), &s3.DeleteObjectInput{Bucket: aws.String(stack.S3MediaBucket), Key: aws.String(key)})
		}
	})
	readObject := func(key string) []byte {
		t.Helper()
		object, err := s3Client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(stack.S3MediaBucket), Key: aws.String(key)})
		require.NoError(t, err)
		body, err := io.ReadAll(object.Body)
		require.NoError(t, err)
		require.NoError(t, object.Body.Close())
		return body
	}
	stored := readObject(fileKey)
	require.Equal(t, "image/webp", detectCanonicalMime(stored, map[string]struct{}{"image/webp": {}}))
	digest := sha256.Sum256(stored)
	require.Equal(t, digest[:], file.SHA256)
	require.EqualValues(t, len(stored), file.FileSize)
	require.Equal(t, stored, readObject(asset.ObjectKey), "public image Asset must contain the verified normalized File bytes")
	imageURL, err := url.Parse(first.GetDelivery().GetAsset().GetUrl())
	require.NoError(t, err)
	query := imageURL.Query()
	// Public Asset transforms accept the CDN's fixed dimension set, whose
	// smallest supported size is 32. The source image may be smaller.
	query.Set("w", "32")
	query.Set("h", "32")
	imageURL.RawQuery = query.Encode()
	renderRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, imageURL.String(), nil)
	require.NoError(t, err)
	renderRequest.Header.Set("Accept", "image/webp")
	rendered, err := http.DefaultClient.Do(renderRequest)
	require.NoError(t, err)
	renderedBytes, err := io.ReadAll(rendered.Body)
	require.NoError(t, err)
	require.NoError(t, rendered.Body.Close())
	require.Equal(t, http.StatusOK, rendered.StatusCode, string(renderedBytes))
	require.Equal(t, "image/webp", detectCanonicalMime(renderedBytes, map[string]struct{}{"image/webp": {}}))
	input.SourceURL = "https://expired.example.invalid/image?signature=refreshed"
	retry, err := service.ImportRemoteFile(ctx, input)
	require.NoError(t, err)
	require.Equal(t, first.GetFileId(), retry.GetFileId())
	require.Equal(t, first.GetDelivery().GetAsset().GetAssetId(), retry.GetDelivery().GetAsset().GetAssetId())
	require.EqualValues(t, 1, downloads.Load(), "image retry must not download the source again")
	require.Equal(t, stored, readObject(fileKey))
	var files, assets, audits int64
	require.NoError(t, stack.DB.Model(&model.File{}).Where("id = ?", file.ID).Count(&files).Error)
	require.NoError(t, stack.DB.Model(&model.PublicAsset{}).Where("source_file_id = ?", file.ID).Count(&assets).Error)
	require.NoError(t, stack.DB.Table("domain_audit").Where("target_id = ? AND action = ?", file.ID, "file.created").Count(&audits).Error)
	require.EqualValues(t, 1, files)
	require.EqualValues(t, 1, assets)
	require.EqualValues(t, 1, audits)
}
