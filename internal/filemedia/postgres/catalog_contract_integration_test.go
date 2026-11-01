//go:build integration

package filemediapostgres_test

import (
	"crypto/sha256"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/mediaasset"
	mediaassetpostgres "github.com/echovisionlab/geul-api/internal/mediaasset/postgres"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"

	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	"github.com/echovisionlab/geul-api/internal/filemedia/filemediatest"
	filemediapostgres "github.com/echovisionlab/geul-api/internal/filemedia/postgres"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/echovisionlab/geul-api/internal/structured"
	"github.com/echovisionlab/geul-api/internal/testutil"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestPostgresCatalogStoreContractIntegration(t *testing.T) {
	t.Parallel()
	filemediatest.RunCatalogContract(t, func(t *testing.T) filemediatest.CatalogHarness {
		db := testutil.SetupAppPostgres(t, testutil.AppPostgresOptions{ApplyAppSchemaSQL: true}).DB
		return filemediatest.CatalogHarness{Harness: mutationtest.PostgresHarness(db, filemediapostgres.NewUnits(db, filemediapostgres.Ports{})), SeedAsset: func(t *testing.T, a mediaasset.Asset) {
			r := mediaassetpostgres.AssetRow(a)
			require.NoError(t, db.Create(&r).Error)
		}, SeedGeneration: func(t *testing.T, g mediaasset.Generation) {
			r := model.MediaGeneration{ID: g.ID, FileID: g.FileID, Kind: g.Kind, ObjectPrefix: g.ObjectPrefix, ManifestName: g.ManifestName, ManifestSHA256: g.ManifestSHA256, ObjectCount: g.ObjectCount, TotalSize: g.TotalSize, Status: string(g.Status), ReadyAt: g.ReadyAt, CreatedAt: g.CreatedAt, UpdatedAt: g.UpdatedAt}
			require.NoError(t, db.Create(&r).Error)
		}, SeedDerivative: func(t *testing.T, r domain.StoredDerivativeDelivery) {
			typ, ok := managev1.FileDerivativeType_value[r.Type]
			require.True(t, ok)
			require.NoError(t, db.Create(&model.FileDerivative{ID: uuid.NewString(), FileID: r.FileID, Type: managev1.FileDerivativeType(typ), AssetID: r.AssetID, MediaGenerationID: r.MediaGenerationID}).Error)
		}, SeedAvatar: func(t *testing.T, id, owner string) {
			require.NoError(t, db.Create(&model.FileIngestBinding{FileID: id, UploadType: "UPLOAD_TYPE_USER_AVATAR", EntityID: owner}).Error)
		}, SeedMesh: func(t *testing.T, w domain.MeshDeliveryWitness, status string) {
			require.NoError(t, db.Create(&model.MeshOptimizationCandidate{ID: w.CandidateID, SourceFileID: w.SourceFileID, OutputObjectID: uuid.NewString(), OutputFileID: &w.OutputFileID, TargetRatioPercent: 50, Method: model.MeshOptimizationMethodDraco, PipelineVersion: model.MeshOptimizationPipelineVersionDracoWebpV1, CacheKey: uuid.NewString(), Status: status, CreatedAt: time.Unix(1, 0)}).Error)
		}, SeedDeliveryOwner: func(t *testing.T, kind, id, doc, status, fileID string) {
			require.Equal(t, "post", kind)
			require.NoError(t, db.Exec(`INSERT INTO content_document(id,profile) VALUES (?::uuid,'post')`, doc).Error)
			require.NoError(t, db.Exec(`INSERT INTO post(id,status,content_document_id,featured_image_file_id) VALUES (?::uuid,?,?::uuid,?::uuid)`, id, status, doc, fileID).Error)
		}, SeedFile: func(t *testing.T, row domain.CatalogRow, pending, output bool) {
			digest := sha256.Sum256([]byte(row.ID))
			duration := int(*row.DurationSeconds)
			var deletion *time.Time
			if pending {
				now := time.Now().UTC()
				deletion = &now
			}
			require.NoError(t, db.Create(&model.File{ID: row.ID, FileName: row.Name, Extension: *row.Extension, MimeType: *row.MimeType, FileSize: *row.FileSize, DurationSeconds: &duration, SHA256: digest[:], CreatedAt: row.CreatedAt, DeleteRequestedAt: deletion}).Error)
			require.NoError(t, db.Table("file").Where("id = ?", row.ID).Updates(structured.Fields{"folder_id": row.ParentID, "updated_at": row.UpdatedAt}).Error)
			if output {
				require.NoError(t, db.Create(&model.MeshOptimizationCandidate{ID: uuid.NewString(), SourceFileID: row.ID, OutputObjectID: uuid.NewString(), OutputFileID: &row.ID, TargetRatioPercent: 50, Method: model.MeshOptimizationMethodDraco, PipelineVersion: model.MeshOptimizationPipelineVersionDracoWebpV1, CacheKey: uuid.NewString(), Status: model.MeshOptimizationCandidateStatusReady}).Error)
			}
		}}
	})
}
