package filemedia

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	"github.com/echovisionlab/geul-api/internal/mutation"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	mediaauth "github.com/echovisionlab/geul-api/internal/mediaauth"
	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/dbtime"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

const meshOptimizationCandidateTTL = 7 * 24 * time.Hour

var errUnsupportedMeshOptimizationResource = errors.New("unsupported mesh optimization resource")

type MeshOptimizationFileDeleter interface {
	DeleteFileByID(ctx context.Context, fileID string) error
}

type MeshOptimizationService struct {
	units       mutation.UnitOfWork[Store]
	fileDeleter MeshOptimizationFileDeleter
	authorizer  mutation.Authorizer
}

type MeshOptimizationDeps struct {
	Units       mutation.UnitOfWork[Store]
	FileDeleter MeshOptimizationFileDeleter
	Authorizer  mutation.Authorizer
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
	Candidate domain.MeshCandidate
	CacheHit  bool
	Enqueued  bool
}

func NewMeshOptimizationService(deps MeshOptimizationDeps) *MeshOptimizationService {
	if deps.Units == nil {
		panic("mesh optimization service: units are required")
	}
	return &MeshOptimizationService{units: deps.Units, fileDeleter: deps.FileDeleter, authorizer: deps.Authorizer}
}

func BuildMeshOptimizationCacheKey(sourceFileID string, targetRatioPercent int32, method string, pipelineVersion string) string {
	return strings.Join([]string{
		strings.TrimSpace(sourceFileID),
		fmt.Sprintf("%d", targetRatioPercent),
		normalizeMeshOptimizationMethod(method),
		normalizeMeshOptimizationPipelineVersion(pipelineVersion),
	}, ":")
}

