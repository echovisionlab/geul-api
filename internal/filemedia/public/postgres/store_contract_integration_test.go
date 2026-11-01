//go:build integration

package filemediapublicpostgres

import (
	"context"
	"crypto/sha256"
	"testing"
	"time"

	public "github.com/echovisionlab/geul-api/internal/filemedia/public"
	"github.com/echovisionlab/geul-api/internal/filemedia/public/publictest"
	"github.com/echovisionlab/geul-api/internal/mediaauth"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"

	"github.com/echovisionlab/geul-api/internal/testutil/pgtest"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestPostgresPublicStoreContractIntegration(t *testing.T) {
	t.Parallel()
	publictest.RunStoreContract(t, func(t *testing.T) publictest.Harness {
		db := pgtest.SetupAppPostgres(t, pgtest.AppPostgresOptions{ApplyAppSchemaSQL: true}).DB
		return publictest.Harness{Harness: mutationtest.PostgresHarness(db, NewUnits(db, Ports{})), SeedFile: func(t *testing.T, f publictest.FileRecord) {
			now := time.Now().UTC()
			var pending *time.Time
			if f.PendingDeletion {
				pending = &now
			}
			digest := sha256.Sum256([]byte(f.ID))
			require.NoError(t, db.Create(&model.File{ID: f.ID, FileName: *f.FileName, Extension: f.Extension, MimeType: f.MimeType, FileSize: f.FileSize, DurationSeconds: f.DurationSeconds, SHA256: digest[:], DeleteRequestedAt: pending}).Error)
		}, SeedAsset: func(t *testing.T, a publictest.AssetRecord) {
			key, err := mediaauth.AssetObjectKey(a.AssetID, a.Extension)
			require.NoError(t, err)
			row := model.PublicAsset{ID: a.AssetID, SourceFileID: a.SourceFileID, Kind: a.Kind, ObjectKey: key, Extension: a.Extension, MimeType: a.MimeType, Disposition: a.Disposition, DownloadFilename: a.DownloadFilename, Status: a.Status, CreatedAt: a.CreatedAt, UpdatedAt: a.CreatedAt}
			if a.Status == "ready" {
				row.FileSize = &a.FileSize
				row.SHA256 = a.SHA256
				row.ReadyAt = &a.CreatedAt
			}
			require.NoError(t, db.Create(&row).Error)
		}, SeedGeneration: func(t *testing.T, g publictest.GenerationRecord) {
			now := time.Now().UTC()
			prefix, err := mediaauth.MediaHLSObjectPrefix(g.FileID, g.GenerationID)
			require.NoError(t, err)
			digest := sha256.Sum256([]byte(g.GenerationID))
			count, size := int32(2), int64(4096)
			row := model.MediaGeneration{ID: g.GenerationID, FileID: g.FileID, Kind: "hls", ObjectPrefix: prefix, ManifestName: g.ManifestName, ManifestSHA256: digest[:], ObjectCount: &count, TotalSize: &size, Status: g.Status, ReadyAt: &now}
			if g.Status == "retired" {
				row.RetiredAt = &now
				after := now.Add(8 * time.Hour)
				row.DeleteAfter = &after
			}
			require.NoError(t, db.Create(&row).Error)
		}, SeedDerivative: func(t *testing.T, d public.Derivative) {
			require.NoError(t, db.Exec(`INSERT INTO file_derivative(id,file_id,type,asset_id,media_generation_id) VALUES (?::uuid,?::uuid,?,?::uuid,?::uuid)`, uuid.NewString(), d.FileID, d.Type, d.AssetID, d.MediaGenerationID).Error)
		}, SeedOwner: func(t *testing.T, o public.OwnerWitness) {
			profile := o.ResourceType
			if profile == "release" {
				profile = "compact"
			}
			require.NoError(t, db.Exec(`INSERT INTO content_document(id,profile) VALUES (?::uuid,?)`, o.DocumentID, profile).Error)
			switch o.ResourceType {
			case "post":
				require.NoError(t, db.Exec(`INSERT INTO post(id,status,content_document_id) VALUES (?::uuid,?,?::uuid)`, o.ResourceID, o.Status, o.DocumentID).Error)
			case "release":
				require.NoError(t, db.Exec(`INSERT INTO release(id,type,status,content_document_id) VALUES (?::uuid,'RELEASE_TYPE_ALBUM',?,?::uuid)`, o.ResourceID, o.Status, o.DocumentID).Error)
			default:
				t.Fatalf("unsupported owner fixture %q", o.ResourceType)
			}
		}, SeedRelation: func(t *testing.T, r publictest.Relation) {
			var position int64
			require.NoError(t, db.Table("content_block").Where("document_id = ?", r.Owner.DocumentID).Count(&position).Error)
			require.NoError(t, db.Exec(`INSERT INTO content_block(id,document_id,container_slot,position,kind,shared_data) VALUES (?::uuid,?::uuid,'root',?,?,'{}'::jsonb)`, r.BlockID, r.Owner.DocumentID, position, r.Kind).Error)
			require.NoError(t, db.Exec(`INSERT INTO content_block_attachment(block_id,reference_path,selector_kind,file_id,download_audience) VALUES (?::uuid,?,'active',?::uuid,?)`, r.BlockID, r.ReferencePath, r.FileID, string(r.Audience)).Error)
		}, SeedTrack: func(t *testing.T, r publictest.Track) {
			require.NoError(t, db.Exec(`INSERT INTO track(id,release_id,track_number,title,audio_original_file_id,download_audience) VALUES (?::uuid,?::uuid,1,'Track',?::uuid,?)`, r.ID, r.ReleaseID, r.FileID, string(r.Audience)).Error)
		}, SeedShareLink: func(t *testing.T, r public.ShareLink) {
			require.NoError(t, db.Exec(`INSERT INTO share_link(id,entity_type,entity_id,token,expires_at) VALUES (?::uuid,?,?::uuid,?,?)`, r.ID, r.EntityType, r.EntityID, uuid.NewString(), r.ExpiresAt).Error)
		}, UpdateOwner: func(ctx context.Context, bound public.Store, o public.OwnerWitness) error {
			return bound.(*store).tx.WithContext(ctx).Table(o.ResourceType).Where("id = ?", o.ResourceID).Update("status", o.Status).Error
		}}
	})
}
