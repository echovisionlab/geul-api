package filemedia

import (
	"strings"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/model"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	mediaauth "github.com/echovisionlab/geul-mediaauth"
)

func TestFileURLsResponseFromStoredFileSignsDownloadURL(t *testing.T) {
	t.Parallel()

	const secret = "test-media-secret"
	svc := &FileService{
		cdnDomain:   "cdn.example.com",
		mediaDomain: "media.example.com",
		mediaSecret: secret,
		downloadTTL: 2 * time.Minute,
	}

	fileID := "11111111-1111-4111-8111-111111111111"
	fileName := "작품 사진.jpg"
	response, err := svc.fileURLsResponseFromStoredFile(fileID, "jpg", "image/jpeg", 128, &fileName)
	if err != nil {
		t.Fatalf("fileURLsResponseFromStoredFile() error = %v", err)
	}
	delivery := response.GetDelivery()

	if delivery.GetMimeType() != "image/jpeg" {
		t.Fatalf("mime type = %q, want image/jpeg", delivery.GetMimeType())
	}
	if delivery.GetFileSize() != 128 {
		t.Fatalf("file size = %d, want 128", delivery.GetFileSize())
	}
	downloadURL := delivery.GetDownload().GetUrl()
	if !strings.HasPrefix(downloadURL, "https://media.example.com/media/") {
		t.Fatalf("expected signed media URL, got %q", downloadURL)
	}
	if !strings.HasSuffix(downloadURL, "/"+fileID+".jpg") {
		t.Fatalf("expected object key suffix, got %q", downloadURL)
	}

	claims := validateMediaURLToken(t, downloadURL, secret)
	if claims.Purpose != mediaauth.PurposeDownload {
		t.Fatalf("purpose = %q, want %q", claims.Purpose, mediaauth.PurposeDownload)
	}
	if claims.ScopeType != mediaauth.ScopeExact {
		t.Fatalf("scope type = %q, want %q", claims.ScopeType, mediaauth.ScopeExact)
	}
	if claims.ScopeValue != "media/"+fileID+".jpg" {
		t.Fatalf("scope value = %q, want media/%s.jpg", claims.ScopeValue, fileID)
	}
	if claims.Filename != fileName {
		t.Fatalf("filename = %q, want %q", claims.Filename, fileName)
	}
	if got := claims.ExpiryUnix - claims.IssuedAtUnix; got != int64((2*time.Minute)/time.Second) {
		t.Fatalf("download TTL = %ds, want %ds", got, int64((2*time.Minute)/time.Second))
	}
}

func TestFileURLsResponseFromStoredFileFailsClosedWithoutSecret(t *testing.T) {
	t.Parallel()

	svc := &FileService{mediaDomain: "media.example.com"}

	if _, err := svc.fileURLsResponseFromStoredFile(
		"11111111-1111-4111-8111-111111111111",
		"png",
		"image/png",
		64,
		nil,
	); err == nil {
		t.Fatal("expected signing error without media secret")
	}
}

func TestFileURLsResponseFromStoredFileUsesDeterministicHistoricalFilenameFallback(t *testing.T) {
	t.Parallel()

	const secret = "test-media-secret"
	svc := &FileService{mediaDomain: "media.example.com", mediaSecret: secret}
	fileID := "11111111-1111-4111-8111-111111111111"
	invalidName := "../source.wav"
	response, err := svc.fileURLsResponseFromStoredFile(
		fileID,
		"wav",
		"audio/wav",
		128,
		&invalidName,
	)
	if err != nil {
		t.Fatalf("fileURLsResponseFromStoredFile() error = %v", err)
	}
	claims := validateMediaURLToken(t, response.GetDelivery().GetDownload().GetUrl(), secret)
	if claims.Filename != "download-"+fileID+".wav" {
		t.Fatalf("filename = %q, want deterministic fallback", claims.Filename)
	}
}

