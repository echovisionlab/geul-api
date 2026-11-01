//go:build integration

package filemediapostgres_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/adapters/filemediapolicy"

	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	telemetry "github.com/echovisionlab/geul-telemetry"
	"gorm.io/gorm"

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
		require.NoError(t, db.Exec(`CREATE TABLE filemedia_contract_output (sequence bigserial PRIMARY KEY, kind text NOT NULL, payload jsonb NOT NULL)`).Error)
		return filemediatest.CatalogHarness{SeedSegment: func(t *testing.T, segment domain.PolicySegment) {
			row := model.AudienceSegment{ID: segment.ID, Name: segment.Name, Description: segment.Description, SegmentType: model.AudienceSegmentType(segment.Kind), ArchivedAt: segment.ArchivedAt}
			require.NoError(t, db.Create(&row).Error)
		}, SeedPolicy: func(t *testing.T, r domain.PolicyRelation, ids []string) {
			doc := uuid.NewString()
			if r.Selector.OwnerKind == "track" {
				require.NoError(t, db.Exec(`INSERT INTO content_document(id,profile) VALUES (?::uuid,'compact')`, doc).Error)
				require.NoError(t, db.Exec(`INSERT INTO release(id,content_document_id,type) VALUES (?::uuid,?::uuid,'RELEASE_TYPE_ALBUM')`, r.ResourceID, doc).Error)
				require.NoError(t, db.Exec(`INSERT INTO track(id,release_id,track_number,title,audio_original_file_id,download_audience) VALUES (?::uuid,?::uuid,1,'Track',?::uuid,?)`, r.Selector.EntityID, r.ResourceID, r.FileID, string(r.Audience)).Error)
			} else {
				require.NoError(t, db.Exec(`INSERT INTO content_document(id,profile) VALUES (?::uuid,'post')`, doc).Error)
				require.NoError(t, db.Exec(`INSERT INTO post(id,content_document_id) VALUES (?::uuid,?::uuid)`, r.ResourceID, doc).Error)
				require.NoError(t, db.Exec(`INSERT INTO content_block(id,document_id,container_slot,position,kind) VALUES (?::uuid,?::uuid,'body',0,'file')`, *r.Selector.BlockID, doc).Error)
				require.NoError(t, db.Exec(`INSERT INTO content_block_attachment(block_id,reference_path,selector_kind,file_id,download_audience) VALUES (?::uuid,?,'active',?::uuid,?)`, *r.Selector.BlockID, *r.Selector.ReferencePath, r.FileID, string(r.Audience)).Error)
			}
			require.Empty(t, ids)
		}, Audit: deletionContractOutputs{}, SeedAssetBinding: func(t *testing.T, asset, id string) {
			require.NoError(t, db.Create(&model.PublicAssetBinding{AssetID: asset, SourceFileID: &id, OwnerType: "post", OwnerID: uuid.NewString(), BindingKey: "featured_image"}).Error)
		}, Asset: func(t *testing.T, id string) mediaasset.Asset {
			var a model.PublicAsset
			require.NoError(t, db.First(&a, "id = ?", id).Error)
			return mediaassetpostgres.Asset(a)
		}, Outbox: func(t *testing.T) []domain.DeletionTarget {
			var rows []struct{ Payload []byte }
			require.NoError(t, db.Table("filemedia_contract_output").Select("payload").Where("kind = ?", "delete").Order("sequence").Find(&rows).Error)
			targets := make([]domain.DeletionTarget, len(rows))
			for i, r := range rows {
				require.NoError(t, json.Unmarshal(r.Payload, &targets[i]))
			}
			return targets
		}, AuditCount: func(t *testing.T) int {
			var n int64
			require.NoError(t, db.Table("filemedia_contract_output").Where("kind = ?", "audit").Count(&n).Error)
			return int(n)
		}, Mesh: func(t *testing.T, id string) (filemediatest.MeshRecord, bool) {
			var m model.MeshOptimizationCandidate
			err := db.First(&m, "id = ?", id).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return filemediatest.MeshRecord{}, false
			}
			require.NoError(t, err)
			expires, updated := time.Time{}, m.UpdatedAt
			if m.ExpiresAt != nil {
				expires = *m.ExpiresAt
			}
			return filemediatest.MeshRecord{Status: m.Status, CancelledAt: m.CancelledAt, ExpiresAt: expires, UpdatedAt: updated}, true
		}, Harness: mutationtest.PostgresHarness(db, filemediapostgres.NewUnits(db, filemediapostgres.Ports{DeletionQueue: deletionContractOutputs{}, PolicyAudiences: filemediapolicy.Audiences{}})), SeedAsset: func(t *testing.T, a mediaasset.Asset) {
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

type deletionContractOutputs struct{}

func (deletionContractOutputs) EnqueueFileDeletion(ctx context.Context, executor eventpkg.DBTX, target domain.DeletionTarget) error {
	payload, err := json.Marshal(target)
	if err != nil {
		return err
	}
	_, err = executor.ExecContext(ctx, "INSERT INTO filemedia_contract_output(kind,payload) VALUES ('delete',$1::jsonb)", string(payload))
	return err
}
func (deletionContractOutputs) AppendDomainAuditInTransaction(ctx context.Context, tx *gorm.DB, record telemetry.AuditRecord) error {
	payload, err := json.Marshal(record)
	if err != nil {
		return err
	}
	return tx.WithContext(ctx).Exec("INSERT INTO filemedia_contract_output(kind,payload) VALUES ('audit',?::jsonb)", string(payload)).Error
}