func meshOptimizationJobFromCandidate(candidate domain.MeshCandidate, source File) (*domain.MeshJob, error) {
	if err := validateMeshOptimizationTargetRatioPercent(candidate.TargetRatioPercent); err != nil {
		return nil, err
	}
	if candidate.JobID == nil || strings.TrimSpace(*candidate.JobID) == "" {
		return nil, domainerr.Required("job_id")
	}
	if strings.TrimSpace(candidate.OutputObjectID) == "" {
		return nil, domainerr.Required("output_object_id")
	}
	sourceTarget, err := domain.CanonicalObjectTarget(source)
	if err != nil {
		return nil, err
	}
	outputTarget, err := domain.CanonicalObjectTarget(File{ID: candidate.OutputObjectID, Extension: "glb", MimeType: "model/gltf-binary"})
	if err != nil {
		return nil, err
	}
	entityType, entityID := candidatePermissionTarget(candidate)
	return &domain.MeshJob{JobID: strings.TrimSpace(*candidate.JobID), CandidateID: candidate.ID,
		EntityType: int32(entityType), EntityID: entityID, SourceFileID: candidate.SourceFileID,
		Source: sourceTarget, SlotID: source.IngestSlotID, AttemptID: source.IngestAttemptID,
		CompressionMethod: int32(meshOptimizationCompressionMethod(candidate.Method)), TargetRatioPercent: candidate.TargetRatioPercent,
		Profile: int32(meshOptimizationProfileForPipelineVersion(candidate.PipelineVersion)), Output: outputTarget, TimestampMS: time.Now().UnixMilli(),
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
	if normalizeMeshOptimizationMethod(method) != domain.MeshMethodDraco {
		return domainerr.InvalidField("method", "only DRACO is supported")
	}
	return nil
}

func validateMeshOptimizationTargetRatioPercent(targetRatioPercent int32) error {
	if targetRatioPercent < 1 || targetRatioPercent > 100 {
		return domainerr.InvalidField("target_ratio_percent", "must be between 1 and 100")
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
	if normalizeMeshOptimizationMethod(method) == domain.MeshMethodDraco {
		return managev1.MeshOptimizationCompressionMethod_MESH_OPTIMIZATION_COMPRESSION_METHOD_DRACO
	}
	return managev1.MeshOptimizationCompressionMethod_MESH_OPTIMIZATION_COMPRESSION_METHOD_UNSPECIFIED
}

func normalizeMeshOptimizationMethod(method string) string {
	method = strings.TrimSpace(method)
	if method == "" {
		return domain.MeshMethodDraco
	}
	return strings.ToUpper(method)
}

func normalizeMeshOptimizationPipelineVersion(pipelineVersion string) string {
	pipelineVersion = strings.TrimSpace(pipelineVersion)
	if pipelineVersion == "" {
		return domain.MeshPipelineDracoWebpV1
	}
	return pipelineVersion
}

func meshOptimizationPipelineVersionForProfile(profile managev1.MeshOptimizationProfile) (string, error) {
	switch profile {
	case managev1.MeshOptimizationProfile_MESH_OPTIMIZATION_PROFILE_UNSPECIFIED,
		managev1.MeshOptimizationProfile_MESH_OPTIMIZATION_PROFILE_DRACO_WEBP_V1:
		return domain.MeshPipelineDracoWebpV1, nil
	case managev1.MeshOptimizationProfile_MESH_OPTIMIZATION_PROFILE_PARTICLE_MESH_V1:
		return domain.MeshPipelineParticleMeshV1, nil
	default:
		return "", domainerr.InvalidField("profile", "unsupported mesh optimization profile")
	}
}

func meshOptimizationProfileForPipelineVersion(pipelineVersion string) managev1.MeshOptimizationProfile {
	switch normalizeMeshOptimizationPipelineVersion(pipelineVersion) {
	case domain.MeshPipelineParticleMeshV1:
		return managev1.MeshOptimizationProfile_MESH_OPTIMIZATION_PROFILE_PARTICLE_MESH_V1
	case domain.MeshPipelineDracoV1,
		domain.MeshPipelineDracoWebpV1:
		return managev1.MeshOptimizationProfile_MESH_OPTIMIZATION_PROFILE_DRACO_WEBP_V1
	default:
		return managev1.MeshOptimizationProfile_MESH_OPTIMIZATION_PROFILE_UNSPECIFIED
	}
}

func validateMeshOptimizationOutputProfile(
	candidate domain.MeshCandidate,
	outputProfile managev1.MeshOptimizationProfile,
) error {
	expected := meshOptimizationProfileForPipelineVersion(candidate.PipelineVersion)
	if expected == managev1.MeshOptimizationProfile_MESH_OPTIMIZATION_PROFILE_DRACO_WEBP_V1 &&
		outputProfile == managev1.MeshOptimizationProfile_MESH_OPTIMIZATION_PROFILE_UNSPECIFIED {
		return nil
	}
	if outputProfile != expected {
		return domainerr.InvalidField("output.profile", "does not match the requested mesh optimization profile")
	}
	return nil
}

func (s *MeshOptimizationService) ListCandidates(ctx context.Context, input MeshOptimizationListInput) ([]domain.MeshCandidate, error) {
	input.SourceFileID = strings.TrimSpace(input.SourceFileID)
	input.EntityID = strings.TrimSpace(input.EntityID)
	if input.SourceFileID == "" {
		return nil, domainerr.Required("source_file_id")
	}
	if err := s.ensureSourceFileReadable(ctx, input.SourceFileID, input.EntityType, input.EntityID); err != nil {
		return nil, err
	}
	pipelineVersion, err := meshOptimizationPipelineVersionForProfile(input.Profile)
	if err != nil {
		return nil, err
	}

	var candidates []domain.MeshCandidate
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		candidates, err = store.MeshCandidates(ctx, input.SourceFileID, pipelineVersion)
		return err
	}); err != nil {
		return nil, err
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
		return nil, domainerr.Required("source_file_id")
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
	if err := s.units.Read(ctx, func(store Store) error {
		if err := store.RequireMeshPublisher(ctx); err != nil {
			return domainerr.Precondition(err.Error())
		}
		return nil
	}); err != nil {
		return nil, err
	}

	var (
		candidate       domain.MeshCandidate
		cacheHit        bool
		cleanupOutputID string
		enqueued        bool
	)
	if err := s.units.Transaction(ctx, false, func(unit mutation.Tx[Store]) error {
		store := unit.Store()
		var err error
		candidate, cacheHit, cleanupOutputID, err = s.findOrCreateCandidate(ctx, store, input, cacheKey)
		if err != nil || (cacheHit && meshOptimizationCandidateCacheUsable(candidate.Status)) {
			return err
		}
		job, err := meshOptimizationJobFromCandidate(candidate, *source)
		if err != nil {
			return err
		}
		if err := store.EnqueueMeshJob(ctx, *job); err != nil {
			return err
		}
		enqueued = true
		return nil
	}); err != nil {
		return nil, fmt.Errorf("failed to enqueue mesh optimization: %w", err)
	}
	if cleanupOutputID != "" && s.fileDeleter != nil {
		if err := s.fileDeleter.DeleteFileByID(ctx, cleanupOutputID); err != nil {
			slog.ErrorContext(ctx, "Failed to delete superseded mesh optimization output", "file_id", cleanupOutputID, failurecause.Attr(err))
		}
	}
	return &MeshOptimizationGenerateResult{Candidate: candidate, CacheHit: cacheHit, Enqueued: enqueued}, nil
}

func (s *MeshOptimizationService) MarkCandidateProcessing(ctx context.Context, candidateID string, startedAt time.Time) error {
	candidateID = strings.TrimSpace(candidateID)
	if candidateID == "" {
		return domainerr.Required("candidate_id")
	}
	if startedAt.IsZero() {
		startedAt = time.Now()
	}
	return s.units.Transaction(ctx, false, func(unit mutation.Tx[Store]) error {
		return unit.Store().MarkMeshProcessing(ctx, candidateID, startedAt, startedAt.Add(meshOptimizationCandidateTTL))
	})
}

func (s *MeshOptimizationService) UseCandidate(ctx context.Context, candidateID string) (*domain.MeshCandidate, error) {
	candidate, err := s.loadCandidate(ctx, candidateID)
	if err != nil {
		return nil, err
	}
	entityType, entityID := candidatePermissionTarget(*candidate)
	if err := s.ensureSourceFileReadable(ctx, candidate.SourceFileID, entityType, entityID); err != nil {
		return nil, err
	}
	if candidate.Status != domain.MeshStatusReady || candidate.OutputFileID == nil {
		return nil, domainerr.Precondition("mesh optimization candidate is not ready")
	}
	now := dbtime.Now()
	if err := s.units.Transaction(ctx, false, func(unit mutation.Tx[Store]) error { return unit.Store().SelectMeshCandidate(ctx, candidate.ID, now) }); err != nil {
		return nil, err
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
	if candidate.Status == domain.MeshStatusPending ||
		candidate.Status == domain.MeshStatusProcessing {
		now := time.Now()
		if err := s.units.Transaction(ctx, false, func(unit mutation.Tx[Store]) error {
			return unit.Store().CancelMeshCandidate(ctx, candidate.ID, now, now.Add(meshOptimizationCandidateTTL))
		}); err != nil {
			return err
		}
		return nil
	}
	return s.deleteCandidateAndOutput(ctx, *candidate)
}

func (s *MeshOptimizationService) HandleProgress(
	ctx context.Context,
	event *managev1.MeshOptimizationProgressEvent,
) (_ *domain.MeshCandidate, resultErr error) {
	if event == nil {
		return nil, domainerr.Required("event")
	}
	if strings.TrimSpace(event.GetJobId()) == "" {
		return nil, domainerr.Required("job_id")
	}
	candidate, err := s.loadCandidateForMeshEvent(ctx, event.GetJobId(), event.GetCorrelationId(), event.GetIdentity())
	if err != nil {
		return nil, err
	}
	if candidate.Status == domain.MeshStatusPending {
		if err := s.MarkCandidateProcessing(ctx, candidate.ID, unixMillisTime(event.GetTimestampMs())); err != nil {
			return nil, err
		}
		return s.loadCandidate(ctx, candidate.ID)
	}
	return candidate, nil
}

func (s *MeshOptimizationService) HandleComplete(
	ctx context.Context,
	event *managev1.MeshOptimizationCompleteEvent,
) (_ *domain.MeshCandidate, resultErr error) {
	completion, candidate, err := s.prepareMeshOptimizationCompletion(ctx, event)
	if err != nil {
		return nil, err
	}
	sourceAcceptsResults := false
	err = s.units.Transaction(ctx, false, func(unit mutation.Tx[Store]) error {
		store := unit.Store()
		updated, acceptsResults, err := applyMeshOptimizationCompletion(ctx, store, candidate, completion)
		candidate = updated
		sourceAcceptsResults = acceptsResults
		return err
	})
	if err != nil {
		return nil, err
	}
	if !sourceAcceptsResults || candidate.Status == domain.MeshStatusCancelled {
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
) (meshOptimizationCompletion, *domain.MeshCandidate, error) {
	if event == nil {
		return meshOptimizationCompletion{}, nil, domainerr.Required("event")
	}
	if strings.TrimSpace(event.GetJobId()) == "" {
		return meshOptimizationCompletion{}, nil, domainerr.Required("job_id")
	}
	output := event.GetOutput()
	if output == nil {
		return meshOptimizationCompletion{}, nil, domainerr.Required("output")
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
		return meshOptimizationCompletion{}, domainerr.Required("output.written")
	}
	outputFileID := strings.TrimSpace(written.GetFileId())
	expectedOutputObjectID = strings.TrimSpace(expectedOutputObjectID)
	if expectedOutputObjectID == "" {
		return meshOptimizationCompletion{}, domainerr.Precondition("mesh optimization output was not allocated")
	}
	if outputFileID != expectedOutputObjectID {
		return meshOptimizationCompletion{}, domainerr.InvalidField("output.written.file_id", "does not match the allocated output file")
	}
	if output.OptimizedSizeBytes == nil {
		return meshOptimizationCompletion{}, domainerr.Required("optimized_size_bytes")
	}
	if written.GetFileSize() <= 0 || written.GetFileSize() != output.GetOptimizedSizeBytes() {
		return meshOptimizationCompletion{}, domainerr.InvalidField("output.written.file_size", "must match optimized_size_bytes and be positive")
	}
	if len(written.GetSha256()) != 32 {
		return meshOptimizationCompletion{}, domainerr.InvalidField("output.written.sha256", "must be a 32-byte SHA-256 digest")
	}
	completedAt := unixMillisTime(event.GetTimestampMs())
	return meshOptimizationCompletion{
		event: event, output: output, outputFileID: outputFileID,
		fileSize: *output.OptimizedSizeBytes, sha256: append([]byte(nil), written.GetSha256()...),
		completedAt: completedAt, expiresAt: completedAt.Add(meshOptimizationCandidateTTL),
	}, nil
}

func applyMeshOptimizationCompletion(
	ctx context.Context,
	store Store,
	candidate *domain.MeshCandidate,
	completion meshOptimizationCompletion,
) (*domain.MeshCandidate, bool, error) {
	acceptsResults, err := store.LockMeshCompletionFiles(ctx, candidate.SourceFileID, completion.outputFileID)
	if err != nil {
		return nil, false, err
	}
	lockedCandidate, err := store.MeshCandidate(ctx, candidate.ID, "", true)
	if err != nil {
		return nil, false, err
	}
	if err := ensureMeshOptimizationOutputFile(ctx, store, completion); err != nil {
		return nil, false, err
	}
	updates, status := meshOptimizationCompletionUpdates(completion, acceptsResults, lockedCandidate.Status)
	if err := store.CompleteMeshCandidate(ctx, lockedCandidate.ID, updates); err != nil {
		return nil, false, err
	}
	lockedCandidate.Status = status
	return &lockedCandidate, acceptsResults, nil
}

func ensureMeshOptimizationOutputFile(ctx context.Context, store Store, completion meshOptimizationCompletion) error {
	const mimeType = "model/gltf-binary"
	outputFile := File{
		ID: completion.outputFileID, FileName: completion.outputFileID, MimeType: mimeType,
		FileSize: completion.fileSize, Extension: "glb", SHA256: completion.sha256,
		CreatedAt: completion.completedAt,
	}
	created, err := store.InsertMeshOutputFile(ctx, outputFile)
	if err != nil || created {
		return err
	}
	existing, err := store.IngestFile(ctx, completion.outputFileID)
	if err != nil {
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
) (domain.MeshCompletion, string) {
	output := completion.output
	status := domain.MeshStatusCancelled
	if sourceAcceptsResults && currentStatus != domain.MeshStatusCancelled {
		status = domain.MeshStatusReady
	}
	return domain.MeshCompletion{OutputFileID: completion.outputFileID, Status: status,
		OptimizedFileSize: optionalEventInt64(output.OptimizedSizeBytes), OriginalFileSize: optionalEventInt64(output.OriginalSizeBytes),
		OriginalVertexes: optionalEventInt64(output.OriginalVertexCount), OptimizedVertexes: optionalEventInt64(output.OptimizedVertexCount),
		OriginalTriangles: optionalEventInt64(output.OriginalTriangleCount), OptimizedTriangles: optionalEventInt64(output.OptimizedTriangleCount),
		ProcessingTimeMS: optionalPositiveEventInt64(completion.event.GetProcessingTimeMs()), CompletedAt: completion.completedAt, ExpiresAt: completion.expiresAt,
	}, status
}

func (s *MeshOptimizationService) HandleFailed(ctx context.Context, event *managev1.MeshOptimizationFailEvent) (_ *domain.MeshCandidate, resultErr error) {
	if event == nil {
		return nil, domainerr.Required("event")
	}
	if strings.TrimSpace(event.GetJobId()) == "" {
		return nil, domainerr.Required("job_id")
	}
	candidate, err := s.loadCandidateForMeshEvent(ctx, event.GetJobId(), event.GetCorrelationId(), event.GetIdentity())
	if err != nil {
		return nil, err
	}
	if candidate.Status == domain.MeshStatusCancelled {
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
	level := slog.LevelError
	if event.GetReason() == managev1.MeshOptimizationFailureReason_MESH_OPTIMIZATION_FAILURE_REASON_REJECTED {
		level = slog.LevelWarn
	}
	slog.Log(ctx, level, "Mesh optimization processing failed", failurecause.Attr(fmt.Errorf("%s", errText)), "job_id", event.GetJobId(), "correlation_id", event.GetCorrelationId(), "candidate_id", candidate.ID, "source_file_id", candidate.SourceFileID, "reason", event.GetReason().String())
	if err := s.units.Transaction(ctx, false, func(unit mutation.Tx[Store]) error {
		return unit.Store().FailMeshCandidate(ctx, candidate.ID, errText, failedAt, expiresAt)
	}); err != nil {
		return nil, err
	}
	return s.loadCandidate(ctx, candidate.ID)
}

func (s *MeshOptimizationService) ExpireStaleUnselectedCandidates(ctx context.Context, now time.Time) (int64, error) {
	if now.IsZero() {
		now = time.Now()
	}
	var candidates []domain.MeshCandidate
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		candidates, err = store.ExpiredMeshCandidates(ctx, now)
		return err
	}); err != nil {
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
	case domain.MeshStatusPending,
		domain.MeshStatusProcessing,
		domain.MeshStatusReady:
		return true
	default:
		return false
	}
}

func MeshOptimizationOutputProtectionStatuses() []string {
	return domain.MeshOutputProtectionStatuses()
}

func (s *MeshOptimizationService) findOrCreateCandidate(
	ctx context.Context,
	store Store,
	input MeshOptimizationGenerateInput,
	cacheKey string,
) (domain.MeshCandidate, bool, string, error) {
	candidate, err := store.MeshCandidateByCacheKey(ctx, cacheKey)
	if err == nil {
		if meshOptimizationCandidateCacheUsable(candidate.Status) {
			return candidate, true, "", nil
		}
		revived, cleanupOutputID, err := s.requeueCandidate(ctx, store, candidate, input)
		return revived, false, cleanupOutputID, err
	}
	if !errors.Is(err, domain.ErrMeshCandidateNotFound) {
		return domain.MeshCandidate{}, false, "", err
	}

	now := dbtime.Now()
	expiresAt := now.Add(meshOptimizationCandidateTTL)
	candidateID := uuid.New().String()
	outputObjectID := uuid.New().String()
	if _, err := domain.CanonicalObjectTarget(File{ID: outputObjectID, Extension: "glb", MimeType: "model/gltf-binary"}); err != nil {
		return domain.MeshCandidate{}, false, "", err
	}
	jobID := uuid.New().String()
	status := domain.MeshStatusPending
	entityType := optionalMeshOptimizationEntityType(input.EntityType)
	entityID := optionalNonEmptyString(input.EntityID)
	candidate = domain.MeshCandidate{
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
	if err := store.InsertMeshCandidate(ctx, candidate); err != nil {
		return domain.MeshCandidate{}, false, "", err
	}
	return candidate, false, "", nil
}

func (s *MeshOptimizationService) requeueCandidate(
	ctx context.Context,
	store Store,
	candidate domain.MeshCandidate,
	input MeshOptimizationGenerateInput,
) (domain.MeshCandidate, string, error) {
	now := dbtime.Now()
	expiresAt := now.Add(meshOptimizationCandidateTTL)
	outputObjectID := uuid.New().String()
	if _, err := domain.CanonicalObjectTarget(File{ID: outputObjectID, Extension: "glb", MimeType: "model/gltf-binary"}); err != nil {
		return domain.MeshCandidate{}, "", err
	}
	previousOutputObjectID := strings.TrimSpace(candidate.OutputObjectID)
	jobID := uuid.New().String()
	entityType := optionalMeshOptimizationEntityType(input.EntityType)
	entityID := optionalNonEmptyString(input.EntityID)
	if err := store.RequeueMeshCandidate(ctx, candidate.ID, domain.MeshRequeue{OutputObjectID: outputObjectID, JobID: jobID, EntityType: entityType, EntityID: entityID, At: now, ExpiresAt: expiresAt}); err != nil {
		return domain.MeshCandidate{}, "", err
	}
	candidate.OutputObjectID = outputObjectID
	candidate.OutputFileID = nil
	candidate.JobID = &jobID
	candidate.Status = domain.MeshStatusPending
	candidate.EnqueuedAt = &now
	candidate.ExpiresAt = &expiresAt
	candidate.UpdatedAt = now
	candidate.ErrorMessage = nil
	candidate.SelectedAt = nil
	candidate.ProcessingStartedAt = nil
	candidate.CompletedAt = nil
	candidate.FailedAt = nil
	candidate.CancelledAt = nil
	if entityType != nil {
		candidate.EntityType = entityType
	}
	if entityID != nil {
		candidate.EntityID = entityID
	}
	return candidate, previousOutputObjectID, nil
}

func (s *MeshOptimizationService) loadCandidate(ctx context.Context, candidateID string) (*domain.MeshCandidate, error) {
	candidateID = strings.TrimSpace(candidateID)
	if candidateID == "" {
		return nil, domainerr.Required("candidate_id")
	}
	var candidate domain.MeshCandidate
	err := s.units.Read(ctx, func(store Store) error {
		var err error
		candidate, err = store.MeshCandidate(ctx, candidateID, "", false)
		return err
	})
	if err != nil {
		if errors.Is(err, domain.ErrMeshCandidateNotFound) {
			return nil, domainerr.ResourceNotFound("mesh_optimization_candidate", candidateID)
		}
		return nil, err
	}
	return &candidate, nil
}

func (s *MeshOptimizationService) loadCandidateForMeshEvent(
	ctx context.Context,
	jobID string,
	correlationID string,
	identity *managev1.MeshOptimizationIdentity,
) (*domain.MeshCandidate, error) {
	jobID = strings.TrimSpace(jobID)
	correlationID = strings.TrimSpace(correlationID)
	if jobID == "" || correlationID == "" || identity == nil {
		return nil, domainerr.InvalidField("event", "job_id, correlation_id, and identity are required")
	}

	var candidate domain.MeshCandidate
	err := s.units.Read(ctx, func(store Store) error {
		var err error
		candidate, err = store.MeshCandidate(ctx, correlationID, jobID, false)
		return err
	})
	if err != nil {
		if errors.Is(err, domain.ErrMeshCandidateNotFound) {
			return nil, domainerr.New(domainerr.NotFound, "", "mesh optimization candidate not found")
		}
		return nil, err
	}
	entityType, entityID := candidatePermissionTarget(candidate)
	if strings.TrimSpace(identity.GetFileId()) != candidate.SourceFileID ||
		identity.GetEntityType() != entityType ||
		strings.TrimSpace(identity.GetEntityId()) != strings.TrimSpace(entityID) {
		return nil, domainerr.InvalidField("identity", "does not match the allocated mesh optimization job")
	}
	return &candidate, nil
}

func (s *MeshOptimizationService) deleteCandidateAndOutput(ctx context.Context, candidate domain.MeshCandidate) error {
	now := time.Now()
	if err := s.units.Transaction(ctx, false, func(unit mutation.Tx[Store]) error { return unit.Store().RetireMeshCandidate(ctx, candidate.ID, now) }); err != nil {
		return err
	}
	outputObjectID := strings.TrimSpace(candidate.OutputObjectID)
	if outputObjectID != "" && s.fileDeleter != nil {
		if err := s.fileDeleter.DeleteFileByID(ctx, outputObjectID); err != nil {
			return err
		}
	}
	return s.units.Transaction(ctx, false, func(unit mutation.Tx[Store]) error { return unit.Store().DeleteMeshCandidate(ctx, candidate.ID) })
}

func (s *MeshOptimizationService) ensureSourceFileReadable(
	ctx context.Context,
	sourceFileID string,
	entityType managev1.TranscodeEntityType,
	entityID string,
) error {
	sourceFileID = strings.TrimSpace(sourceFileID)
	if sourceFileID == "" {
		return domainerr.Required("source_file_id")
	}
	user := auth.GetUser(ctx)
	if user == nil {
		return domainerr.AuthenticationRequired()
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
) (*File, error) {
	if err := s.ensureMeshOptimizationPermission(ctx, entityType, entityID); err != nil {
		return nil, err
	}
	if err := s.ensureSourceFileLinkedToEntity(ctx, sourceFileID, entityType, entityID); err != nil {
		return nil, err
	}
	var file File
	err := s.units.Read(ctx, func(store Store) error {
		var err error
		file, err = store.MeshSourceFile(ctx, sourceFileID)
		return err
	})
	if err != nil {
		if errors.Is(err, domain.ErrFileNotFound) {
			return nil, domainerr.ResourceNotFound("file", sourceFileID)
		}
		return nil, err
	}
	if strings.TrimSpace(file.MimeType) != "model/gltf-binary" {
		return nil, domainerr.InvalidField("source_file_id", "source file must be a GLB mesh")
	}
	if strings.TrimSpace(file.Extension) != "glb" {
		return nil, domainerr.Precondition("source file extension is not canonical")
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
		return domainerr.Required("source_file_id")
	}
	if entityID == "" {
		return domainerr.Required("entity_id")
	}
	var ownerTable string
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
		return domainerr.InvalidField("entity_type", "unsupported editor entity type")
	}

	var linked bool
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		linked, err = store.MeshSourceLinked(ctx, ownerTable, entityID, sourceFileID)
		return err
	}); err != nil {
		return err
	}
	if !linked {
		return domainerr.ResourceNotFound("source_file", sourceFileID)
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
		return domainerr.AuthenticationRequired()
	}
	resourceType := meshOptimizationResourceType(entityType)
	if resourceType == "" || strings.TrimSpace(entityID) == "" {
		return domainerr.Denied("mesh optimization requires an editable entity")
	}
	return s.ensureUserCanEditResource(ctx, resourceType, entityID, user)
}

func (s *MeshOptimizationService) ensureUserCanEditResource(ctx context.Context, resourceType, entityID string, user *auth.UserInfo) error {
	if s == nil || s.authorizer == nil {
		return domainerr.DependencyUnavailable("SpiceDB")
	}
	can, err := meshOptimizationEditCan(resourceType, entityID)
	if err != nil {
		if errors.Is(err, errUnsupportedMeshOptimizationResource) {
			return domainerr.Denied("mesh optimization requires an editable entity")
		}
		return domainerr.InvalidField("entity_id", err.Error())
	}
	decision, err := auth.AuthorizationDecision(auth.WithUser(ctx, user), can)
	if err != nil {
		return domainerr.AuthenticationRequired().WithCause(err)
	}
	ok, err := s.authorizer.Can(ctx, decision)
	if err != nil {
		return domainerr.DependencyUnavailable("SpiceDB").WithCause(err)
	}
	if !ok {
		return domainerr.NoPermission("optimize mesh for", resourceType)
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

func candidatePermissionTarget(candidate domain.MeshCandidate) (managev1.TranscodeEntityType, string) {
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