func TestAttachFileDerivativeURLs(t *testing.T) {
	t.Parallel()

	const secret = "test-media-secret"
	svc := &FileService{
		cdnDomain:   "cdn.example.com",
		mediaDomain: "media.example.com",
		mediaSecret: secret,
	}
	fileID := "11111111-1111-4111-8111-111111111111"
	hlsID := "22222222-2222-4222-8222-222222222222"
	thumbnailID := "33333333-3333-4333-8333-333333333333"
	spectrogramID := "44444444-4444-4444-8444-444444444444"
	waveformID := "55555555-5555-4555-8555-555555555555"
	response := &managev1.GetMediaDeliveryResponse{Delivery: &commonv1.MediaDelivery{}}
	imageMime := "image/webp"
	jsonMime := "application/json"
	ready := model.PublicAssetStatusReady
	generationReady := model.MediaGenerationStatusReady
	webp := "webp"
	jsonExtension := "json"
	inline := "inline"
	thumbnailKind := "thumbnail"
	spectrogramKind := "spectrogram"
	waveformKind := "waveform"
	assetSize := int64(64)
	sha256 := make([]byte, 32)
	manifest := "master.m3u8"

	if err := svc.attachFileDerivativeURLs(response, fileID, map[string]storedDerivativeDeliveryRow{
		managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_THUMBNAIL.String(): {
			AssetID:          &thumbnailID,
			AssetKind:        &thumbnailKind,
			AssetExtension:   &webp,
			AssetMimeType:    &imageMime,
			AssetFileSize:    &assetSize,
			AssetSHA256:      sha256,
			AssetDisposition: &inline,
			AssetStatus:      &ready,
		},
		managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_HLS.String(): {
			MediaGenerationID:       &hlsID,
			MediaGenerationFileID:   &fileID,
			MediaGenerationManifest: &manifest,
			MediaGenerationStatus:   &generationReady,
		},
		managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_SPECTROGRAM.String(): {
			AssetID:          &spectrogramID,
			AssetKind:        &spectrogramKind,
			AssetExtension:   &webp,
			AssetMimeType:    &imageMime,
			AssetFileSize:    &assetSize,
			AssetSHA256:      sha256,
			AssetDisposition: &inline,
			AssetStatus:      &ready,
		},
		managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_WAVEFORM.String(): {
			AssetID:          &waveformID,
			AssetKind:        &waveformKind,
			AssetExtension:   &jsonExtension,
			AssetMimeType:    &jsonMime,
			AssetFileSize:    &assetSize,
			AssetSHA256:      sha256,
			AssetDisposition: &inline,
			AssetStatus:      &ready,
		},
	}); err != nil {
		t.Fatalf("attachFileDerivativeURLs() error = %v", err)
	}

	if response.GetDelivery().GetThumbnail().GetUrl() != "https://cdn.example.com/asset/"+thumbnailID+"/thumbnail.webp" {
		t.Fatalf("thumbnail URL = %q", response.GetDelivery().GetThumbnail().GetUrl())
	}
	if response.GetDelivery().GetSpectrogram().GetUrl() != "https://media.example.com/asset/"+spectrogramID+"/spectrogram.webp" {
		t.Fatalf("spectrogram URL = %v", response.GetDelivery().GetSpectrogram().GetUrl())
	}
	if response.GetDelivery().GetWaveform().GetUrl() != "https://media.example.com/asset/"+waveformID+"/waveform.json" {
		t.Fatalf("waveform URL = %v", response.GetDelivery().GetWaveform().GetUrl())
	}
	playbackURL := response.GetDelivery().GetPlayback().GetUrl()
	wantPlaybackURL := "https://media.example.com/media/" + fileID + "/hls/" + hlsID + "/master.m3u8"
	if playbackURL != wantPlaybackURL {
		t.Fatalf("playback URL = %q, want %q", playbackURL, wantPlaybackURL)
	}
	if response.GetDelivery().GetPlayback().GetGenerationId() != hlsID {
		t.Fatalf("playback generation = %q, want %q", response.GetDelivery().GetPlayback().GetGenerationId(), hlsID)
	}
}

func TestAssetRefForDerivativeRejectsLegacyAttachmentPublicAsset(t *testing.T) {
	t.Parallel()

	assetID := "33333333-3333-4333-8333-333333333333"
	kind := "attachment"
	extension := "pdf"
	mimeType := "application/pdf"
	fileSize := int64(64)
	disposition := "attachment"
	status := model.PublicAssetStatusReady
	_, err := (&FileService{cdnDomain: "cdn.example.com"}).assetRefForDerivative(
		storedDerivativeDeliveryRow{
			AssetID:          &assetID,
			AssetKind:        &kind,
			AssetExtension:   &extension,
			AssetMimeType:    &mimeType,
			AssetFileSize:    &fileSize,
			AssetSHA256:      make([]byte, 32),
			AssetDisposition: &disposition,
			AssetStatus:      &status,
		},
	)
	if err == nil || !strings.Contains(err.Error(), "cannot be emitted") {
		t.Fatalf("expected attachment public asset emission rejection, got %v", err)
	}
}

func validateMediaURLToken(t *testing.T, rawURL, secret string) *mediaauth.Claims {
	t.Helper()

	token := mediaTokenFromURL(t, rawURL)
	claims, err := mediaauth.ValidateToken(token, secret)
	if err != nil {
		t.Fatalf("failed to validate media token: %v", err)
	}
	return claims
}

func mediaTokenFromURL(t *testing.T, rawURL string) string {
	t.Helper()

	parts := strings.SplitN(rawURL, "/media/", 2)
	if len(parts) != 2 {
		t.Fatalf("URL does not contain /media/: %q", rawURL)
	}
	token, _, ok := strings.Cut(parts[1], "/")
	if !ok || token == "" {
		t.Fatalf("URL does not contain signed token path segment: %q", rawURL)
	}
	return token
}
