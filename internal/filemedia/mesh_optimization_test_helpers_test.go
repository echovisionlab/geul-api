package filemedia

import (
	"context"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

//lint:ignore U1000 Integration-tagged mesh optimization tests use this publisher.
type recordingMeshOptimizationPublisher struct {
	jobs []*managev1.MeshOptimizationJob
}

func (p *recordingMeshOptimizationPublisher) PublishMeshOptimizationJob(_ context.Context, job *managev1.MeshOptimizationJob) error {
	p.jobs = append(p.jobs, job)
	return nil
}

//lint:ignore U1000 Integration-tagged mesh optimization tests use this fixture.
type unitMeshOptimizationCandidateFixture struct {
	SourceFileID    string
	OutputFileID    *string
	EntityType      managev1.TranscodeEntityType
	EntityID        string
	Status          string
	JobID           string
	PipelineVersion string
}

func newMeshOptimizationUnitDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`CREATE TABLE file (id text PRIMARY KEY, file_name text NOT NULL, mime_type text NOT NULL, file_size integer NOT NULL, extension text NOT NULL, sha256 blob NOT NULL, duration_seconds integer, ingest_slot_id text, ingest_attempt_id text, delete_requested_at datetime, created_at datetime NOT NULL)`).Error)
	require.NoError(t, db.Exec(`CREATE TABLE public_asset (id text PRIMARY KEY, source_file_id text, kind text, object_key text NOT NULL, extension text NOT NULL, mime_type text NOT NULL)`).Error)
	require.NoError(t, db.Exec(`CREATE TABLE media_generation (id text PRIMARY KEY, file_id text NOT NULL, kind text NOT NULL, object_prefix text NOT NULL, manifest_name text, manifest_sha256 blob, object_count integer, total_size integer, status text, ready_at datetime, retired_at datetime, delete_after datetime, created_at datetime, updated_at datetime)`).Error)
	require.NoError(t, db.Exec(`CREATE TABLE file_derivative (id text PRIMARY KEY, file_id text NOT NULL, type text NOT NULL, asset_id text, media_generation_id text, created_at datetime)`).Error)
	require.NoError(t, db.Exec(`CREATE TABLE mesh_optimization_candidate (id text PRIMARY KEY, source_file_id text NOT NULL, output_object_id text NOT NULL, output_file_id text, entity_type text, entity_id text, target_ratio_percent integer NOT NULL, method text NOT NULL, pipeline_version text NOT NULL, cache_key text NOT NULL UNIQUE, public_asset_id text, status text NOT NULL, job_id text, original_file_size integer, optimized_file_size integer, processing_time_ms integer, original_vertexes integer, optimized_vertexes integer, original_triangles integer, optimized_triangles integer, error_message text, selected_at datetime, expires_at datetime, enqueued_at datetime, processing_started_at datetime, completed_at datetime, failed_at datetime, cancelled_at datetime, created_at datetime NOT NULL, updated_at datetime NOT NULL)`).Error)
	createFileAttachmentReferenceTablesForServiceTests(t, db)
	return db
}

//lint:ignore U1000 Integration-tagged mesh optimization tests use this fixture helper.
func seedUnitMeshOptimizationFile(t *testing.T, db *gorm.DB, _ string, mimeType string) string {
	t.Helper()
	fileID := uuid.NewString()
	require.NoError(t, db.Create(&model.File{ID: fileID, FileName: "mesh.glb", MimeType: mimeType, FileSize: 1024, Extension: "glb", SHA256: make([]byte, 32), CreatedAt: time.Now().UTC()}).Error)
	return fileID
}

//lint:ignore U1000 Integration-tagged mesh optimization tests use this fixture helper.
func seedUnitPageFileLink(t *testing.T, db *gorm.DB, pageID, fileID string) {
	t.Helper()
	documentID := uuid.NewString()
	require.NoError(t, db.Exec(
		`INSERT INTO page (id, content_document_id) VALUES (?, ?)`,
		pageID,
		documentID,
	).Error)
	blockID := uuid.NewString()
	require.NoError(t, db.Exec(
		`INSERT INTO content_block (id, document_id, kind) VALUES (?, ?, ?)`,
		blockID,
		documentID,
		"file",
	).Error)
	require.NoError(t, db.Exec(
		`INSERT INTO content_block_attachment (block_id, reference_path, selector_kind, file_id) VALUES (?, ?, 'active', ?)`,
		blockID,
		"file",
		fileID,
	).Error)
}

//lint:ignore U1000 Integration-tagged mesh optimization tests use this fixture helper.
func seedUnitMeshOptimizationCandidate(t *testing.T, db *gorm.DB, fixture unitMeshOptimizationCandidateFixture) model.MeshOptimizationCandidate {
	t.Helper()
	now := time.Now().UTC()
	pipelineVersion := fixture.PipelineVersion
	if pipelineVersion == "" {
		pipelineVersion = model.MeshOptimizationPipelineVersionDracoWebpV1
	}
	status := fixture.Status
	if status == "" {
		status = model.MeshOptimizationCandidateStatusPending
	}
	jobID := fixture.JobID
	if jobID == "" {
		jobID = uuid.NewString()
	}
	entityType, entityID := fixture.EntityType.String(), fixture.EntityID
	outputObjectID := uuid.NewString()
	if fixture.OutputFileID != nil {
		outputObjectID = *fixture.OutputFileID
	}
	var outputFileID *string
	if fixture.OutputFileID != nil {
		var count int64
		require.NoError(t, db.Table("file").Where("id = ?", *fixture.OutputFileID).Count(&count).Error)
		if count > 0 {
			outputFileID = fixture.OutputFileID
		}
	}
	candidate := model.MeshOptimizationCandidate{ID: uuid.NewString(), SourceFileID: fixture.SourceFileID, OutputObjectID: outputObjectID, OutputFileID: outputFileID, EntityType: &entityType, EntityID: &entityID, TargetRatioPercent: 50, Method: model.MeshOptimizationMethodDraco, PipelineVersion: pipelineVersion, CacheKey: BuildMeshOptimizationCacheKey(fixture.SourceFileID, 50, model.MeshOptimizationMethodDraco, pipelineVersion), Status: status, JobID: &jobID, CreatedAt: now, UpdatedAt: now}
	require.NoError(t, db.Create(&candidate).Error)
	return candidate
}

//lint:ignore U1000 Integration-tagged mesh optimization tests use this assertion helper.
func requireUnitFileAbsent(t *testing.T, db *gorm.DB, fileID string) {
	t.Helper()
	var file model.File
	require.ErrorIs(t, db.First(&file, "id = ?", fileID).Error, gorm.ErrRecordNotFound)
}
