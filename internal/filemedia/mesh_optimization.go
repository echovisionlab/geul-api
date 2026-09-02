package filemedia

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	mediaauth "github.com/echovisionlab/geul-mediaauth"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
)

const meshOptimizationCandidateTTL = 7 * 24 * time.Hour

var errUnsupportedMeshOptimizationResource = errors.New("unsupported mesh optimization resource")

type MeshOptimizationJobPublisher interface {
	PublishMeshOptimizationJob(ctx context.Context, job *managev1.MeshOptimizationJob) error
}

type MeshOptimizationFileDeleter interface {
	DeleteFileByID(ctx context.Context, fileID string) error
}

type MeshOptimizationService struct {
	db          *gorm.DB
	publisher   MeshOptimizationJobPublisher
	fileDeleter MeshOptimizationFileDeleter
	spiceDB     *auth.SpiceDBClient
}

type MeshOptimizationListInput struct {
	SourceFileID string
	EntityType   managev1.TranscodeEntityType
	EntityID     string
	Profile      managev1.MeshOptimizationProfile
}

type MeshOptimizationGenerateInput struct {
	SourceFileID       string
	EntityType         managev1.TranscodeEntityType
	EntityID           string
	TargetRatioPercent int32
	Method             string
	Profile            managev1.MeshOptimizationProfile
	PipelineVersion    string
}

type MeshOptimizationGenerateResult struct {
	Candidate model.MeshOptimizationCandidate
	CacheHit  bool
	Enqueued  bool
}

func NewMeshOptimizationService(
	db *gorm.DB,
	publisher MeshOptimizationJobPublisher,
	fileDeleter MeshOptimizationFileDeleter,
	spiceDB *auth.SpiceDBClient,
) *MeshOptimizationService {
	if db == nil {
		panic("mesh optimization service: db is required")
	}
	return &MeshOptimizationService{
		db:          db,
		publisher:   publisher,
		fileDeleter: fileDeleter,
		spiceDB:     spiceDB,
	}
}

func BuildMeshOptimizationCacheKey(sourceFileID string, targetRatioPercent int32, method string, pipelineVersion string) string {
	return strings.Join([]string{
		strings.TrimSpace(sourceFileID),
		fmt.Sprintf("%d", targetRatioPercent),
		normalizeMeshOptimizationMethod(method),
		normalizeMeshOptimizationPipelineVersion(pipelineVersion),
	}, ":")
}

func meshOptimizationJobFromCandidate(candidate model.MeshOptimizationCandidate, source model.File) (*managev1.MeshOptimizationJob, error) {
	if err := validateMeshOptimizationTargetRatioPercent(candidate.TargetRatioPercent); err != nil {
		return nil, err
	}
	if candidate.JobID == nil || strings.TrimSpace(*candidate.JobID) == "" {
		return nil, errs.Required("job_id")
	}
	if strings.TrimSpace(candidate.OutputObjectID) == "" {
		return nil, errs.Required("output_object_id")
	}
	sourceTarget, err := meshMediaObjectTarget(source.ID, source.Extension, source.MimeType)
	if err != nil {
		return nil, errs.Internal(err)
	}
	outputTarget, err := meshMediaObjectTarget(candidate.OutputObjectID, "glb", "model/gltf-binary")
	if err != nil {
		return nil, errs.Internal(err)
	}
	entityType, entityID := candidatePermissionTarget(candidate)
	return &managev1.MeshOptimizationJob{
		JobId:         strings.TrimSpace(*candidate.JobID),
		CorrelationId: candidate.ID,
		Identity: &managev1.MeshOptimizationIdentity{
			EntityType: entityType,
			EntityId:   entityID,
			FileId:     candidate.SourceFileID,
			Source:     sourceTarget,
			SlotId:     source.IngestSlotID,
			AttemptId:  source.IngestAttemptID,
		},
		Options: &managev1.MeshOptimizationOptions{
			CompressionMethod:  meshOptimizationCompressionMethod(candidate.Method),
			TargetRatioPercent: candidate.TargetRatioPercent,
			Profile:            meshOptimizationProfileForPipelineVersion(candidate.PipelineVersion),
		},
		Output:      outputTarget,
		TimestampMs: time.Now().UnixMilli(),
	}, nil
}

func meshMediaObjectTarget(fileID, extension, mimeType string) (*commonv1.MediaObjectTarget, error) {
	fileID = strings.TrimSpace(fileID)
	extension = strings.ToLower(strings.TrimSpace(extension))
	objectKey, err := mediaauth.MediaObjectKey(fileID, extension)
	if err != nil {
		return nil, err
	}
	return &commonv1.MediaObjectTarget{
		FileId:    fileID,
		ObjectKey: objectKey,
		Extension: extension,
		MimeType:  strings.TrimSpace(mimeType),
	}, nil
}

func ValidateMeshOptimizationRequest(targetRatioPercent int32, method string) error {
	if err := validateMeshOptimizationTargetRatioPercent(targetRatioPercent); err != nil {
		return err
	}
	if normalizeMeshOptimizationMethod(method) != model.MeshOptimizationMethodDraco {
		return errs.InvalidArgument("method", "only DRACO is supported")
	}
	return nil
}

