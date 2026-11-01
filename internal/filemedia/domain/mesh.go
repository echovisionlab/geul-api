package domain

import (
	"context"
	"errors"
	"time"
)

var ErrMeshCandidateNotFound = errors.New("mesh optimization candidate not found")

const (
	MeshStatusPending    = "pending"
	MeshStatusProcessing = "processing"
	MeshStatusReady      = "ready"
	MeshStatusFailed     = "failed"
	MeshStatusCancelled  = "cancelled"

	MeshMethodDraco            = "DRACO"
	MeshPipelineDracoV1        = "draco-v1"
	MeshPipelineDracoWebpV1    = "draco-webp-v1"
	MeshPipelineParticleMeshV1 = "particle-mesh-v1"
)

type MeshCandidate struct {
	ID                  string
	SourceFileID        string
	OutputObjectID      string
	OutputFileID        *string
	EntityType          *string
	EntityID            *string
	TargetRatioPercent  int32
	Method              string
	PipelineVersion     string
	CacheKey            string
	PublicAssetID       *string
	Status              string
	JobID               *string
	OriginalFileSize    *int64
	OptimizedFileSize   *int64
	ProcessingTimeMs    *int64
	OriginalVertexes    *int64
	OptimizedVertexes   *int64
	OriginalTriangles   *int64
	OptimizedTriangles  *int64
	ErrorMessage        *string
	SelectedAt          *time.Time
	ExpiresAt           *time.Time
	EnqueuedAt          *time.Time
	ProcessingStartedAt *time.Time
	CompletedAt         *time.Time
	FailedAt            *time.Time
	CancelledAt         *time.Time
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

type MeshJob struct {
	JobID, CandidateID, SourceFileID, EntityID                 string
	EntityType, CompressionMethod, TargetRatioPercent, Profile int32
	Source, Output                                             *ObjectTarget
	SlotID, AttemptID                                          *string
	TimestampMS                                                int64
}

type MeshRequeue struct {
	OutputObjectID, JobID string
	EntityType, EntityID  *string
	At, ExpiresAt         time.Time
}

type MeshCompletion struct {
	OutputFileID, Status                  string
	OptimizedFileSize, OriginalFileSize   *int64
	OriginalVertexes, OptimizedVertexes   *int64
	OriginalTriangles, OptimizedTriangles *int64
	ProcessingTimeMS                      *int64
	CompletedAt, ExpiresAt                time.Time
}

type MeshStore interface {
	MeshCandidates(context.Context, string, string) ([]MeshCandidate, error)
	MeshCandidate(context.Context, string, string, bool) (MeshCandidate, error)
	MeshCandidateByCacheKey(context.Context, string) (MeshCandidate, error)
	InsertMeshCandidate(context.Context, MeshCandidate) error
	RequeueMeshCandidate(context.Context, string, MeshRequeue) error
	MarkMeshProcessing(context.Context, string, time.Time, time.Time) error
	SelectMeshCandidate(context.Context, string, time.Time) error
	CancelMeshCandidate(context.Context, string, time.Time, time.Time) error
	RetireMeshCandidate(context.Context, string, time.Time) error
	CompleteMeshCandidate(context.Context, string, MeshCompletion) error
	FailMeshCandidate(context.Context, string, string, time.Time, time.Time) error
	ExpiredMeshCandidates(context.Context, time.Time) ([]MeshCandidate, error)
	DeleteMeshCandidate(context.Context, string) error
	LockMeshCompletionFiles(context.Context, string, string) (bool, error)
	InsertMeshOutputFile(context.Context, File) (bool, error)
	MeshSourceFile(context.Context, string) (File, error)
	MeshSourceLinked(context.Context, string, string, string) (bool, error)
	RequireMeshPublisher(context.Context) error
	EnqueueMeshJob(context.Context, MeshJob) error
}
