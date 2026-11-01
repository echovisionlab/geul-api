//go:build integration

package applicationpostgres

import (
	"crypto/sha256"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/filemedia"
	"github.com/echovisionlab/geul-api/internal/filemedia/filemediatest"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"

	"github.com/echovisionlab/geul-api/internal/testutil/pgtest"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestPostgresDeletionStoreContractIntegration(t *testing.T) {
	t.Parallel()
	filemediatest.RunDeletionContract(t, func(t *testing.T) filemediatest.DeletionHarness {
		db := pgtest.SetupAppPostgres(t, pgtest.AppPostgresOptions{ApplyAppSchemaSQL: true}).DB
		return filemediatest.DeletionHarness{Harness: mutationtest.PostgresHarness(db, NewDeletionUnits(db)), SeedFile: func(t *testing.T, f filemedia.File) {
			row := model.File{ID: f.ID, FileName: f.FileName, MimeType: f.MimeType, FileSize: f.FileSize, Extension: f.Extension, CreatedAt: f.CreatedAt, DeleteRequestedAt: f.DeleteRequestedAt}
			hash := sha256.Sum256([]byte(f.ID))
			row.SHA256 = hash[:]
			require.NoError(t, db.Create(&row).Error)
		}, SeedGeneration: func(t *testing.T, g mediaasset.Generation) {
			require.NoError(t, db.Create(&model.MediaGeneration{ID: g.ID, FileID: g.FileID, Kind: g.Kind, ObjectPrefix: g.ObjectPrefix, ManifestName: g.ManifestName, ManifestSHA256: g.ManifestSHA256, ObjectCount: g.ObjectCount, TotalSize: g.TotalSize, Status: string(g.Status), ReadyAt: g.ReadyAt, CreatedAt: g.CreatedAt, UpdatedAt: g.UpdatedAt}).Error)
		}, SeedMeshResult: func(t *testing.T, id, status string, selected bool) {
			row := model.MeshOptimizationCandidate{ID: uuid.NewString(), SourceFileID: id, OutputObjectID: uuid.NewString(), TargetRatioPercent: 50, Method: model.MeshOptimizationMethodDraco, PipelineVersion: model.MeshOptimizationPipelineVersionDracoWebpV1, CacheKey: uuid.NewString(), Status: status}
			if selected {
				now := time.Now()
				row.SelectedAt = &now
			}
			require.NoError(t, db.Create(&row).Error)
		}, SeedReference: func(t *testing.T, id string) {
			require.NoError(t, db.Create(&model.Client{ID: uuid.NewString(), Name: "client", LogoLightFileID: &id, LogoDarkFileID: &id}).Error)
		}}
	})
}