func validateMeshOptimizationTargetRatioPercent(targetRatioPercent int32) error {
	if targetRatioPercent < 1 || targetRatioPercent > 100 {
		return errs.InvalidArgument("target_ratio_percent", "must be between 1 and 100")
	}
	return nil
}

func unixMillisTime(timestampMs int64) time.Time {
	if timestampMs <= 0 {
		return time.Now()
	}
	return time.UnixMilli(timestampMs)
}

func optionalEventInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	next := *value
	return &next
}

func optionalPositiveEventInt64(value int64) *int64 {
	if value <= 0 {
		return nil
	}
	return &value
}

func meshOptimizationCompressionMethod(method string) managev1.MeshOptimizationCompressionMethod {
	if normalizeMeshOptimizationMethod(method) == model.MeshOptimizationMethodDraco {
		return managev1.MeshOptimizationCompressionMethod_MESH_OPTIMIZATION_COMPRESSION_METHOD_DRACO
	}
	return managev1.MeshOptimizationCompressionMethod_MESH_OPTIMIZATION_COMPRESSION_METHOD_UNSPECIFIED
}

func normalizeMeshOptimizationMethod(method string) string {
	method = strings.TrimSpace(method)
	if method == "" {
		return model.MeshOptimizationMethodDraco
	}
	return strings.ToUpper(method)
}

func normalizeMeshOptimizationPipelineVersion(pipelineVersion string) string {
	pipelineVersion = strings.TrimSpace(pipelineVersion)
	if pipelineVersion == "" {
		return model.MeshOptimizationPipelineVersionDracoWebpV1
	}
	return pipelineVersion
}

func meshOptimizationPipelineVersionForProfile(profile managev1.MeshOptimizationProfile) (string, error) {
	switch profile {
	case managev1.MeshOptimizationProfile_MESH_OPTIMIZATION_PROFILE_UNSPECIFIED,
		managev1.MeshOptimizationProfile_MESH_OPTIMIZATION_PROFILE_DRACO_WEBP_V1:
		return model.MeshOptimizationPipelineVersionDracoWebpV1, nil
	case managev1.MeshOptimizationProfile_MESH_OPTIMIZATION_PROFILE_PARTICLE_MESH_V1:
		return model.MeshOptimizationPipelineVersionParticleMeshV1, nil
	default:
		return "", errs.InvalidArgument("profile", "unsupported mesh optimization profile")
	}
}

func meshOptimizationProfileForPipelineVersion(pipelineVersion string) managev1.MeshOptimizationProfile {
	switch normalizeMeshOptimizationPipelineVersion(pipelineVersion) {
	case model.MeshOptimizationPipelineVersionParticleMeshV1:
		return managev1.MeshOptimizationProfile_MESH_OPTIMIZATION_PROFILE_PARTICLE_MESH_V1
	case model.MeshOptimizationPipelineVersionDracoV1,
		model.MeshOptimizationPipelineVersionDracoWebpV1:
		return managev1.MeshOptimizationProfile_MESH_OPTIMIZATION_PROFILE_DRACO_WEBP_V1
	default:
		return managev1.MeshOptimizationProfile_MESH_OPTIMIZATION_PROFILE_UNSPECIFIED
	}
}

func validateMeshOptimizationOutputProfile(
	candidate model.MeshOptimizationCandidate,
	actual managev1.MeshOptimizationProfile,
) error {
	expected := meshOptimizationProfileForPipelineVersion(candidate.PipelineVersion)
	if expected == managev1.MeshOptimizationProfile_MESH_OPTIMIZATION_PROFILE_DRACO_WEBP_V1 &&
		actual == managev1.MeshOptimizationProfile_MESH_OPTIMIZATION_PROFILE_UNSPECIFIED {
		return nil
	}
	if actual != expected {
		return errs.InvalidArgument("output.profile", "does not match the requested mesh optimization profile")
	}
	return nil
}

func (s *MeshOptimizationService) ListCandidates(ctx context.Context, input MeshOptimizationListInput) ([]model.MeshOptimizationCandidate, error) {
	input.SourceFileID = strings.TrimSpace(input.SourceFileID)
	input.EntityID = strings.TrimSpace(input.EntityID)
	if input.SourceFileID == "" {
		return nil, errs.Required("source_file_id")
	}
	if err := s.ensureSourceFileReadable(ctx, input.SourceFileID, input.EntityType, input.EntityID); err != nil {
		return nil, err
	}
	pipelineVersion, err := meshOptimizationPipelineVersionForProfile(input.Profile)
	if err != nil {
		return nil, err
	}

	var candidates []model.MeshOptimizationCandidate
	if err := s.db.WithContext(ctx).
		Where("source_file_id = ? AND pipeline_version = ?", input.SourceFileID, pipelineVersion).
		Order("target_ratio_percent ASC, created_at DESC").
		Find(&candidates).Error; err != nil {
		return nil, errs.Internal(err)
	}
	return candidates, nil
}

