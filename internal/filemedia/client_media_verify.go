package filemedia

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image/png"
	"io"
	"math"
	"path/filepath"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"gorm.io/gorm"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/transcoding/hls"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
)

type clientTSValidator struct{ offset int64 }

func (v *clientTSValidator) Write(p []byte) (int, error) {
	for i, b := range p {
		if (v.offset+int64(i))%188 == 0 && b != 0x47 {
			return i, fmt.Errorf("invalid MPEG-TS sync packet")
		}
	}
	v.offset += int64(len(p))
	return len(p), nil
}

// verifyClientMediaObject bounds every read and derives integrity from stored bytes.
func (s *FileService) verifyClientMediaObject(ctx context.Context, key string, artifact clientMediaArtifact) ([]byte, error) {
	object, err := s.s3Client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.s3Bucket), Key: aws.String(key)})
	if err != nil {
		if isMissingStoredObjectError(err) {
			return nil, missingClientArtifact(artifact.Path)
		}
		return nil, fmt.Errorf("read artifact %s: %w", artifact.Path, err)
	}
	defer object.Body.Close()
	if aws.ToInt64(object.ContentLength) != artifact.Size || normalizeMimeType(aws.ToString(object.ContentType)) != artifact.MimeType {
		return nil, fmt.Errorf("artifact metadata mismatch: %s", artifact.Path)
	}
	var payload bytes.Buffer
	var destination io.Writer = &payload
	if filepath.Ext(artifact.Path) == ".ts" {
		if artifact.Size%188 != 0 {
			return nil, fmt.Errorf("invalid MPEG-TS packet length")
		}
		destination = &clientTSValidator{}
	}
	size, digest, err := hashArtifactBody(object.Body, destination, artifact.Size)
	if err != nil {
		return nil, err
	}
	if size != artifact.Size || digest != artifact.SHA256 {
		return nil, fmt.Errorf("artifact checksum mismatch: %s", artifact.Path)
	}
	data := payload.Bytes()
	return data, validateClientArtifactContent(artifact.Path, data)
}

// hashArtifactBody is shared by HTTP staging and stored-object revalidation.
// Every caller reads at most the planned size plus one byte and hashes the bytes read.
func hashArtifactBody(source io.Reader, destination io.Writer, size int64) (int64, string, error) {
	digest := sha256.New()
	count, err := io.CopyBuffer(io.MultiWriter(digest, destination), io.LimitReader(source, size+1), make([]byte, clientMediaBufferSize))
	return count, hex.EncodeToString(digest.Sum(nil)), err
}

func validateClientArtifactContent(path string, data []byte) error {
	switch path {
	case "spectrogram.png":
		cfg, err := png.DecodeConfig(bytes.NewReader(data))
		if err != nil || cfg.Width != 1600 || cfg.Height != 224 {
			return fmt.Errorf("invalid spectrogram image")
		}
	case "thumbnail.webp":
		if len(data) < 30 || string(data[:4]) != "RIFF" || string(data[8:12]) != "WEBP" {
			return fmt.Errorf("invalid WebP thumbnail")
		}
	case "waveform.json":
		return validateClientWaveform(data)
	}
	return nil
}

