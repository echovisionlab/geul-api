package filemedia

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"io"
	"math"
	"path/filepath"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/transcoding/hls"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
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
func (s *Service) verifyClientMediaObject(ctx context.Context, key string, artifact clientMediaArtifact) ([]byte, error) {
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
func (s *Service) verifyClientMediaBundle(ctx context.Context, session domain.UploadSession) (clientMediaPlan, error) {
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
func (s *Service) loadClientMediaPlan(ctx context.Context, session domain.UploadSession, memberID string) (*clientMediaPlan, error) {
	if session.ClientMediaBundleID == nil {
		return nil, nil
	}
	plan, err := decodeClientMediaPlan(session)
	if err != nil {
		return nil, err
	}
	if plan.MemberID != memberID {
		return nil, domainerr.Denied("client media bundle belongs to another member")
	}
	if err := s.checkPartUploadPermission(ctx, memberID, session); err != nil {
		return nil, err
	}
	ready, err := s.clientMediaRegistrationReady(ctx, session)
	if err != nil {
		return nil, err
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

func (s *Service) registerClientMediaBundle(ctx context.Context, completion *multipartCompletion) error {
	return s.registerVerifiedClientMediaBundle(ctx, completion, func(ctx context.Context, store Store, lockActive func(*auth.UserInfo) (bool, error)) error {
		return requireFreshFileIngestAuthority(ctx, store, s, completion.uploadType, uploadSessionEntityTypeToEnum(completion.session.EntityType), completion.session.EntityID, lockActive)
	})
}

func clientMediaFileReady(ctx context.Context, store Store, file File) (bool, error) {
	if file.ClientMediaBundleID == nil {
		return false, nil
	}
	required := []string{clientHLS.String()}
	if strings.HasPrefix(file.MimeType, "audio/") {
		required = append(required, clientSpectrogram.String(), clientWaveform.String())
	} else {
		required = append(required, clientThumbnail.String())
	}
	count, err := store.ReadyIngestDerivativeCount(ctx, file.ID, required)
	return count == int64(len(required)), err
}

func (s *Service) clientMediaFileReady(ctx context.Context, file File) (bool, error) {
	var ready bool
	err := s.readUploadFacts(ctx, func(store Store) error {
		var err error
		ready, err = clientMediaFileReady(ctx, store, file)
		return err
	})
	return ready, err
}

func (s *Service) publishClientMediaReady(ctx context.Context, completion *multipartCompletion) error {
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
	outputs := &domain.ProcessingOutputs{HLSGenerationID: &hlsID, DurationSeconds: &duration}
	for _, artifact := range plan.Artifacts {
		kind := artifact.assetKind()
		id := stableFileIngestFinalizerID(fileID, kind)
		switch kind {
		case "waveform":
			outputs.WaveformAssetID = &id
		case "spectrogram":
			outputs.SpectrogramAssetID = &id
		case "thumbnail":
			outputs.ThumbnailAssetID = &id
		}
	}
	entityType := managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_FILE
	entityID := fileID
	var trackID, releaseID *string
	if completion.target.requiresDurableAttachment() {
		entityType = managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_TRACK
		entityID = completion.session.EntityID
		trackID = &entityID
		var release string
		if err := s.units.Read(ctx, func(store Store) error {
			var err error
			release, err = store.AttachedIngestTrackRelease(ctx, entityID, fileID)
			return err
		}); err != nil {
			return err
		}
		releaseID = &release
	}
	event := domain.ProcessingEvent{CorrelationID: completion.correlationID, EntityType: int32(entityType), EntityID: entityID, FileID: fileID, Status: int32(ProcessingReady), Outputs: outputs, TimestampMS: time.Now().UnixMilli(), SlotID: completion.session.SlotID, AttemptID: completion.session.AttemptID, TrackID: trackID, ReleaseID: releaseID}
	if s.asyncPublisher == nil {
		return fmt.Errorf("async publisher is required")
	}
	return s.asyncPublisher.PublishProcessing(ctx, event)
}
func (s *Service) cleanupClientMediaStaging(ctx context.Context, session domain.UploadSession) error {
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

func (s *Service) clientMediaRegistrationReady(ctx context.Context, session domain.UploadSession) (bool, error) {
	var file File
	err := s.readUploadFacts(ctx, func(store Store) error { var err error; file, err = store.IngestFile(ctx, session.FileID); return err })
	if errors.Is(err, domain.ErrFileNotFound) {
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
	return s.clientMediaFileReady(ctx, file)
}

func clientMediaCompletionError(err error) error {
	if errors.As(err, new(missingClientArtifactError)) {
		return err
	}
	if mutation.IsDenied(err) || mutation.IsUnauthenticated(err) {
		return err
	}
	return domainerr.Precondition(clientErrorText(err))
}

// missingClientArtifactError answers a client media artifact that is not
// staged yet; the client may upload it and retry.
type missingClientArtifactError struct{ answer *domainerr.Error }

func (err missingClientArtifactError) Error() string { return err.answer.Error() }
func (err missingClientArtifactError) Unwrap() error { return err.answer }

func missingClientArtifact(path string) error {
	return missingClientArtifactError{domainerr.New(domainerr.Unavailable, "", fmt.Sprintf("%s %s", missingClientArtifactReason, path))}
}