func (s *MeshOptimizationService) GenerateCandidate(
	ctx context.Context,
	input MeshOptimizationGenerateInput,
) (*MeshOptimizationGenerateResult, error) {
	input.SourceFileID = strings.TrimSpace(input.SourceFileID)
	input.EntityID = strings.TrimSpace(input.EntityID)
	input.Method = normalizeMeshOptimizationMethod(input.Method)
	pipelineVersion, err := meshOptimizationPipelineVersionForProfile(input.Profile)
	if err != nil {
		return nil, err
	}
	input.PipelineVersion = pipelineVersion
	if input.SourceFileID == "" {
		return nil, errs.Required("source_file_id")
	}
	if err := ValidateMeshOptimizationRequest(input.TargetRatioPercent, input.Method); err != nil {
		return nil, err
	}
	source, err := s.ensureSourceFileOptimizable(ctx, input.SourceFileID, input.EntityType, input.EntityID)
	if err != nil {
		return nil, err
	}
	cacheKey := BuildMeshOptimizationCacheKey(
		input.SourceFileID,
		input.TargetRatioPercent,
		input.Method,
		input.PipelineVersion,
	)
	if s.publisher == nil {
		return nil, errs.FailedPrecondition("mesh optimization publisher is not configured")
	}

	var (
		candidate       model.MeshOptimizationCandidate
		cacheHit        bool
		cleanupOutputID string
		enqueued        bool
	)
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		candidate, cacheHit, cleanupOutputID, err = s.findOrCreateCandidate(ctx, tx, input, cacheKey)
		if err != nil || (cacheHit && meshOptimizationCandidateCacheUsable(candidate.Status)) {
			return err
		}
		job, err := meshOptimizationJobFromCandidate(candidate, *source)
		if err != nil {
			return err
		}
		if asyncPublisher, ok := s.publisher.(AsyncPublisher); ok {
			if err := publishDurableProtoInTransaction(
				ctx,
				asyncPublisher,
				tx,
				eventpkg.QueueAssetOptimizerMesh,
				job.GetJobId(),
				job,
			); err != nil {
				return err
			}
		} else if err := s.publisher.PublishMeshOptimizationJob(ctx, job); err != nil {
			return err
		}
		enqueued = true
		return nil
	}); err != nil {
		return nil, errs.Internal(fmt.Errorf("failed to enqueue mesh optimization: %w", err))
	}
	if cleanupOutputID != "" && s.fileDeleter != nil {
		if err := s.fileDeleter.DeleteFileByID(ctx, cleanupOutputID); err != nil {
			slog.Warn("Failed to delete superseded mesh optimization output", "file_id", cleanupOutputID, "error", err)
		}
	}
	return &MeshOptimizationGenerateResult{Candidate: candidate, CacheHit: cacheHit, Enqueued: enqueued}, nil
}

func (s *MeshOptimizationService) MarkCandidateProcessing(ctx context.Context, candidateID string, startedAt time.Time) error {
	candidateID = strings.TrimSpace(candidateID)
	if candidateID == "" {
		return errs.Required("candidate_id")
	}
	if startedAt.IsZero() {
		startedAt = time.Now()
	}
	return s.db.WithContext(ctx).
		Model(&model.MeshOptimizationCandidate{}).
		Where("id = ? AND status = ?", candidateID, model.MeshOptimizationCandidateStatusPending).
		Updates(structured.Fields{
			"status":                model.MeshOptimizationCandidateStatusProcessing,
			"processing_started_at": startedAt,
			"expires_at":            startedAt.Add(meshOptimizationCandidateTTL),
			"updated_at":            startedAt,
		}).Error
}

func (s *MeshOptimizationService) UseCandidate(ctx context.Context, candidateID string) (*model.MeshOptimizationCandidate, error) {
	candidate, err := s.loadCandidate(ctx, candidateID)
	if err != nil {
		return nil, err
	}
	entityType, entityID := candidatePermissionTarget(*candidate)
	if err := s.ensureSourceFileReadable(ctx, candidate.SourceFileID, entityType, entityID); err != nil {
		return nil, err
	}
	if candidate.Status != model.MeshOptimizationCandidateStatusReady || candidate.OutputFileID == nil {
		return nil, errs.FailedPrecondition("mesh optimization candidate is not ready")
	}
	now := time.Now()
	if err := s.db.WithContext(ctx).
		Model(&model.MeshOptimizationCandidate{}).
		Where("id = ?", candidate.ID).
		Updates(structured.Fields{
			"selected_at": now,
			"expires_at":  nil,
			"updated_at":  now,
		}).Error; err != nil {
		return nil, errs.Internal(err)
	}
	candidate.SelectedAt = &now
	candidate.ExpiresAt = nil
	candidate.UpdatedAt = now
	return candidate, nil
}

func (s *MeshOptimizationService) DiscardCandidate(ctx context.Context, candidateID string) error {
	candidate, err := s.loadCandidate(ctx, candidateID)
	if err != nil {
		return err
	}
	entityType, entityID := candidatePermissionTarget(*candidate)
	if err := s.ensureSourceFileReadable(ctx, candidate.SourceFileID, entityType, entityID); err != nil {
		return err
	}
	if candidate.SelectedAt != nil {
		return s.deleteCandidateAndOutput(ctx, *candidate)
	}
	if candidate.Status == model.MeshOptimizationCandidateStatusPending ||
		candidate.Status == model.MeshOptimizationCandidateStatusProcessing {
		now := time.Now()
		if err := s.db.WithContext(ctx).
			Model(&model.MeshOptimizationCandidate{}).
			Where("id = ?", candidate.ID).
			Updates(structured.Fields{
				"status":       model.MeshOptimizationCandidateStatusCancelled,
				"cancelled_at": now,
				"expires_at":   now.Add(meshOptimizationCandidateTTL),
				"updated_at":   now,
			}).Error; err != nil {
			return errs.Internal(err)
		}
		return nil
	}
	return s.deleteCandidateAndOutput(ctx, *candidate)
}

