package model

import "time"

const (
	MeshOptimizationCandidateStatusPending    = "pending"
	MeshOptimizationCandidateStatusProcessing = "processing"
	MeshOptimizationCandidateStatusReady      = "ready"
	MeshOptimizationCandidateStatusFailed     = "failed"
	MeshOptimizationCandidateStatusCancelled  = "cancelled"

	MeshOptimizationMethodDraco                   = "DRACO"
	MeshOptimizationPipelineVersionDracoV1        = "draco-v1"
	MeshOptimizationPipelineVersionDracoWebpV1    = "draco-webp-v1"
	MeshOptimizationPipelineVersionParticleMeshV1 = "particle-mesh-v1"
)

// MeshOptimizationCandidate represents a cached optimized GLB candidate for an editor mesh file.
type MeshOptimizationCandidate struct {
	ID                  string     `gorm:"column:id;type:uuid;primaryKey;default:gen_random_uuid()"`
	SourceFileID        string     `gorm:"column:source_file_id;type:uuid;not null"`
	OutputObjectID      string     `gorm:"column:output_object_id;type:uuid;not null"`
	OutputFileID        *string    `gorm:"column:output_file_id;type:uuid"`
	EntityType          *string    `gorm:"column:entity_type;type:text"`
	EntityID            *string    `gorm:"column:entity_id;type:uuid"`
	TargetRatioPercent  int32      `gorm:"column:target_ratio_percent;not null"`
	Method              string     `gorm:"column:method;type:text;not null"`
	PipelineVersion     string     `gorm:"column:pipeline_version;type:text;not null"`
	CacheKey            string     `gorm:"column:cache_key;type:text;not null;uniqueIndex"`
	PublicAssetID       *string    `gorm:"column:public_asset_id;type:uuid"`
	Status              string     `gorm:"column:status;type:text;not null"`
	JobID               *string    `gorm:"column:job_id;type:text"`
	OriginalFileSize    *int64     `gorm:"column:original_file_size;type:bigint"`
	OptimizedFileSize   *int64     `gorm:"column:optimized_file_size;type:bigint"`
	ProcessingTimeMs    *int64     `gorm:"column:processing_time_ms;type:bigint"`
	OriginalVertexes    *int64     `gorm:"column:original_vertexes;type:bigint"`
	OptimizedVertexes   *int64     `gorm:"column:optimized_vertexes;type:bigint"`
	OriginalTriangles   *int64     `gorm:"column:original_triangles;type:bigint"`
	OptimizedTriangles  *int64     `gorm:"column:optimized_triangles;type:bigint"`
	ErrorMessage        *string    `gorm:"column:error_message;type:text"`
	SelectedAt          *time.Time `gorm:"column:selected_at;type:timestamptz"`
	ExpiresAt           *time.Time `gorm:"column:expires_at;type:timestamptz"`
	EnqueuedAt          *time.Time `gorm:"column:enqueued_at;type:timestamptz"`
	ProcessingStartedAt *time.Time `gorm:"column:processing_started_at;type:timestamptz"`
	CompletedAt         *time.Time `gorm:"column:completed_at;type:timestamptz"`
	FailedAt            *time.Time `gorm:"column:failed_at;type:timestamptz"`
	CancelledAt         *time.Time `gorm:"column:cancelled_at;type:timestamptz"`
	CreatedAt           time.Time  `gorm:"column:created_at;not null;default:now()"`
	UpdatedAt           time.Time  `gorm:"column:updated_at;not null;default:now()"`
}

func (MeshOptimizationCandidate) TableName() string {
	return "mesh_optimization_candidate"
}