func validateClientWaveform(data []byte) error {
	var peaks [][]*float64
	if err := json.Unmarshal(data, &peaks); err != nil {
		return fmt.Errorf("invalid waveform JSON")
	}
	if len(peaks) < 1 || len(peaks) > 2 {
		return fmt.Errorf("invalid waveform channels")
	}
	for _, channel := range peaks {
		if len(channel) != 2048 {
			return fmt.Errorf("invalid waveform point count")
		}
		for _, value := range channel {
			if value == nil {
				return fmt.Errorf("waveform peak must be numeric")
			}
			point := *value
			if math.IsNaN(point) || math.IsInf(point, 0) || point < -1 || point > 1 {
				return fmt.Errorf("invalid waveform peak")
			}
		}
	}
	return nil
}
func (s *FileService) verifyClientMediaBundle(ctx context.Context, session model.UploadSession) (clientMediaPlan, error) {
	plan, err := decodeClientMediaPlan(session)
	if err != nil {
		return plan, err
	}
	objects := map[string]hls.ClientObject{}
	for _, artifact := range plan.Artifacts {
		data, err := s.verifyClientMediaObject(ctx, clientMediaStagingKey(*session.ClientMediaBundleID, artifact.Path), artifact)
		if err != nil {
			return plan, err
		}
		if artifact.isHLS() {
			objects[artifact.Path] = hls.ClientObject{Size: artifact.Size, Playlist: data}
		}
	}
	if err := hls.ValidateClientPackage(objects); err != nil {
		return plan, err
	}
	return plan, hls.ValidateClientDurations(objects, plan.DurationSeconds)
}

// A durable READY receipt skips staging reads on completion recovery. Otherwise
// verify the sealed plan under the caller's advisory lock before claiming completion.
func (s *FileService) loadClientMediaPlan(ctx context.Context, session model.UploadSession, memberID string) (*clientMediaPlan, error) {
	if session.ClientMediaBundleID == nil {
		return nil, nil
	}
	plan, err := decodeClientMediaPlan(session)
	if err != nil {
		return nil, errs.Internal(err)
	}
	if plan.MemberID != memberID {
		return nil, errs.PermissionDenied("client media bundle belongs to another member")
	}
	if err := s.checkPartUploadPermission(ctx, memberID, session); err != nil {
		return nil, err
	}
	ready, err := s.clientMediaRegistrationReady(ctx, session)
	if err != nil {
		return nil, errs.Internal(err)
	}
	if ready {
		return nil, nil
	}
	verified, err := s.verifyClientMediaBundle(ctx, session)
	if err != nil {
		return nil, clientMediaCompletionError(err)
	}
	return &verified, nil
}

func (s *FileService) registerClientMediaBundle(ctx context.Context, completion *multipartCompletion) error {
	return s.registerVerifiedClientMediaBundle(ctx, completion, func(ctx context.Context, tx *gorm.DB) error {
		return requireFreshFileIngestAuthority(ctx, tx, s, completion.uploadType, uploadSessionEntityTypeToEnum(completion.session.EntityType), completion.session.EntityID)
	})
}