func (s *MeshOptimizationService) HandleProgress(
	ctx context.Context,
	event *managev1.MeshOptimizationProgressEvent,
) (*model.MeshOptimizationCandidate, error) {
	if event == nil {
		return nil, errs.Required("event")
	}
	if strings.TrimSpace(event.GetJobId()) == "" {
		return nil, errs.Required("job_id")
	}
	candidate, err := s.loadCandidateForMeshEvent(ctx, event.GetJobId(), event.GetCorrelationId(), event.GetIdentity())
	if err != nil {
		return nil, err
	}
	if candidate.Status == model.MeshOptimizationCandidateStatusPending {
		if err := s.MarkCandidateProcessing(ctx, candidate.ID, unixMillisTime(event.GetTimestampMs())); err != nil {
			return nil, errs.Internal(err)
		}
		return s.loadCandidate(ctx, candidate.ID)
	}
	return candidate, nil
}

func (s *MeshOptimizationService) HandleComplete(
	ctx context.Context,
	event *managev1.MeshOptimizationCompleteEvent,
) (*model.MeshOptimizationCandidate, error) {
	completion, candidate, err := s.prepareMeshOptimizationCompletion(ctx, event)
	if err != nil {
		return nil, err
	}
	sourceAcceptsResults := false
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		updated, acceptsResults, err := applyMeshOptimizationCompletionWithDB(ctx, tx, candidate, completion)
		candidate = updated
		sourceAcceptsResults = acceptsResults
		return err
	})
	if err != nil {
		return nil, errs.Internal(err)
	}
	if !sourceAcceptsResults || candidate.Status == model.MeshOptimizationCandidateStatusCancelled {
		candidate.OutputObjectID = completion.outputFileID
		candidate.OutputFileID = &completion.outputFileID
		if err := s.deleteCandidateAndOutput(ctx, *candidate); err != nil {
			return nil, err
		}
		return candidate, nil
	}

	return s.loadCandidate(ctx, candidate.ID)
}

type meshOptimizationCompletion struct {
	event        *managev1.MeshOptimizationCompleteEvent
	output       *managev1.MeshOptimizationOutput
	outputFileID string
	fileSize     int64
	sha256       []byte
	completedAt  time.Time
	expiresAt    time.Time
}

func (s *MeshOptimizationService) prepareMeshOptimizationCompletion(
	ctx context.Context,
	event *managev1.MeshOptimizationCompleteEvent,
) (meshOptimizationCompletion, *model.MeshOptimizationCandidate, error) {
	if event == nil {
		return meshOptimizationCompletion{}, nil, errs.Required("event")
	}
	if strings.TrimSpace(event.GetJobId()) == "" {
		return meshOptimizationCompletion{}, nil, errs.Required("job_id")
	}
	output := event.GetOutput()
	if output == nil {
		return meshOptimizationCompletion{}, nil, errs.Required("output")
	}
	candidate, err := s.loadCandidateForMeshEvent(ctx, event.GetJobId(), event.GetCorrelationId(), event.GetIdentity())
	if err != nil {
		return meshOptimizationCompletion{}, nil, err
	}
	if err := validateMeshOptimizationOutputProfile(*candidate, output.GetProfile()); err != nil {
		return meshOptimizationCompletion{}, nil, err
	}
	completion, err := validateMeshOptimizationCompletionPayload(event, output, candidate.OutputObjectID)
	return completion, candidate, err
}

func validateMeshOptimizationCompletionPayload(
	event *managev1.MeshOptimizationCompleteEvent,
	output *managev1.MeshOptimizationOutput,
	expectedOutputObjectID string,
) (meshOptimizationCompletion, error) {
	written := output.GetWritten()
	if written == nil {
		return meshOptimizationCompletion{}, errs.Required("output.written")
	}
	outputFileID := strings.TrimSpace(written.GetFileId())
	expectedOutputObjectID = strings.TrimSpace(expectedOutputObjectID)
	if expectedOutputObjectID == "" {
		return meshOptimizationCompletion{}, errs.FailedPrecondition("mesh optimization output was not allocated")
	}
	if outputFileID != expectedOutputObjectID {
		return meshOptimizationCompletion{}, errs.InvalidArgument("output.written.file_id", "does not match the allocated output file")
	}
	if output.OptimizedSizeBytes == nil {
		return meshOptimizationCompletion{}, errs.Required("optimized_size_bytes")
	}
	if written.GetFileSize() <= 0 || written.GetFileSize() != output.GetOptimizedSizeBytes() {
		return meshOptimizationCompletion{}, errs.InvalidArgument("output.written.file_size", "must match optimized_size_bytes and be positive")
	}
	if len(written.GetSha256()) != 32 {
		return meshOptimizationCompletion{}, errs.InvalidArgument("output.written.sha256", "must be a 32-byte SHA-256 digest")
	}
	completedAt := unixMillisTime(event.GetTimestampMs())
	return meshOptimizationCompletion{
		event: event, output: output, outputFileID: outputFileID,
		fileSize: *output.OptimizedSizeBytes, sha256: append([]byte(nil), written.GetSha256()...),
		completedAt: completedAt, expiresAt: completedAt.Add(meshOptimizationCandidateTTL),
	}, nil
}

