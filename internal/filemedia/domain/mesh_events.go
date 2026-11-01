package domain

const (
	MeshFailureUnspecified = "MESH_OPTIMIZATION_FAILURE_REASON_UNSPECIFIED"
	MeshFailureRejected    = "MESH_OPTIMIZATION_FAILURE_REASON_REJECTED"
)

type MeshIdentity struct {
	EntityType       int32
	EntityID, FileID string
}

type MeshProgressEvent struct {
	JobID, CorrelationID string
	Identity             *MeshIdentity
	TimestampMS          int64
}

type MeshWritten struct {
	FileID   string
	FileSize int64
	SHA256   []byte
}

type MeshOutput struct {
	Written                                       *MeshWritten
	OriginalSizeBytes, OptimizedSizeBytes         *int64
	OriginalVertexCount, OptimizedVertexCount     *int64
	OriginalTriangleCount, OptimizedTriangleCount *int64
	Profile                                       int32
}

type MeshCompleteEvent struct {
	JobID, CorrelationID          string
	Identity                      *MeshIdentity
	Output                        *MeshOutput
	ProcessingTimeMS, TimestampMS int64
}

type MeshFailedEvent struct {
	JobID, CorrelationID string
	Identity             *MeshIdentity
	Reason, Error        string
	TimestampMS          int64
}