func clientMediaFileReady(ctx context.Context, db *gorm.DB, file model.File) (bool, error) {
	if file.ClientMediaBundleID == nil {
		return false, nil
	}
	required := []string{clientHLS.String()}
	if strings.HasPrefix(file.MimeType, "audio/") {
		required = append(required, clientSpectrogram.String(), clientWaveform.String())
	} else {
		required = append(required, clientThumbnail.String())
	}
	var count int64
	err := db.WithContext(ctx).Table("file_derivative AS d").
		Joins("LEFT JOIN media_generation AS g ON g.id = d.media_generation_id").
		Joins("LEFT JOIN public_asset AS artifact ON artifact.id = d.asset_id").
		Where("d.file_id = ? AND d.type IN ? AND ((g.file_id = ? AND g.status = ?) OR (artifact.source_file_id = ? AND artifact.status = ?))",
			file.ID, required, file.ID, model.MediaGenerationStatusReady, file.ID, model.PublicAssetStatusReady).
		Count(&count).Error
	return count == int64(len(required)), err
}
func (s *FileService) publishClientMediaReady(ctx context.Context, completion *multipartCompletion) error {
	if completion.session.ClientMediaBundleID == nil {
		return nil
	}
	fileID := completion.session.FileID
	hlsID := stableFileIngestFinalizerID(fileID, "hls")
	plan, err := decodeClientMediaPlan(completion.session)
	if err != nil {
		return err
	}
	duration := int32(math.Ceil(plan.DurationSeconds))
	outputs := &managev1.MediaProcessingLifecycleOutputs{HlsGenerationId: &hlsID, DurationSeconds: &duration}
	for _, artifact := range plan.Artifacts {
		kind := artifact.assetKind()
		id := stableFileIngestFinalizerID(fileID, kind)
		switch kind {
		case "waveform":
			outputs.WaveformAssetId = &id
		case "spectrogram":
			outputs.SpectrogramAssetId = &id
		case "thumbnail":
			outputs.ThumbnailAssetId = &id
		}
	}
	entityType := managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_FILE
	entityID := fileID
	var trackID, releaseID *string
	if completion.target.requiresDurableAttachment() {
		entityType = managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_TRACK
		entityID = completion.session.EntityID
		trackID = &entityID
		var track model.Track
		if err := s.db.WithContext(ctx).Select("release_id").Where("id = ? AND audio_original_file_id = ?", entityID, fileID).Take(&track).Error; err != nil {
			return err
		}
		releaseID = &track.ReleaseID
	}
	event := &managev1.MediaProcessingLifecycleEvent{CorrelationId: completion.correlationID, EntityType: entityType, EntityId: entityID, FileId: fileID, Status: commonv1.MediaProcessingStatus_MEDIA_PROCESSING_STATUS_READY, Outputs: outputs, TimestampMs: time.Now().UnixMilli(), SlotId: completion.session.SlotID, AttemptId: completion.session.AttemptID, TrackId: trackID, ReleaseId: releaseID}
	return publishSignalProto(ctx, s.asyncPublisher, eventpkg.SignalMediaProcessingLifecycle, event)
}
func (s *FileService) cleanupClientMediaStaging(ctx context.Context, session model.UploadSession) error {
	if session.ClientMediaBundleID == nil {
		return nil
	}
	if session.ClientMediaManifest == nil {
		return fmt.Errorf("missing staging manifest")
	}
	return CleanupClientMediaStaging(ctx, s.s3Client, s.s3Bucket, *session.ClientMediaBundleID, *session.ClientMediaManifest)
}

// CleanupClientMediaStaging removes only the immutable upload-owned artifact keys.
func CleanupClientMediaStaging(ctx context.Context, client *s3.Client, bucket, bundleID, manifest string) error {
	if !IsValidUUID(bundleID) {
		return fmt.Errorf("invalid staging bundle ID")
	}
	var plan clientMediaPlan
	if err := json.Unmarshal([]byte(manifest), &plan); err != nil {
		return err
	}
	if len(plan.Artifacts) > maxClientMediaArtifacts {
		return fmt.Errorf("invalid cleanup artifact count")
	}
	for _, artifact := range plan.Artifacts {
		if !clientMediaPathPattern.MatchString(artifact.Path) {
			return fmt.Errorf("invalid staging cleanup path")
		}
		if _, err := client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: aws.String(clientMediaStagingKey(bundleID, artifact.Path))}); err != nil {
			return err
		}
	}
	return nil
}

func (s *FileService) clientMediaRegistrationReady(ctx context.Context, session model.UploadSession) (bool, error) {
	var file model.File
	err := s.db.WithContext(ctx).Where("id = ?", session.FileID).Take(&file).Error
	if err == gorm.ErrRecordNotFound {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if file.ClientMediaBundleID == nil {
		return false, nil
	}
	if session.ClientMediaBundleID == nil || *file.ClientMediaBundleID != *session.ClientMediaBundleID {
		return false, fmt.Errorf("registered bundle authority differs")
	}
	return clientMediaFileReady(ctx, s.db, file)
}

func clientMediaCompletionError(err error) error {
	if connect.CodeOf(err) == connect.CodeUnavailable && strings.Contains(err.Error(), missingClientArtifactReason) {
		return err
	}
	if connect.CodeOf(err) == connect.CodePermissionDenied || connect.CodeOf(err) == connect.CodeUnauthenticated {
		return err
	}
	return errs.FailedPrecondition(err.Error())
}

func missingClientArtifact(path string) error {
	return connect.NewError(connect.CodeUnavailable, fmt.Errorf("%s %s", missingClientArtifactReason, path))
}