func applyMeshOptimizationCompletionWithDB(
	ctx context.Context,
	tx *gorm.DB,
	candidate *model.MeshOptimizationCandidate,
	completion meshOptimizationCompletion,
) (*model.MeshOptimizationCandidate, bool, error) {
	acceptsResults, err := lockMeshOptimizationCompletionFiles(
		ctx,
		tx,
		candidate.SourceFileID,
		completion.outputFileID,
	)
	if err != nil {
		return nil, false, err
	}
	lockedCandidate, err := lockMeshOptimizationCandidate(ctx, tx, candidate.ID)
	if err != nil {
		return nil, false, err
	}
	if err := ensureMeshOptimizationOutputFile(tx, completion); err != nil {
		return nil, false, err
	}
	updates, status := meshOptimizationCompletionUpdates(completion, acceptsResults, lockedCandidate.Status)
	if err := tx.Model(&model.MeshOptimizationCandidate{}).
		Where("id = ?", lockedCandidate.ID).Updates(updates).Error; err != nil {
		return nil, false, err
	}
	lockedCandidate.Status = status
	return &lockedCandidate, acceptsResults, nil
}

func lockMeshOptimizationCompletionFiles(
	ctx context.Context,
	tx *gorm.DB,
	sourceFileID string,
	outputFileID string,
) (bool, error) {
	fileIDs := normalizedSortedFileIDs([]string{sourceFileID, outputFileID})
	var files []model.File
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Select("id", "delete_requested_at").Where("id IN ?", fileIDs).Order("id ASC").Find(&files).Error; err != nil {
		return false, err
	}
	for _, file := range files {
		if file.ID == sourceFileID {
			return file.DeleteRequestedAt == nil, nil
		}
	}
	return false, gorm.ErrRecordNotFound
}

func lockMeshOptimizationCandidate(
	ctx context.Context,
	tx *gorm.DB,
	candidateID string,
) (model.MeshOptimizationCandidate, error) {
	var candidate model.MeshOptimizationCandidate
	err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ?", candidateID).Take(&candidate).Error
	return candidate, err
}

func ensureMeshOptimizationOutputFile(tx *gorm.DB, completion meshOptimizationCompletion) error {
	const mimeType = "model/gltf-binary"
	outputFile := model.File{
		ID: completion.outputFileID, FileName: completion.outputFileID, MimeType: mimeType,
		FileSize: completion.fileSize, Extension: "glb", SHA256: completion.sha256,
		CreatedAt: completion.completedAt,
	}
	created := tx.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "id"}}, DoNothing: true,
	}).Create(&outputFile)
	if created.Error != nil || created.RowsAffected == 1 {
		return created.Error
	}
	var existing model.File
	if err := tx.Where("id = ?", completion.outputFileID).Take(&existing).Error; err != nil {
		return err
	}
	if existing.FileSize != completion.fileSize || existing.Extension != "glb" ||
		existing.MimeType != mimeType || !bytes.Equal(existing.SHA256, completion.sha256) {
		return fmt.Errorf("mesh optimization completion conflicts with existing output metadata")
	}
	return nil
}

func meshOptimizationCompletionUpdates(
	completion meshOptimizationCompletion,
	sourceAcceptsResults bool,
	currentStatus string,
) (structured.Fields, string) {
	output := completion.output
	updates := structured.Fields{
		"output_file_id":      completion.outputFileID,
		"optimized_file_size": optionalEventInt64(output.OptimizedSizeBytes),
		"original_file_size":  optionalEventInt64(output.OriginalSizeBytes),
		"original_vertexes":   optionalEventInt64(output.OriginalVertexCount),
		"optimized_vertexes":  optionalEventInt64(output.OptimizedVertexCount),
		"original_triangles":  optionalEventInt64(output.OriginalTriangleCount),
		"optimized_triangles": optionalEventInt64(output.OptimizedTriangleCount),
		"processing_time_ms":  optionalPositiveEventInt64(completion.event.GetProcessingTimeMs()),
		"error_message":       nil, "completed_at": completion.completedAt,
		"expires_at": completion.expiresAt, "updated_at": completion.completedAt,
	}
	if sourceAcceptsResults && currentStatus != model.MeshOptimizationCandidateStatusCancelled {
		updates["status"] = model.MeshOptimizationCandidateStatusReady
		updates["failed_at"] = nil
		updates["cancelled_at"] = nil
		return updates, model.MeshOptimizationCandidateStatusReady
	}
	updates["status"] = model.MeshOptimizationCandidateStatusCancelled
	updates["cancelled_at"] = completion.completedAt
	return updates, model.MeshOptimizationCandidateStatusCancelled
}

func (s *MeshOptimizationService) HandleFailed(ctx context.Context, event *managev1.MeshOptimizationFailEvent) (*model.MeshOptimizationCandidate, error) {
	if event == nil {
		return nil, errs.Required("event")
	}
	if strings.TrimSpace(event.GetJobId()) == "" {
		return nil, errs.Required("job_id")
	}
	candidate, err := s.loadCandidateForMeshEvent(ctx, event.GetJobId(), event.GetCorrelationId(), event.GetIdentity())
	if err != nil {
		return nil, err
	}
	if candidate.Status == model.MeshOptimizationCandidateStatusCancelled {
		if err := s.deleteCandidateAndOutput(ctx, *candidate); err != nil {
			return nil, err
		}
		return candidate, nil
	}
	failedAt := unixMillisTime(event.GetTimestampMs())
	expiresAt := failedAt.Add(meshOptimizationCandidateTTL)
	errText := strings.TrimSpace(event.GetError())
	if errText == "" {
		errText = event.GetReason().String()
	}
	if err := s.db.WithContext(ctx).
		Model(&model.MeshOptimizationCandidate{}).
		Where("id = ?", candidate.ID).
		Updates(structured.Fields{
			"status":        model.MeshOptimizationCandidateStatusFailed,
			"error_message": errText,
			"failed_at":     failedAt,
			"expires_at":    expiresAt,
			"updated_at":    failedAt,
		}).Error; err != nil {
		return nil, errs.Internal(err)
	}
	return s.loadCandidate(ctx, candidate.ID)
}

func (s *MeshOptimizationService) ExpireStaleUnselectedCandidates(ctx context.Context, now time.Time) (int64, error) {
	if now.IsZero() {
		now = time.Now()
	}
	var candidates []model.MeshOptimizationCandidate
	if err := s.db.WithContext(ctx).
		Where("selected_at IS NULL").
		Where("expires_at IS NOT NULL AND expires_at < ?", now).
		Find(&candidates).Error; err != nil {
		return 0, err
	}
	for _, candidate := range candidates {
		if err := s.deleteCandidateAndOutput(ctx, candidate); err != nil {
			return 0, err
		}
	}
	return int64(len(candidates)), nil
}

func meshOptimizationCandidateCacheUsable(status string) bool {
	switch status {
	case model.MeshOptimizationCandidateStatusPending,
		model.MeshOptimizationCandidateStatusProcessing,
		model.MeshOptimizationCandidateStatusReady:
		return true
	default:
		return false
	}
}

func MeshOptimizationOutputProtectionStatuses() []string {
	return []string{
		model.MeshOptimizationCandidateStatusPending,
		model.MeshOptimizationCandidateStatusProcessing,
		model.MeshOptimizationCandidateStatusReady,
	}
}

func (s *MeshOptimizationService) findOrCreateCandidate(
	ctx context.Context,
	db *gorm.DB,
	input MeshOptimizationGenerateInput,
	cacheKey string,
) (model.MeshOptimizationCandidate, bool, string, error) {
	var candidate model.MeshOptimizationCandidate
	err := db.WithContext(ctx).Where("cache_key = ?", cacheKey).Take(&candidate).Error
	if err == nil {
		if meshOptimizationCandidateCacheUsable(candidate.Status) {
			return candidate, true, "", nil
		}
		revived, cleanupOutputID, err := s.requeueCandidate(ctx, db, candidate, input)
		return revived, false, cleanupOutputID, err
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return model.MeshOptimizationCandidate{}, false, "", errs.Internal(err)
	}

	now := time.Now()
	expiresAt := now.Add(meshOptimizationCandidateTTL)
	candidateID := uuid.New().String()
	outputObjectID := uuid.New().String()
	if _, err := meshMediaObjectTarget(outputObjectID, "glb", "model/gltf-binary"); err != nil {
		return model.MeshOptimizationCandidate{}, false, "", errs.Internal(err)
	}
	jobID := uuid.New().String()
	status := model.MeshOptimizationCandidateStatusPending
	entityType := optionalMeshOptimizationEntityType(input.EntityType)
	entityID := optionalNonEmptyString(input.EntityID)
	candidate = model.MeshOptimizationCandidate{
		ID:                 candidateID,
		SourceFileID:       input.SourceFileID,
		EntityType:         entityType,
		EntityID:           entityID,
		TargetRatioPercent: input.TargetRatioPercent,
		Method:             input.Method,
		PipelineVersion:    input.PipelineVersion,
		CacheKey:           cacheKey,
		OutputObjectID:     outputObjectID,
		OutputFileID:       nil,
		Status:             status,
		JobID:              &jobID,
		ExpiresAt:          &expiresAt,
		EnqueuedAt:         &now,
		CreatedAt:          now,
		UpdatedAt:          now,
	}
	if err := db.WithContext(ctx).Create(&candidate).Error; err != nil {
		return model.MeshOptimizationCandidate{}, false, "", errs.Internal(err)
	}
	return candidate, false, "", nil
}

func (s *MeshOptimizationService) requeueCandidate(
	ctx context.Context,
	db *gorm.DB,
	candidate model.MeshOptimizationCandidate,
	input MeshOptimizationGenerateInput,
) (model.MeshOptimizationCandidate, string, error) {
	now := time.Now()
	expiresAt := now.Add(meshOptimizationCandidateTTL)
	outputObjectID := uuid.New().String()
	if _, err := meshMediaObjectTarget(outputObjectID, "glb", "model/gltf-binary"); err != nil {
		return model.MeshOptimizationCandidate{}, "", errs.Internal(err)
	}
	previousOutputObjectID := strings.TrimSpace(candidate.OutputObjectID)
	jobID := uuid.New().String()
	updates := structured.Fields{
		"status":                model.MeshOptimizationCandidateStatusPending,
		"output_object_id":      outputObjectID,
		"output_file_id":        nil,
		"job_id":                jobID,
		"error_message":         nil,
		"selected_at":           nil,
		"expires_at":            expiresAt,
		"enqueued_at":           now,
		"processing_started_at": nil,
		"completed_at":          nil,
		"failed_at":             nil,
		"cancelled_at":          nil,
		"updated_at":            now,
	}
	if entityType := optionalMeshOptimizationEntityType(input.EntityType); entityType != nil {
		updates["entity_type"] = *entityType
	}
	if entityID := optionalNonEmptyString(input.EntityID); entityID != nil {
		updates["entity_id"] = *entityID
	}
	if err := db.WithContext(ctx).Model(&model.MeshOptimizationCandidate{}).
		Where("id = ?", candidate.ID).
		Updates(updates).Error; err != nil {
		return model.MeshOptimizationCandidate{}, "", errs.Internal(err)
	}
	candidate.OutputObjectID = outputObjectID
	candidate.OutputFileID = nil
	candidate.JobID = &jobID
	candidate.Status = model.MeshOptimizationCandidateStatusPending
	candidate.EnqueuedAt = &now
	candidate.ExpiresAt = &expiresAt
	candidate.UpdatedAt = now
	candidate.ErrorMessage = nil
	candidate.SelectedAt = nil
	candidate.ProcessingStartedAt = nil
	candidate.CompletedAt = nil
	candidate.FailedAt = nil
	candidate.CancelledAt = nil
	return candidate, previousOutputObjectID, nil
}

func (s *MeshOptimizationService) loadCandidate(ctx context.Context, candidateID string) (*model.MeshOptimizationCandidate, error) {
	candidateID = strings.TrimSpace(candidateID)
	if candidateID == "" {
		return nil, errs.Required("candidate_id")
	}
	var candidate model.MeshOptimizationCandidate
	if err := s.db.WithContext(ctx).Where("id = ?", candidateID).Take(&candidate).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.NotFound("mesh_optimization_candidate", candidateID)
		}
		return nil, errs.Internal(err)
	}
	return &candidate, nil
}

func (s *MeshOptimizationService) loadCandidateForMeshEvent(
	ctx context.Context,
	jobID string,
	correlationID string,
	identity *managev1.MeshOptimizationIdentity,
) (*model.MeshOptimizationCandidate, error) {
	jobID = strings.TrimSpace(jobID)
	correlationID = strings.TrimSpace(correlationID)
	if jobID == "" || correlationID == "" || identity == nil {
		return nil, errs.InvalidArgument("event", "job_id, correlation_id, and identity are required")
	}

	var candidate model.MeshOptimizationCandidate
	if err := s.db.WithContext(ctx).
		Where("id = ? AND job_id = ?", correlationID, jobID).
		Take(&candidate).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.NotFoundMsg("mesh optimization candidate not found")
		}
		return nil, errs.Internal(err)
	}
	entityType, entityID := candidatePermissionTarget(candidate)
	if strings.TrimSpace(identity.GetFileId()) != candidate.SourceFileID ||
		identity.GetEntityType() != entityType ||
		strings.TrimSpace(identity.GetEntityId()) != strings.TrimSpace(entityID) {
		return nil, errs.InvalidArgument("identity", "does not match the allocated mesh optimization job")
	}
	return &candidate, nil
}

func (s *MeshOptimizationService) deleteCandidateAndOutput(ctx context.Context, candidate model.MeshOptimizationCandidate) error {
	now := time.Now()
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return tx.Model(&model.MeshOptimizationCandidate{}).
			Where("id = ?", candidate.ID).
			Updates(structured.Fields{
				"status":         model.MeshOptimizationCandidateStatusCancelled,
				"output_file_id": nil,
				"cancelled_at":   now,
				"updated_at":     now,
			}).Error
	}); err != nil {
		return err
	}
	outputObjectID := strings.TrimSpace(candidate.OutputObjectID)
	if outputObjectID != "" && s.fileDeleter != nil {
		if err := s.fileDeleter.DeleteFileByID(ctx, outputObjectID); err != nil {
			return err
		}
	}
	return s.db.WithContext(ctx).
		Where("id = ?", candidate.ID).
		Delete(&model.MeshOptimizationCandidate{}).Error
}

func (s *MeshOptimizationService) ensureSourceFileReadable(
	ctx context.Context,
	sourceFileID string,
	entityType managev1.TranscodeEntityType,
	entityID string,
) error {
	sourceFileID = strings.TrimSpace(sourceFileID)
	if sourceFileID == "" {
		return errs.Required("source_file_id")
	}
	user := auth.GetUser(ctx)
	if user == nil {
		return errs.AuthenticationRequired()
	}
	if err := s.ensureMeshOptimizationPermission(ctx, entityType, entityID); err != nil {
		return err
	}
	return s.ensureSourceFileLinkedToEntity(ctx, sourceFileID, entityType, entityID)
}

func (s *MeshOptimizationService) ensureSourceFileOptimizable(
	ctx context.Context,
	sourceFileID string,
	entityType managev1.TranscodeEntityType,
	entityID string,
) (*model.File, error) {
	if err := s.ensureMeshOptimizationPermission(ctx, entityType, entityID); err != nil {
		return nil, err
	}
	if err := s.ensureSourceFileLinkedToEntity(ctx, sourceFileID, entityType, entityID); err != nil {
		return nil, err
	}
	var file model.File
	if err := s.db.WithContext(ctx).
		Where("id = ? AND delete_requested_at IS NULL", sourceFileID).
		Take(&file).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.NotFound("file", sourceFileID)
		}
		return nil, errs.Internal(err)
	}
	if strings.TrimSpace(file.MimeType) != "model/gltf-binary" {
		return nil, errs.InvalidArgument("source_file_id", "source file must be a GLB mesh")
	}
	if strings.TrimSpace(file.Extension) != "glb" {
		return nil, errs.FailedPrecondition("source file extension is not canonical")
	}
	return &file, nil
}

func (s *MeshOptimizationService) ensureSourceFileLinkedToEntity(
	ctx context.Context,
	sourceFileID string,
	entityType managev1.TranscodeEntityType,
	entityID string,
) error {
	sourceFileID = strings.TrimSpace(sourceFileID)
	entityID = strings.TrimSpace(entityID)
	if sourceFileID == "" {
		return errs.Required("source_file_id")
	}
	if entityID == "" {
		return errs.Required("entity_id")
	}
	ownerTable := ""
	switch entityType {
	case managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_POST:
		ownerTable = "post"
	case managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_PAGE:
		ownerTable = "page"
	case managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_WORK:
		ownerTable = "work"
	case managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_PROGRAM_EVENT:
		ownerTable = "program_event"
	default:
		return errs.InvalidArgument("entity_type", "unsupported editor entity type")
	}

	var count int64
	if err := s.db.WithContext(ctx).
		Table("content_block_attachment AS cbf").
		Joins("JOIN content_block AS cb ON cb.id = cbf.block_id").
		Joins("JOIN "+ownerTable+" AS owner ON owner.content_document_id = cb.document_id").
		Where("owner.id = ? AND cbf.selector_kind = 'active' AND cbf.file_id = ?", entityID, sourceFileID).
		Count(&count).Error; err != nil {
		return errs.Internal(err)
	}
	if count == 0 {
		return errs.NotFound("source_file", sourceFileID)
	}
	return nil
}

func (s *MeshOptimizationService) ensureMeshOptimizationPermission(
	ctx context.Context,
	entityType managev1.TranscodeEntityType,
	entityID string,
) error {
	user := auth.GetUser(ctx)
	if user == nil {
		return errs.AuthenticationRequired()
	}
	resourceType := meshOptimizationResourceType(entityType)
	if resourceType == "" || strings.TrimSpace(entityID) == "" {
		return errs.PermissionDenied("mesh optimization requires an editable entity")
	}
	return s.ensureUserCanEditResource(ctx, resourceType, entityID, user)
}

func (s *MeshOptimizationService) ensureUserCanEditResource(ctx context.Context, resourceType, entityID string, user *auth.UserInfo) error {
	if s == nil || s.spiceDB == nil {
		return errs.DependencyUnavailable("SpiceDB")
	}
	can, err := meshOptimizationEditCan(resourceType, entityID)
	if err != nil {
		if errors.Is(err, errUnsupportedMeshOptimizationResource) {
			return errs.PermissionDenied("mesh optimization requires an editable entity")
		}
		return errs.InvalidArgument("entity_id", err.Error())
	}
	decision, err := auth.AuthorizationDecision(auth.WithUser(ctx, user), can)
	if err != nil {
		return errs.AuthenticationRequired()
	}
	ok, err := s.spiceDB.Can(ctx, decision)
	if err != nil {
		return errs.DependencyUnavailable("SpiceDB")
	}
	if !ok {
		return errs.NoPermission("optimize mesh for", resourceType)
	}
	return nil
}

func meshOptimizationEditCan(resourceType, resourceID string) (policyv1.Can, error) {
	switch resourceType {
	case "page":
		return policyv1.Page.Edit(resourceID)
	case "post":
		return policyv1.Post.Edit(resourceID)
	case "work":
		return policyv1.Work.Edit(resourceID)
	case "program_event":
		return policyv1.ProgramEvent.Edit(resourceID)
	default:
		return policyv1.Can{}, fmt.Errorf("%w %q", errUnsupportedMeshOptimizationResource, resourceType)
	}
}

func meshOptimizationResourceType(entityType managev1.TranscodeEntityType) string {
	switch entityType {
	case managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_PAGE:
		return "page"
	case managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_POST:
		return "post"
	case managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_WORK:
		return "work"
	case managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_PROGRAM_EVENT:
		return "program_event"
	default:
		return ""
	}
}

func optionalMeshOptimizationEntityType(entityType managev1.TranscodeEntityType) *string {
	if entityType == managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_UNSPECIFIED {
		return nil
	}
	value := entityType.String()
	return &value
}

func candidatePermissionTarget(candidate model.MeshOptimizationCandidate) (managev1.TranscodeEntityType, string) {
	entityType := managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_UNSPECIFIED
	if candidate.EntityType != nil {
		if value, ok := managev1.TranscodeEntityType_value[*candidate.EntityType]; ok {
			entityType = managev1.TranscodeEntityType(value)
		}
	}
	entityID := ""
	if candidate.EntityID != nil {
		entityID = *candidate.EntityID
	}
	return entityType, entityID
}
